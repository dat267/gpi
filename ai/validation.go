package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// Port of utils/validation.ts: tool-argument validation with coercion.
//
// D-row D6: upstream validates with TypeBox (a full TypeBox/JSON-Schema
// compiler). The Go port implements the JSON-Schema keyword subset TypeBox
// schemas actually generate for tool parameters — type (incl. unions),
// properties, required, items, additionalProperties, anyOf/oneOf/allOf,
// enum/const — with upstream's exact coercion rules. Keywords outside the
// subset (pattern, format, minimum, ...) do not constrain validation.

type jsonSchema struct {
	Raw                     map[string]json.RawMessage `json:"-"`
	Order                   []string                   `json:"-"`
	Types                   []string
	Properties              map[string]*jsonSchema
	PropertyOrder           []string
	Required                map[string]bool
	Items                   []*jsonSchema // tuple form
	ItemsOne                *jsonSchema   // single form
	AdditionalProperties    *jsonSchema
	HasAdditionalProperties bool
	AnyOf, OneOf, AllOf     []*jsonSchema
	Enum                    []json.RawMessage
	Const                   json.RawMessage
}

func parseJSONSchema(data json.RawMessage) *jsonSchema {
	schema := &jsonSchema{Raw: map[string]json.RawMessage{}, Required: map[string]bool{}}
	if len(data) == 0 || string(data) == "null" {
		return schema
	}
	var raw map[string]json.RawMessage
	if jsonUnmarshalStrict(data, &raw) != nil {
		return schema
	}
	schema.Raw = raw
	for key := range raw {
		schema.Order = append(schema.Order, key)
	}
	sort.Strings(schema.Order)
	if t, ok := raw["type"]; ok {
		var single string
		if jsonUnmarshalStrict(t, &single) == nil {
			schema.Types = []string{single}
		} else {
			var list []string
			_ = jsonUnmarshalStrict(t, &list)
			schema.Types = list
		}
	}
	if p, ok := raw["properties"]; ok {
		var props map[string]json.RawMessage
		if jsonUnmarshalStrict(p, &props) == nil {
			schema.Properties = map[string]*jsonSchema{}
			for key, propData := range props {
				schema.Properties[key] = parseJSONSchema(propData)
			}
			// Preserve property order from the encoded object.
			schema.PropertyOrder = orderedKeys(p)
		}
	}
	if r, ok := raw["required"]; ok {
		var required []string
		if jsonUnmarshalStrict(r, &required) == nil {
			for _, key := range required {
				schema.Required[key] = true
			}
		}
	}
	if items, ok := raw["items"]; ok && string(items) != "null" {
		var list []json.RawMessage
		if jsonUnmarshalStrict(items, &list) == nil {
			for _, item := range list {
				schema.Items = append(schema.Items, parseJSONSchema(item))
			}
		} else {
			schema.ItemsOne = parseJSONSchema(items)
		}
	}
	if ap, ok := raw["additionalProperties"]; ok {
		if string(ap) == "false" {
			schema.HasAdditionalProperties = true
		} else if string(ap) != "true" && string(ap) != "null" {
			schema.AdditionalProperties = parseJSONSchema(ap)
			schema.HasAdditionalProperties = true
		}
	}
	for _, combinator := range []string{"anyOf", "oneOf", "allOf"} {
		if c, ok := raw[combinator]; ok {
			var list []*jsonSchema
			var rawList []json.RawMessage
			if jsonUnmarshalStrict(c, &rawList) == nil {
				for _, item := range rawList {
					list = append(list, parseJSONSchema(item))
				}
			}
			switch combinator {
			case "anyOf":
				schema.AnyOf = list
			case "oneOf":
				schema.OneOf = list
			case "allOf":
				schema.AllOf = list
			}
		}
	}
	if e, ok := raw["enum"]; ok {
		var list []json.RawMessage
		if jsonUnmarshalStrict(e, &list) == nil {
			schema.Enum = list
		}
	}
	if c, ok := raw["const"]; ok {
		schema.Const = c
	}
	return schema
}

// schemaValue is decoded JSON preserving object key order.
type schemaValue struct {
	Null     bool
	Bool     bool
	Number   float64
	String   string
	IsBool   bool
	IsNumber bool
	IsString bool
	IsArray  bool
	Array    []*schemaValue
	Object   *schemaObject
}

type schemaObject struct {
	keys   []string
	values map[string]*schemaValue
}

func (o *schemaObject) get(key string) (*schemaValue, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.values[key]
	return v, ok
}

func (o *schemaObject) set(key string, value *schemaValue) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *schemaObject) delete(key string) {
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// decodeSchemaValue decodes JSON preserving object key order.
func decodeSchemaValue(data []byte) *schemaValue {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return &schemaValue{}
	}
	return decodeTokenValue(dec, tok)
}

