package ai

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Port of api/constrained-sampling.ts.

type strictSchemaError struct{ msg string }

func (e *strictSchemaError) Error() string { return e.msg }

var unsupportedStrictSchemaKeys = []string{
	"$ref", "$defs", "definitions", "allOf", "oneOf", "patternProperties",
	"dependentSchemas", "dependencies", "unevaluatedProperties", "propertyNames",
	"contains", "prefixItems", "not", "if", "then", "else",
}

// schemaNode is a decoded JSON-schema object preserving key order for
// property maps (Go maps would sort; use ordered maps).
type schemaNode struct {
	fields map[string]json.RawMessage
	keys   []string
}

func decodeSchemaNode(data []byte) (*schemaNode, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("not an object")
	}
	node := &schemaNode{fields: map[string]json.RawMessage{}}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if _, seen := node.fields[key]; !seen {
			node.keys = append(node.keys, key)
		}
		node.fields[key] = raw
	}
	return node, nil
}

func (n *schemaNode) encode() ([]byte, error) {
	var buf []byte
	buf = append(buf, '{')
	for i, key := range n.keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		keyEnc, _ := MarshalJSON(key)
		buf = append(buf, keyEnc...)
		buf = append(buf, ':')
		buf = append(buf, n.fields[key]...)
	}
	return append(buf, '}'), nil
}

func (n *schemaNode) raw(key string) (json.RawMessage, bool) {
	v, ok := n.fields[key]
	return v, ok
}

func (n *schemaNode) set(key string, value json.RawMessage) {
	if _, seen := n.fields[key]; !seen {
		n.keys = append(n.keys, key)
	}
	n.fields[key] = value
}

func (n *schemaNode) delete(key string) {
	delete(n.fields, key)
	for i, k := range n.keys {
		if k == key {
			n.keys = append(n.keys[:i], n.keys[i+1:]...)
			break
		}
	}
}

func schemaTypes(data json.RawMessage) []string {
	var single string
	if err := jsonUnmarshalStrict(data, &single); err == nil {
		return []string{single}
	}
	var list []string
	if err := jsonUnmarshalStrict(data, &list); err == nil {
		return list
	}
	return nil
}

func isStructuredSchema(data json.RawMessage) bool {
	node, err := decodeSchemaNode(data)
	if err != nil {
		return false
	}
	types := []string{}
	if t, ok := node.raw("type"); ok {
		types = schemaTypes(t)
	}
	for _, ty := range types {
		if ty == "object" || ty == "array" {
			return true
		}
	}
	_, hasProps := node.raw("properties")
	_, hasItems := node.raw("items")
	return hasProps || hasItems
}

func schemaAllowsNull(data json.RawMessage) bool {
	node, err := decodeSchemaNode(data)
	if err != nil {
		return false
	}
	if t, ok := node.raw("type"); ok {
		for _, ty := range schemaTypes(t) {
			if ty == "null" {
				return true
			}
		}
	}
	if c, ok := node.raw("const"); ok && string(c) == "null" {
		return true
	}
	if e, ok := node.raw("enum"); ok {
		var list []json.RawMessage
		if err := jsonUnmarshalStrict(e, &list); err == nil {
			for _, v := range list {
				if string(v) == "null" {
					return true
				}
			}
		}
	}
	if anyOf, ok := node.raw("anyOf"); ok {
		var list []json.RawMessage
		if err := jsonUnmarshalStrict(anyOf, &list); err == nil {
			for _, variant := range list {
				if schemaAllowsNull(variant) {
					return true
				}
			}
		}
	}
	return false
}

