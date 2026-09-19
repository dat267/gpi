// Package telemetry is a Go port of @earendil-works/pi-telemetry
// (pi/packages/telemetry): vendor-neutral telemetry contracts, the noop and
// in-memory implementations, and the adapter conformance suite.
//
// Ground truth: pi/packages/telemetry/src at the pinned upstream commit.
//
// D-row D13: upstream's startSpan is generic over the callback's return type
// (`startSpan<T>(options, callback: (span) => T | Promise<T>): Promise<T>`).
// Go methods cannot have type parameters, so the interface contract uses an
// error return (the Go idiom) and StartSpanValue is the value-returning
// helper. Upstream's "unreadable payload" cases use JavaScript Proxies whose
// property reads throw; the Go equivalents are unusable attribute values and
// panicking errors, which the recorder must swallow just the same.
package telemetry

import "fmt"

// AttributeValue is one span attribute value.
type AttributeValue = any

// SpanAttributes maps attribute names to values. A nil value is treated as
// absent (upstream's `undefined`).
type SpanAttributes = map[string]AttributeValue

// SpanOptions start one span.
type SpanOptions struct {
	Name       string
	Attributes SpanAttributes
}

// SpanError describes a failure attached to an error status.
type SpanError struct {
	Name    string
	Message string
}

// Status names.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// SpanStatus is an ok status or an error status with optional details.
type SpanStatus struct {
	Status string
	Error  *SpanError
}

// OKStatus is the default status.
func OKStatus() SpanStatus { return SpanStatus{Status: StatusOK} }

// ErrorStatus builds an error status.
func ErrorStatus(name, message string) SpanStatus {
	return SpanStatus{Status: StatusError, Error: &SpanError{Name: name, Message: message}}
}

// TelemetryContext starts spans.
type TelemetryContext interface {
	// StartSpan runs fn with a new span. fn runs synchronously (the span is
	// admitted before fn is invoked). A panic in fn propagates after the span
	// settles as an error.
	StartSpan(options SpanOptions, fn func(span TelemetrySpan) error) error
}

// TelemetrySpan is a live span; it is itself a context for child spans.
type TelemetrySpan interface {
	TelemetryContext
	AddEvent(name string, attributes SpanAttributes)
	SetAttributes(attributes SpanAttributes)
	SetStatus(status SpanStatus)
}

// StartSpanValue runs fn with a new span and returns its value.
func StartSpanValue[T any](ctx TelemetryContext, options SpanOptions, fn func(span TelemetrySpan) (T, error)) (T, error) {
	var value T
	err := ctx.StartSpan(options, func(span TelemetrySpan) error {
		result, fnErr := fn(span)
		if fnErr != nil {
			return fnErr
		}
		value = result
		return nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}

// Schema definition data (upstream's TelemetrySchemaDefinition and friends).
// These are serializable descriptions; upstream's type-level inference has no
// Go counterpart, so they are plain data plus the identity helper.

// TelemetryAttributeType names an attribute's wire type.
type TelemetryAttributeType = string

const (
	AttributeTypeString   TelemetryAttributeType = "string"
	AttributeTypeNumber   TelemetryAttributeType = "number"
	AttributeTypeBoolean  TelemetryAttributeType = "boolean"
	AttributeTypeStrings  TelemetryAttributeType = "string[]"
	AttributeTypeNumbers  TelemetryAttributeType = "number[]"
	AttributeTypeBooleans TelemetryAttributeType = "boolean[]"
)

// TelemetryAttributeMetadata describes one attribute.
type TelemetryAttributeMetadata struct {
	Description string
	Sensitive   bool
	// Cardinality is "low" or "high".
	Cardinality string
}

// TelemetryAttributeDefinition is metadata plus the attribute's type and
// optional allowed values/examples.
type TelemetryAttributeDefinition struct {
	TelemetryAttributeMetadata
	Type          TelemetryAttributeType
	Values        []any
	ElementValues []any
	Examples      []any
}

// TelemetryEventDefinition describes one span event.
type TelemetryEventDefinition struct {
	Description string
	Attributes  map[string]TelemetryAttributeDefinition
}

// TelemetrySpanDefinition describes one span in a schema.
type TelemetrySpanDefinition struct {
	Description string
	// Parents: "any" | "root_or_external" | a span-name list.
	Parents         string
	ParentSpans     []string
	StartAttributes map[string]TelemetryAttributeDefinition
	EndAttributes   map[string]TelemetryAttributeDefinition
	Events          map[string]TelemetryEventDefinition
	// StatusDefault is "ok"; StatusErrorWhen names the error condition.
	StatusDefault   string
	StatusErrorWhen string
}

// TelemetrySchemaDefinition is one schema version.
type TelemetrySchemaDefinition struct {
	Version int
	Spans   map[string]TelemetrySpanDefinition
}

// DefineTelemetrySchema is the typed identity helper for serializable schema
// data (upstream defineTelemetrySchema).
func DefineTelemetrySchema(schema TelemetrySchemaDefinition) TelemetrySchemaDefinition {
	return schema
}

// SpanStarter starts one schema-typed span and hands the callback a starter
// for its children (upstream's TypedSpanStarter; schema type inference is not
// representable in Go, so names and attributes are untyped here).
type SpanStarter func(name string, attributes SpanAttributes, fn func(span TelemetrySpan, startChild SpanStarter) error) error

// CreateTypedSpanStarter binds an explicit parent context to a span starter
// (upstream createTypedSpanStarter). Schemas are accepted for documentation
// and validation callers do themselves; no runtime schema validation happens.
func CreateTypedSpanStarter(ctx TelemetryContext, schemas ...TelemetrySchemaDefinition) SpanStarter {
	var start SpanStarter
	start = func(name string, attributes SpanAttributes, fn func(span TelemetrySpan, startChild SpanStarter) error) error {
		return ctx.StartSpan(SpanOptions{Name: name, Attributes: attributes}, func(span TelemetrySpan) error {
			return fn(span, CreateTypedSpanStarter(span))
		})
	}
	return start
}

// validateAttributeValue reports whether a value is an attribute value.
func validateAttributeValue(value AttributeValue) error {
	switch typed := value.(type) {
	case string, bool, float64, int, int64:
		return nil
	case []string, []float64, []bool, []int, []int64:
		return nil
	default:
		return fmt.Errorf("telemetry: unsupported attribute value type %T", typed)
	}
}
