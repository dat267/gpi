package telemetry

import (
	"errors"
	"testing"
)

// Telemetry tests: the conformance suite run against the in-memory adapter,
// plus the noop context and the typed span starter.

type memoryFixture struct {
	context *InMemoryTelemetryContext
}

func (f *memoryFixture) Context() TelemetryContext { return f.context }
func (f *memoryFixture) Spans() []RecordedTelemetrySpan {
	return f.context.GetSpans()
}
func (f *memoryFixture) Close() error { return nil }

// TestInMemoryAdapterConformance runs upstream's whole conformance suite
// against the in-memory recorder.
func TestInMemoryAdapterConformance(t *testing.T) {
	RunAdapterConformance(t, func() (AdapterFixture, error) {
		return &memoryFixture{context: NewInMemoryTelemetryContext()}, nil
	})
}

func TestNoopContext(t *testing.T) {
	if NoopTelemetryContext == nil {
		t.Fatal("noop context must exist")
	}
	calls := 0
	result, err := StartSpanValue(NoopTelemetryContext, SpanOptions{Name: "x", Attributes: SpanAttributes{"a": 1}},
		func(span TelemetrySpan) (string, error) {
			calls++
			span.AddEvent("event", SpanAttributes{"b": true})
			span.SetAttributes(SpanAttributes{"c": "d"})
			span.SetStatus(ErrorStatus("E", "m"))
			if err := span.StartSpan(SpanOptions{Name: "child"}, func(child TelemetrySpan) error { return nil }); err != nil {
				return "", err
			}
			return "done", nil
		})
	if err != nil || result != "done" || calls != 1 {
		t.Fatalf("result=%q calls=%d err=%v", result, calls, err)
	}
	// Failures propagate unchanged through the noop context.
	sentinel := errors.New("boom")
	if err := NoopTelemetryContext.StartSpan(SpanOptions{Name: "x"},
		func(span TelemetrySpan) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v", err)
	}
}

func TestRecordedSpansAreDetachedCopies(t *testing.T) {
	context := NewInMemoryTelemetryContext()
	arr := []string{"a", "b"}
	if err := context.StartSpan(SpanOptions{Name: "span", Attributes: SpanAttributes{"list": arr}},
		func(span TelemetrySpan) error {
			span.AddEvent("event", SpanAttributes{"list": arr})
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	snapshot := context.GetSpans()
	// Mutating the source slice must not change the recorded snapshot.
	arr[0] = "mutated"
	if snapshot[0].Attributes["list"].([]string)[0] != "a" {
		t.Fatalf("snapshot attributes aliased the source: %v", snapshot[0].Attributes["list"])
	}
	if snapshot[0].Events[0].Attributes["list"].([]string)[0] != "a" {
		t.Fatalf("snapshot event attributes aliased the source: %v", snapshot[0].Events[0].Attributes["list"])
	}
	// Mutating a returned snapshot must not corrupt the recorder.
	snapshot[0].Attributes["injected"] = true
	if _, exists := context.GetSpans()[0].Attributes["injected"]; exists {
		t.Fatal("returned snapshots must be detached")
	}
}

func TestSpanIDsAndEndSequences(t *testing.T) {
	context := NewInMemoryTelemetryContext()
	if err := context.StartSpan(SpanOptions{Name: "one"}, func(span TelemetrySpan) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := context.StartSpan(SpanOptions{Name: "two"}, func(span TelemetrySpan) error { return nil }); err != nil {
		t.Fatal(err)
	}
	spans := context.GetSpans()
	if len(spans) != 2 || spans[0].ID != 1 || spans[1].ID != 2 {
		t.Fatalf("spans = %+v", spans)
	}
	// End sequences start at 1 and increase in settle order.
	if spans[0].EndSequence == nil || *spans[0].EndSequence != 1 ||
		spans[1].EndSequence == nil || *spans[1].EndSequence != 2 {
		t.Fatalf("end sequences = %v / %v", spans[0].EndSequence, spans[1].EndSequence)
	}
}

func TestTypedSpanStarter(t *testing.T) {
	context := NewInMemoryTelemetryContext()
	schema := DefineTelemetrySchema(TelemetrySchemaDefinition{
		Version: 1,
		Spans: map[string]TelemetrySpanDefinition{
			"root": {Description: "root span", Parents: "root_or_external", StatusDefault: "ok"},
		},
	})
	start := CreateTypedSpanStarter(context, schema)
	err := start("root", SpanAttributes{"k": "v"}, func(span TelemetrySpan, startChild SpanStarter) error {
		if err := startChild("child", nil, func(child TelemetrySpan, _ SpanStarter) error {
			child.AddEvent("nested", nil)
			return nil
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	spans := context.GetSpans()
	if len(spans) != 2 || spans[0].Name != "root" || spans[1].Name != "child" {
		t.Fatalf("spans = %+v", spans)
	}
	// The child is parented to the root span.
	if spans[1].ParentID == nil || *spans[1].ParentID != spans[0].ID {
		t.Fatalf("child parent = %v; want %d", spans[1].ParentID, spans[0].ID)
	}
	if len(spans[1].Events) != 1 || spans[1].Events[0].Name != "nested" {
		t.Fatalf("child events = %+v", spans[1].Events)
	}
}

func TestPanicInCallbackSettlesAsError(t *testing.T) {
	context := NewInMemoryTelemetryContext()
	var err error
	func() {
		defer func() { _ = recover() }()
		err = context.StartSpan(SpanOptions{Name: "panicking"},
			func(span TelemetrySpan) error { panic("boom") })
	}()
	if err == nil {
		t.Fatal("panic must surface as an error")
	}
	spans := context.GetSpans()
	if len(spans) != 1 || spans[0].Status.Status != StatusError || !spans[0].Settled {
		t.Fatalf("span = %+v", spans)
	}
}
