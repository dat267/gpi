package telemetry

import (
	"errors"
	"fmt"
	"strings"
)

// Port of src/testing/conformance.ts: the runner-independent conformance
// suite for callback telemetry adapters.

// AdapterFixture is a fresh adapter instance plus a normalized snapshot reader
// owned by one conformance case.
type AdapterFixture interface {
	Context() TelemetryContext
	Spans() []RecordedTelemetrySpan
	// Close releases the fixture's resources.
	Close() error
}

// AdapterFixtureFactory creates an isolated fixture per conformance case.
type AdapterFixtureFactory func() (AdapterFixture, error)

// AdapterConformanceCase is one runner-independent case.
type AdapterConformanceCase struct {
	Group string
	Name  string
	Run   func(fixture AdapterFixture) error
}

func conformanceCase(factory AdapterFixtureFactory, group, name string, test func(fixture AdapterFixture) error) AdapterConformanceCase {
	return AdapterConformanceCase{Group: group, Name: name, Run: func(fixture AdapterFixture) error {
		defer fixture.Close()
		return test(fixture)
	}}
}

func findSpan(spans []RecordedTelemetrySpan, name string) (*RecordedTelemetrySpan, error) {
	for i := range spans {
		if spans[i].Name == name {
			return &spans[i], nil
		}
	}
	return nil, fmt.Errorf("Expected recorded span %s", name)
}

// panickingError is the Go analogue of upstream's unreadable error payload:
// inspecting it panics, and the recorder must not care.
type panickingError struct{}

func (panickingError) Error() string { return "read" }

// unreadableAttributes is the Go analogue of upstream's unreadable attribute
// payload: an unusable value that must be rejected atomically.
var unreadableAttributes = SpanAttributes{"secret": make(chan int)}