func makeSchemaNodeStrict(node *schemaNode) error {
	for _, key := range unsupportedStrictSchemaKeys {
		if _, ok := node.raw(key); ok {
			return &strictSchemaError{msg: key + " schemas are unsupported"}
		}
	}

	if anyOfData, ok := node.raw("anyOf"); ok {
		var list []json.RawMessage
		if err := jsonUnmarshalStrict(anyOfData, &list); err != nil || len(list) == 0 {
			return &strictSchemaError{msg: "anyOf must contain at least one schema"}
		}
		for i, variant := range list {
			if isStructuredSchema(variant) {
				return &strictSchemaError{msg: "object and array unions are unsupported"}
			}
			strict, err := makeStrictSubSchema(variant)
			if err != nil {
				return err
			}
			list[i] = strict
		}
		enc, _ := MarshalJSON(list)
		node.set("anyOf", enc)
	}

	if itemsData, ok := node.raw("items"); ok {
		var list []json.RawMessage
		if err := jsonUnmarshalStrict(itemsData, &list); err == nil {
			return &strictSchemaError{msg: "tuple schemas are unsupported"}
		}
		strict, err := makeStrictSubSchema(itemsData)
		if err != nil {
			return err
		}
		node.set("items", strict)
	}

	types := []string{}
	if t, ok := node.raw("type"); ok {
		types = schemaTypes(t)
	}
	isObjectSchema := len(types) == 1 && types[0] == "object"

	if _, hasProps := node.raw("properties"); hasProps && !isObjectSchema {
		return &strictSchemaError{msg: "properties require type object"}
	}
	if !isObjectSchema {
		return nil
	}
	if ap, ok := node.raw("additionalProperties"); ok && string(ap) != "false" {
		return &strictSchemaError{msg: "schema-valued or true additionalProperties is unsupported"}
	}
	propsData, hasProps := node.raw("properties")
	var properties map[string]json.RawMessage
	if hasProps {
		if err := jsonUnmarshalStrict(propsData, &properties); err != nil {
			return &strictSchemaError{msg: "object properties must be a schema map"}
		}
	}
	var required []string
	if reqData, ok := node.raw("required"); ok {
		if err := jsonUnmarshalStrict(reqData, &required); err != nil {
			return &strictSchemaError{msg: "object required must be a string array"}
		}
	}

	// Property key order: decode the properties object with order preserved.
	var orderedProps []string
	{
		var rawMap map[string]json.RawMessage
		_ = jsonUnmarshalStrict(propsData, &rawMap)
		// Recover upstream insertion order from the encoded bytes.
		orderedProps = orderedKeys(propsData)
		_ = rawMap
	}

	requiredSet := map[string]bool{}
	for _, key := range required {
		requiredSet[key] = true
	}
	for _, key := range required {
		if _, ok := properties[key]; !ok {
			return &strictSchemaError{msg: "required contains an unknown property"}
		}
	}
	for _, key := range orderedProps {
		property := properties[key]
		if err := strictSubSchemaInPlace(property); err != nil {
			return err
		}
		// Re-read: strictSubSchemaInPlace mutates via re-encoding below.
		if !requiredSet[key] && !schemaAllowsNull(properties[key]) {
			anyOf := []json.RawMessage{properties[key], json.RawMessage(`{"type":"null"}`)}
			enc, _ := MarshalJSON(anyOf)
			properties[key] = enc
		}
	}
	newRequired := append([]string{}, orderedProps...)
	reqEnc, _ := MarshalJSON(newRequired)
	node.set("required", reqEnc)
	node.set("additionalProperties", json.RawMessage("false"))
	if hasProps {
		propEnc, _ := MarshalJSON(properties)
		// Re-encode with deterministic key order matching the original where
		// possible; Go maps sort keys, which changes byte order but not
		// schema semantics. Upstream preserves insertion order; sorted order
		// is a D-row (D4) — schemas are semantically equal, wire bytes differ.
		node.set("properties", sortedObjectJSON(propEnc))
	}
	return nil
}

// sortedObjectJSON re-encodes an object with sorted keys (canonical form).
func sortedObjectJSON(data json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if err := jsonUnmarshalStrict(data, &m); err != nil {
		return data
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf []byte
	buf = append(buf, '{')
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kEnc, _ := MarshalJSON(k)
		buf = append(buf, kEnc...)
		buf = append(buf, ':')
		buf = append(buf, m[k]...)
	}
	return append(buf, '}')
}

// orderedKeys recovers the encoded key order of a JSON object.
func orderedKeys(data json.RawMessage) []string {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		keys = append(keys, tok.(string))
		var raw json.RawMessage
		_ = dec.Decode(&raw)
	}
	return keys
}

// strictSubSchemaInPlace makes a raw sub-schema strict, re-encoding it.
func strictSubSchemaInPlace(data json.RawMessage) error {
	node, err := decodeSchemaNode(data)
	if err != nil {
		return &strictSchemaError{msg: "boolean schemas are unsupported"}
	}
	if err := makeSchemaNodeStrict(node); err != nil {
		return err
	}
	return nil
}

// makeStrictSubSchema returns the strict encoding of a sub-schema.
func makeStrictSubSchema(data json.RawMessage) (json.RawMessage, error) {
	node, err := decodeSchemaNode(data)
	if err != nil {
		return nil, &strictSchemaError{msg: "root schema must have type object"}
	}
	if err := makeSchemaNodeStrict(node); err != nil {
		return nil, err
	}
	t, _ := node.raw("type")
	if string(t) != `"object"` {
		return nil, &strictSchemaError{msg: "root schema must have type object"}
	}
	enc, err := node.encode()
	if err != nil {
		return nil, err
	}
	return enc, nil
}

// MakeStrictJSONSchema converts a tool schema to the strict subset expected
// by provider constrained sampling (port of makeStrictJsonSchema).
func MakeStrictJSONSchema(schema json.RawMessage) (json.RawMessage, error) {
	return makeStrictSubSchema(schema)
}

// GetJSONSchemaToolParameters returns the tool parameters for a strictness
// mode (port of getJsonSchemaToolParameters).
func GetJSONSchemaToolParameters(tool Tool, strict bool) json.RawMessage {
	if !strict {
		return tool.Parameters
	}
	strictSchema, err := MakeStrictJSONSchema(tool.Parameters)
	if err != nil {
		return tool.Parameters
	}
	return strictSchema
}

// ResolveJSONSchemaStrictSampling resolves whether a tool's json_schema
// constrained sampling applies (port of resolveJsonSchemaStrictSampling).
func ResolveJSONSchemaStrictSampling(tool Tool, supportsStrictMode bool) (strict bool, unset bool, err error) {
	config := tool.ConstrainedSampling
	if !config.Set || config.False || config.Config == nil || config.Config.Type != "json_schema" {
		return false, true, nil
	}
	cfg := config.Config
	if supportsStrictMode {
		if _, err := MakeStrictJSONSchema(tool.Parameters); err != nil {
			if _, ok := err.(*strictSchemaError); ok {
				if cfg.Strict != "require" {
					return false, true, nil
				}
				return false, false, fmt.Errorf("Tool %q requires JSON-schema constrained sampling, but %s.", tool.Name, err.Error())
			}
			return false, false, err
		}
		return true, false, nil
	}
	if cfg.Strict == "require" {
		return false, false, fmt.Errorf("Tool %q requires JSON-schema constrained sampling, but strict tools are unsupported.", tool.Name)
	}
	return false, true, nil
}