func decodeTokenValue(dec *json.Decoder, tok json.Token) *schemaValue {
	switch t := tok.(type) {
	case nil:
		return &schemaValue{Null: true}
	case bool:
		return &schemaValue{Bool: t, IsBool: true}
	case json.Number:
		f, _ := t.Float64()
		return &schemaValue{Number: f, IsNumber: true}
	case string:
		return &schemaValue{String: t, IsString: true}
	case json.Delim:
		switch t {
		case '{':
			out := &schemaValue{Object: &schemaObject{values: map[string]*schemaValue{}}}
			for dec.More() {
				keyTok, _ := dec.Token()
				key := keyTok.(string)
				var raw json.RawMessage
				_ = dec.Decode(&raw)
				inner := decodeSchemaValue(raw)
				out.Object.set(key, inner)
			}
			_, _ = dec.Token() // }
			return out
		case '[':
			out := &schemaValue{IsArray: true}
			for dec.More() {
				var raw json.RawMessage
				_ = dec.Decode(&raw)
				out.Array = append(out.Array, decodeSchemaValue(raw))
			}
			_, _ = dec.Token() // ]
			return out
		}
	}
	return &schemaValue{}
}

// encodeSchemaValue re-encodes preserving key order.
func encodeSchemaValue(v *schemaValue) json.RawMessage {
	switch {
	case v.Null:
		return json.RawMessage("null")
	case v.IsBool:
		return mustMarshalJSON(v.Bool)
	case v.IsNumber:
		return mustMarshalJSON(v.Number)
	case v.IsString:
		return mustMarshalJSON(v.String)
	case v.IsArray:
		out := []byte{'['}
		for i, item := range v.Array {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, encodeSchemaValue(item)...)
		}
		return append(out, ']')
	case v.Object != nil:
		out := []byte{'{'}
		for i, key := range v.Object.keys {
			if i > 0 {
				out = append(out, ',')
			}
			keyEnc := mustMarshalJSON(key)
			out = append(out, keyEnc...)
			out = append(out, ':')
			out = append(out, encodeSchemaValue(v.Object.values[key])...)
		}
		return append(out, '}')
	default:
		return json.RawMessage("null")
	}
}

func matchesJSONType(value *schemaValue, typ string) bool {
	switch typ {
	case "number":
		return value.IsNumber
	case "integer":
		return value.IsNumber && value.Number == math.Trunc(value.Number)
	case "boolean":
		return value.IsBool
	case "string":
		return value.IsString
	case "null":
		return value.Null
	case "array":
		return value.IsArray
	case "object":
		return value.Object != nil
	default:
		return false
	}
}