// AdapterConformance builds the cases for one adapter factory.
func AdapterConformance(factory AdapterFixtureFactory) []AdapterConformanceCase {
	return []AdapterConformanceCase{
		conformanceCase(factory, "callback lifecycle", "admits once synchronously and preserves the result",
			func(fixture AdapterFixture) error {
				admitted := false
				calls := 0
				expected := 42
				result, err := StartSpanValue(fixture.Context(), SpanOptions{Name: "success"},
					func(span TelemetrySpan) (int, error) {
						admitted = true
						calls++
						return expected, nil
					})
				if err != nil {
					return err
				}
				if !admitted || calls != 1 || result != expected {
					return fmt.Errorf("admitted=%v calls=%d result=%d", admitted, calls, result)
				}
				spans := fixture.Spans()
				span, err := findSpan(spans, "success")
				if err != nil {
					return err
				}
				if span.Status.Status != StatusOK || !span.Settled {
					return fmt.Errorf("status=%+v settled=%v", span.Status, span.Settled)
				}
				return nil
			}),

		conformanceCase(factory, "callback lifecycle", "preserves synchronous and asynchronous rejection values",
			func(fixture AdapterFixture) error {
				syncError := errors.New("sync")
				if err := fixture.Context().StartSpan(SpanOptions{Name: "sync-error"},
					func(span TelemetrySpan) error { return syncError }); !errors.Is(err, syncError) {
					return fmt.Errorf("sync error = %v", err)
				}
				asyncError := errors.New("async")
				if err := fixture.Context().StartSpan(SpanOptions{Name: "async-error"},
					func(span TelemetrySpan) error { return asyncError }); !errors.Is(err, asyncError) {
					return fmt.Errorf("async error = %v", err)
				}
				// An error whose inspection panics must still settle the span
				// without breaking the caller.
				if err := fixture.Context().StartSpan(SpanOptions{Name: "unreadable-error"},
					func(span TelemetrySpan) error { return panickingError{} }); err == nil {
					return errors.New("panicking error must propagate")
				}
				spans := fixture.Spans()
				for _, name := range []string{"sync-error", "async-error", "unreadable-error"} {
					span, err := findSpan(spans, name)
					if err != nil {
						return err
					}
					if span.Status.Status != StatusError {
						return fmt.Errorf("%s status = %+v", name, span.Status)
					}
				}
				return nil
			}),

		conformanceCase(factory, "status", "uses last explicit status without automatic overwrite",
			func(fixture AdapterFixture) error {
				if err := fixture.Context().StartSpan(SpanOptions{Name: "last-status"}, func(span TelemetrySpan) error {
					span.SetStatus(ErrorStatus("Expected", "first"))
					span.SetStatus(OKStatus())
					return nil
				}); err != nil {
					return err
				}
				thrown := errors.New("after explicit status")
				if err := fixture.Context().StartSpan(SpanOptions{Name: "explicit-before-throw"},
					func(span TelemetrySpan) error {
						span.SetStatus(OKStatus())
						return thrown
					}); !errors.Is(err, thrown) {
					return fmt.Errorf("err = %v", err)
				}
				rejected := errors.New("after async explicit status")
				if err := fixture.Context().StartSpan(SpanOptions{Name: "explicit-before-rejection"},
					func(span TelemetrySpan) error {
						span.SetStatus(ErrorStatus("Expected", "async failure"))
						return rejected
					}); !errors.Is(err, rejected) {
					return fmt.Errorf("err = %v", err)
				}
				if err := fixture.Context().StartSpan(SpanOptions{Name: "expected-failure"}, func(span TelemetrySpan) error {
					span.SetStatus(ErrorStatus("Expected", "returned failure"))
					return nil
				}); err != nil {
					return err
				}

				spans := fixture.Spans()
				expectations := map[string]SpanStatus{
					"last-status":               OKStatus(),
					"explicit-before-throw":     OKStatus(),
					"explicit-before-rejection": ErrorStatus("Expected", "async failure"),
					"expected-failure":          ErrorStatus("Expected", "returned failure"),
				}
				for name, want := range expectations {
					span, err := findSpan(spans, name)
					if err != nil {
						return err
					}
					if !statusEqual(span.Status, want) {
						return fmt.Errorf("%s status = %+v; want %+v", name, span.Status, want)
					}
				}
				return nil
			}),

		conformanceCase(factory, "recording", "merges attributes and records ordered events",
			func(fixture AdapterFixture) error {
				if err := fixture.Context().StartSpan(SpanOptions{
					Name:       "recording",
					Attributes: SpanAttributes{"start": "value", "overwrite": "start", "ignored": nil},
				}, func(span TelemetrySpan) error {
					span.SetAttributes(SpanAttributes{"count": 1, "overwrite": "middle"})
					span.SetAttributes(SpanAttributes{"count": nil, "overwrite": "end"})
					span.AddEvent("first", SpanAttributes{"index": 1, "ignored": nil})
					span.AddEvent("second", SpanAttributes{"index": 2})
					return nil
				}); err != nil {
					return err
				}
				span, err := findSpan(fixture.Spans(), "recording")
				if err != nil {
					return err
				}
				if len(span.Attributes) != 3 || span.Attributes["start"] != "value" ||
					span.Attributes["overwrite"] != "end" || span.Attributes["count"] != 1 {
					return fmt.Errorf("attributes = %v", span.Attributes)
				}
				if len(span.Events) != 2 || span.Events[0].Name != "first" || span.Events[1].Name != "second" {
					return fmt.Errorf("events = %+v", span.Events)
				}
				if span.Events[0].Attributes["index"] != 1 || len(span.Events[0].Attributes) != 1 {
					return fmt.Errorf("event attributes = %v", span.Events[0].Attributes)
				}
				return nil
			}),

		conformanceCase(factory, "recording", "ignores failed attribute calls atomically",
			func(fixture AdapterFixture) error {
				if err := fixture.Context().StartSpan(SpanOptions{
					Name: "atomic-attributes", Attributes: SpanAttributes{"retained": "value"},
				}, func(span TelemetrySpan) error {
					// The unreadable value fails the merge; the partial value
					// must not survive.
					span.SetAttributes(SpanAttributes{"partial": "must not survive", "unreadable": make(chan int)})
					return nil
				}); err != nil {
					return err
				}
				span, err := findSpan(fixture.Spans(), "atomic-attributes")
				if err != nil {
					return err
				}
				if len(span.Attributes) != 1 || span.Attributes["retained"] != "value" {
					return fmt.Errorf("attributes = %v", span.Attributes)
				}
				return nil
			}),

		conformanceCase(factory, "recording", "makes calls after settlement inert",
			func(fixture AdapterFixture) error {
				var capturedSpan TelemetrySpan
				if err := fixture.Context().StartSpan(SpanOptions{
					Name: "settled", Attributes: SpanAttributes{"value": "initial"},
				}, func(span TelemetrySpan) error {
					capturedSpan = span
					return nil
				}); err != nil {
					return err
				}
				if capturedSpan == nil {
					return errors.New("Expected callback span")
				}
				capturedSpan.SetAttributes(SpanAttributes{"value": "late"})
				capturedSpan.AddEvent("late", SpanAttributes{"value": true})
				capturedSpan.SetStatus(SpanStatus{Status: StatusError})

				// A child of a settled span runs (in a noop context) but is not
				// recorded.
				childAdmitted := false
				if err := capturedSpan.StartSpan(SpanOptions{Name: "late-child"},
					func(span TelemetrySpan) error { childAdmitted = true; return nil }); err != nil {
					return err
				}
				if !childAdmitted {
					return errors.New("child callback must run")
				}

				spans := fixture.Spans()
				if len(spans) != 1 {
					return fmt.Errorf("spans = %d; want 1", len(spans))
				}
				if spans[0].Attributes["value"] != "initial" || len(spans[0].Events) != 0 ||
					spans[0].Status.Status != StatusOK {
					return fmt.Errorf("span = %+v", spans[0])
				}
				return nil
			}),

		conformanceCase(factory, "parentage", "records nested and concurrent child relationships",
			func(fixture AdapterFixture) error {
				type childResult struct {
					err error
				}
				if err := fixture.Context().StartSpan(SpanOptions{Name: "parent"}, func(parent TelemetrySpan) error {
					results := make(chan childResult, 2)
					go func() {
						results <- childResult{parent.StartSpan(SpanOptions{Name: "first-child"},
							func(span TelemetrySpan) error { return nil })}
					}()
					go func() {
						results <- childResult{parent.StartSpan(SpanOptions{Name: "second-child"},
							func(span TelemetrySpan) error { return nil })}
					}()
					for i := 0; i < 2; i++ {
						if result := <-results; result.err != nil {
							return result.err
						}
					}
					return nil
				}); err != nil {
					return err
				}
				spans := fixture.Spans()
				parent, err := findSpan(spans, "parent")
				if err != nil {
					return err
				}
				first, err := findSpan(spans, "first-child")
				if err != nil {
					return err
				}
				second, err := findSpan(spans, "second-child")
				if err != nil {
					return err
				}
				if parent.ParentID != nil {
					return fmt.Errorf("parent id = %v; want nil", parent.ParentID)
				}
				if first.ParentID == nil || *first.ParentID != parent.ID ||
					second.ParentID == nil || *second.ParentID != parent.ID {
					return fmt.Errorf("child parents = %v / %v; want %d", first.ParentID, second.ParentID, parent.ID)
				}
				// Children settle before their parent.
				if first.EndSequence == nil || second.EndSequence == nil || parent.EndSequence == nil {
					return errors.New("end sequences must be assigned")
				}
				if *first.EndSequence >= *parent.EndSequence || *second.EndSequence >= *parent.EndSequence {
					return fmt.Errorf("child end sequences %d/%d must precede parent %d",
						*first.EndSequence, *second.EndSequence, *parent.EndSequence)
				}
				return nil
			}),

		conformanceCase(factory, "passivity", "suppresses unreadable telemetry payload failures",
			func(fixture AdapterFixture) error {
				calls := 0
				// Unreadable start attributes: the work runs, nothing is recorded.
				result, err := StartSpanValue(fixture.Context(),
					SpanOptions{Name: "unreadable-options", Attributes: unreadableAttributes},
					func(span TelemetrySpan) (int, error) { calls++; return 9, nil })
				if err != nil {
					return err
				}
				if calls != 1 || result != 9 {
					return fmt.Errorf("calls=%d result=%d", calls, result)
				}
				if len(fixture.Spans()) != 0 {
					return fmt.Errorf("spans = %d; want 0", len(fixture.Spans()))
				}

				// Unreadable recording payloads are swallowed.
				if err := fixture.Context().StartSpan(SpanOptions{Name: "unreadable-recording"},
					func(span TelemetrySpan) error {
						span.SetAttributes(unreadableAttributes)
						span.AddEvent("unreadable-event", unreadableAttributes)
						span.SetStatus(SpanStatus{Status: "bogus"})
						return nil
					}); err != nil {
					return err
				}
				recorded := fixture.Spans()
				if len(recorded) != 1 {
					return fmt.Errorf("spans = %d; want 1", len(recorded))
				}
				if len(recorded[0].Attributes) != 0 || len(recorded[0].Events) != 0 ||
					recorded[0].Status.Status != StatusOK {
					return fmt.Errorf("span = %+v", recorded[0])
				}
				return nil
			}),

		conformanceCase(factory, "passivity", "ignores failed status calls atomically",
			func(fixture AdapterFixture) error {
				rejection := errors.New("rejected after unreadable status")
				err := fixture.Context().StartSpan(SpanOptions{Name: "unreadable-status"},
					func(span TelemetrySpan) error {
						span.SetStatus(SpanStatus{Status: "bogus"})
						return rejection
					})
				if !errors.Is(err, rejection) {
					return fmt.Errorf("err = %v", err)
				}
				span, findErr := findSpan(fixture.Spans(), "unreadable-status")
				if findErr != nil {
					return findErr
				}
				if span.Status.Status != StatusError {
					return fmt.Errorf("status = %+v; want automatic error", span.Status)
				}
				return nil
			}),
	}
}

func statusEqual(left, right SpanStatus) bool {
	if left.Status != right.Status {
		return false
	}
	if (left.Error == nil) != (right.Error == nil) {
		return false
	}
	if left.Error != nil && right.Error != nil {
		return left.Error.Name == right.Error.Name && left.Error.Message == right.Error.Message
	}
	return true
}

// RunAdapterConformance runs every conformance case against a factory,
// reporting failures through the supplied reporter (a *testing.T in tests).
// A reporter is any value with an Errorf method, so this package stays free of
// a testing dependency.
func RunAdapterConformance(reporter interface {
	Errorf(format string, args ...any)
}, factory AdapterFixtureFactory) {
	for _, testCase := range AdapterConformance(factory) {
		fixture, err := factory()
		if err != nil {
			reporter.Errorf("%s: %s: fixture: %v", testCase.Group, testCase.Name, err)
			continue
		}
		if err := testCase.Run(fixture); err != nil {
			reporter.Errorf("%s: %s: %v", testCase.Group, testCase.Name, err)
		}
	}
}

var _ = strings.TrimSpace
