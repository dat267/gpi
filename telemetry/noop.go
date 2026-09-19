package telemetry

// Port of src/noop.ts: the shared no-op context used when an application does
// not provide one.

type noopTelemetrySpan struct{}

// NoopTelemetryContext is the frozen singleton no-op context.
var NoopTelemetryContext TelemetryContext = noopTelemetrySpan{}

func (noopTelemetrySpan) StartSpan(options SpanOptions, fn func(span TelemetrySpan) error) error {
	return fn(noopTelemetrySpan{})
}

func (noopTelemetrySpan) AddEvent(name string, attributes SpanAttributes) {}

func (noopTelemetrySpan) SetAttributes(attributes SpanAttributes) {}

func (noopTelemetrySpan) SetStatus(status SpanStatus) {}