// schemaValidator checks a value against a parsed schema (TypeBox Check
// subset).
func schemaCheck(value *schemaValue, schema *jsonSchema) bool {
	if schema == nil {
		return true
	}
	// anyOf/oneOf: any member matches (upstream delegates to TypeBox, whose
	// anyOf/oneOf both accept any member for validation purposes here).
	for _, variant := range append(append([]*jsonSchema{}, schema.AnyOf...), schema.OneOf...) {
		if schemaCheck(value, variant) {
			goto typeCheck
		}
	}
	if len(schema.AnyOf) > 0 || len(schema.OneOf) > 0 {
		return false
	}
typeCheck:
	for _, nested := range schema.AllOf {
		if !schemaCheck(value, nested) {
			return false
		}
	}
	if schema.Const != nil {
		if string(encodeSchemaValue(value)) != string(schema.Const) {
			return false
		}
	}
	if len(schema.Enum) > 0 {
		encoded := string(encodeSchemaValue(value))
		matched := false
		for _, option := range schema.Enum {
			if string(option) == encoded {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	if len(schema.Types) > 0 {
		matched := false
		for _, typ := range schema.Types {
			if matchesJSONType(value, typ) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	if value.Object != nil {
		for key, property := range schema.Properties {
			field, ok := value.Object.get(key)
			if !ok {
				if schema.Required[key] {
					return false
				}
				continue
			}
			if !schemaCheck(field, property) {
				return false
			}
		}
		if schema.HasAdditionalProperties && schema.AdditionalProperties != nil {
			for _, key := range value.Object.keys {
				if _, declared := schema.Properties[key]; declared {
					continue
				}
				field, _ := value.Object.get(key)
				if !schemaCheck(field, schema.AdditionalProperties) {
					return false
				}
			}
		}
	}
	if value.IsArray && schema.ItemsOne != nil {
		for _, item := range value.Array {
			if !schemaCheck(item, schema.ItemsOne) {
				return false
			}
		}
	}
	if value.IsArray && len(schema.Items) > 0 {
		for i, item := range value.Array {
			if i >= len(schema.Items) {
				break
			}
			if !schemaCheck(item, schema.Items[i]) {
				return false
			}
		}
	}
	return true
}

// coercePrimitiveByType ports upstream's primitive coercions.
func coercePrimitiveByType(value *schemaValue, typ string) *schemaValue {
	switch typ {
	case "number", "integer":
		if value.Null {
			return &schemaValue{IsNumber: true, Number: 0}
		}
		if value.IsString && strings.TrimSpace(value.String) != "" {
			var parsed float64
			if _, err := fmt.Sscanf(strings.TrimSpace(value.String), "%g", &parsed); err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
				if typ == "number" || parsed == math.Trunc(parsed) {
					return &schemaValue{IsNumber: true, Number: parsed}
				}
			}
		}
		if value.IsBool {
			n := 0.0
			if value.Bool {
				n = 1
			}
			return &schemaValue{IsNumber: true, Number: n}
		}
		return value
	case "boolean":
		if value.Null {
			return &schemaValue{IsBool: true, Bool: false}
		}
		if value.IsString {
			if value.String == "true" {
				return &schemaValue{IsBool: true, Bool: true}
			}
			if value.String == "false" {
				return &schemaValue{IsBool: true, Bool: false}
			}
		}
		if value.IsNumber {
			if value.Number == 1 {
				return &schemaValue{IsBool: true, Bool: true}
			}
			if value.Number == 0 {
				return &schemaValue{IsBool: true, Bool: false}
			}
		}
		return value
	case "string":
		if value.Null {
			return &schemaValue{IsString: true, String: ""}
		}
		if value.IsNumber {
			return &schemaValue{IsString: true, String: trimFloat(value.Number)}
		}
		if value.IsBool {
			if value.Bool {
				return &schemaValue{IsString: true, String: "true"}
			}
			return &schemaValue{IsString: true, String: "false"}
		}
		return value
	case "null":
		if (value.IsString && value.String == "") || (value.IsNumber && value.Number == 0) || (value.IsBool && !value.Bool) {
			return &schemaValue{Null: true}
		}
		return value
	default:
		return value
	}
}

// trimFloat formats a number like JS String(number) for common values.
func trimFloat(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return formatInt(int64(f))
	}
	return formatFloat(f)
}

func formatInt(n int64) string     { return fmt.Sprintf("%d", n) }
func formatFloat(f float64) string { return fmt.Sprintf("%g", f) }

func coerceWithJSONSchema(value *schemaValue, schema *jsonSchema) *schemaValue {
	next := value
	for _, nested := range schema.AllOf {
		next = coerceWithJSONSchema(next, nested)
	}
	if len(schema.AnyOf) > 0 {
		next = coerceWithUnionSchema(next, schema.AnyOf)
	}
	if len(schema.OneOf) > 0 {
		next = coerceWithUnionSchema(next, schema.OneOf)
	}
	matchesUnionMember := false
	if len(schema.Types) > 1 {
		for _, typ := range schema.Types {
			if matchesJSONType(next, typ) {
				matchesUnionMember = true
			}
		}
	}
	if len(schema.Types) > 0 && !matchesUnionMember {
		for _, typ := range schema.Types {
			candidate := coercePrimitiveByType(next, typ)
			if !schemaValueEqual(candidate, next) {
				next = candidate
				break
			}
		}
	}
	types := schema.Types
	isObjectSchema := false
	isArraySchema := false
	for _, typ := range types {
		if typ == "object" {
			isObjectSchema = true
		}
		if typ == "array" {
			isArraySchema = true
		}
	}
	if isObjectSchema && next.Object != nil {
		applySchemaObjectCoercion(next.Object, schema)
	}
	if isArraySchema && next.IsArray {
		applySchemaArrayCoercion(next.Array, schema)
	}
	return next
}

func schemaValueEqual(a, b *schemaValue) bool {
	if a == b {
		return true
	}
	return string(encodeSchemaValue(a)) == string(encodeSchemaValue(b))
}

func coerceWithUnionSchema(value *schemaValue, schemas []*jsonSchema) *schemaValue {
	for _, schema := range schemas {
		if schemaCheck(value, schema) {
			return value
		}
	}
	for _, schema := range schemas {
		candidate := decodeSchemaValue(encodeSchemaValue(value))
		coerced := coerceWithJSONSchema(candidate, schema)
		if schemaCheck(coerced, schema) {
			return coerced
		}
	}
	return value
}

func applySchemaObjectCoercion(object *schemaObject, schema *jsonSchema) {
	for key, propertySchema := range schema.Properties {
		if field, ok := object.get(key); ok {
			object.set(key, coerceWithJSONSchema(field, propertySchema))
		}
	}
	if schema.AdditionalProperties != nil && schema.HasAdditionalProperties {
		for _, key := range append([]string{}, object.keys...) {
			if _, declared := schema.Properties[key]; declared {
				continue
			}
			field, _ := object.get(key)
			object.set(key, coerceWithJSONSchema(field, schema.AdditionalProperties))
		}
	}
}

func applySchemaArrayCoercion(values []*schemaValue, schema *jsonSchema) {
	if len(schema.Items) > 0 {
		for index, item := range values {
			if index >= len(schema.Items) {
				break
			}
			values[index] = coerceWithJSONSchema(item, schema.Items[index])
		}
		return
	}
	if schema.ItemsOne != nil {
		for index, item := range values {
			values[index] = coerceWithJSONSchema(item, schema.ItemsOne)
		}
	}
}

// normalizeOptionalNulls drops explicit nulls on optional properties that
// don't accept null (upstream normalizeOptionalNulls).
func normalizeOptionalNulls(value *schemaValue, schema *jsonSchema) {
	if value.IsArray {
		if len(schema.Items) > 0 {
			for index, item := range value.Array {
				if index < len(schema.Items) {
					normalizeOptionalNulls(item, schema.Items[index])
				}
			}
		} else if schema.ItemsOne != nil {
			for _, item := range value.Array {
				normalizeOptionalNulls(item, schema.ItemsOne)
			}
		}
		return
	}
	if value.Object == nil || len(schema.Properties) == 0 {
		return
	}
	for _, key := range append([]string{}, value.Object.keys...) {
		propertySchema, ok := schema.Properties[key]
		if !ok {
			continue
		}
		field, _ := value.Object.get(key)
		if field.Null && !schema.Required[key] {
			if _, hasRef := propertySchema.Raw["$ref"]; !hasRef && !schemaCheck(field, propertySchema) {
				value.Object.delete(key)
				continue
			}
		}
		normalizeOptionalNulls(field, propertySchema)
	}
}

// validatorCache mirrors upstream's WeakMap validator cache.
var validatorCache sync.Map // json.RawMessage string → *jsonSchema

func getValidator(schema json.RawMessage) *jsonSchema {
	key := string(schema)
	if cached, ok := validatorCache.Load(key); ok {
		return cached.(*jsonSchema)
	}
	parsed := parseJSONSchema(schema)
	validatorCache.Store(key, parsed)
	return parsed
}

// ValidateToolArguments validates (and coerces) tool call arguments against
// the tool's schema (port of validateToolArguments). Returns the validated
// arguments or an error with the formatted validation message.
func ValidateToolArguments(tool Tool, toolCall ToolCall) (json.RawMessage, error) {
	schema := getValidator(tool.Parameters)
	args := decodeSchemaValue(toolCall.Arguments)
	normalizeOptionalNulls(args, schema)

	// TypeBox schemas carry a Kind symbol; plain JSON Schema (the Go port's
	// universal case) always applies the JSON-Schema coercion pass.
	coerced := coerceWithJSONSchema(args, schema)
	if !schemaValueEqual(coerced, args) {
		args = coerced
	}

	if schemaCheck(args, schema) {
		return encodeSchemaValue(args), nil
	}

	var errorLines []string
	if len(schema.Types) > 0 {
		errorLines = append(errorLines, fmt.Sprintf("  - root: Expected %s", strings.Join(schema.Types, " | ")))
	}
	for key := range schema.Required {
		if _, ok := args.Object.get(key); !ok && args.Object != nil {
			errorLines = append(errorLines, fmt.Sprintf("  - %s: required property missing", key))
		}
	}
	if len(errorLines) == 0 {
		errorLines = append(errorLines, "  - root: Unknown validation error")
	}
	var received json.RawMessage = toolCall.Arguments
	if len(toolCall.Arguments) > 0 {
		var buf bytes.Buffer
		if err := json.Indent(&buf, toolCall.Arguments, "", "  "); err == nil {
			received = json.RawMessage(buf.String())
		}
	}
	return nil, fmt.Errorf("Validation failed for tool %q:\n%s\n\nReceived arguments:\n%s",
		toolCall.Name, strings.Join(errorLines, "\n"), received)
}

// ValidateToolCall finds a tool by name and validates the tool call arguments
// against its schema (port of validateToolCall).
func ValidateToolCall(tools []Tool, toolCall ToolCall) (json.RawMessage, error) {
	var tool *Tool
	for i := range tools {
		if tools[i].Name == toolCall.Name {
			tool = &tools[i]
			break
		}
	}
	if tool == nil {
		return nil, fmt.Errorf("Tool %q not found", toolCall.Name)
	}
	return ValidateToolArguments(*tool, toolCall)
}
