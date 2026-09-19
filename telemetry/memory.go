package telemetry

import (
	"fmt"
	"sync"
)

// Port of src/memory.ts: the backend-neutral reference recorder.

// RecordedTelemetryEvent is one recorded event.
type RecordedTelemetryEvent struct {
	Name       string
	Attributes SpanAttributes
}

// RecordedTelemetrySpan is one recorded span snapshot.
type RecordedTelemetrySpan struct {
	ID          int
	ParentID    *int
	Name        string
	Attributes  SpanAttributes
	Events      []RecordedTelemetryEvent
	Status      SpanStatus
	Settled     bool
	EndSequence *int
}

type mutableSpan struct {
	id             int
	parentID       *int
	name           string
	attributes     SpanAttributes
	events         []RecordedTelemetryEvent
	status         SpanStatus
	explicitStatus bool
	settled        bool
	endSequence    *int
}

type inMemoryState struct {
	mu              sync.Mutex
	spans           []*mutableSpan
	nextSpanID      int
	nextEndSequence int
}

// InMemoryTelemetryContext records spans in process memory. Create a fresh
// instance to isolate tests or independent recording scopes.
type InMemoryTelemetryContext struct {
	state *inMemoryState
}

// NewInMemoryTelemetryContext builds an isolated recorder.
func NewInMemoryTelemetryContext() *InMemoryTelemetryContext {
	return &InMemoryTelemetryContext{state: &inMemoryState{nextSpanID: 1, nextEndSequence: 1}}
}

// StartSpan admits and runs one span.
func (c *InMemoryTelemetryContext) StartSpan(options SpanOptions, fn func(span TelemetrySpan) error) error {
	return startInMemorySpan(c.state, nil, options, fn)
}

// GetSpans returns detached snapshots in span-start order.
func (c *InMemoryTelemetryContext) GetSpans() []RecordedTelemetrySpan {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	out := make([]RecordedTelemetrySpan, 0, len(c.state.spans))
	for _, span := range c.state.spans {
		snapshot := RecordedTelemetrySpan{
			ID: span.id, ParentID: span.parentID, Name: span.name,
			Attributes: copyAttributesOrEmpty(span.attributes),
			Status:     copyStatus(span.status),
			Settled:    span.settled,
		}
		snapshot.Events = make([]RecordedTelemetryEvent, 0, len(span.events))
		for _, event := range span.events {
			snapshot.Events = append(snapshot.Events, RecordedTelemetryEvent{
				Name: event.Name, Attributes: copyAttributesOrEmpty(event.Attributes),
			})
		}
		if span.endSequence != nil {
			value := *span.endSequence
			snapshot.EndSequence = &value
		}
		out = append(out, snapshot)
	}
	return out
}

// copyAttributeValue copies array values (upstream copyAttributeValue).
func copyAttributeValue(value AttributeValue) AttributeValue {
	switch typed := value.(type) {
	case []string:
		return append([]string{}, typed...)
	case []float64:
		return append([]float64{}, typed...)
	case []bool:
		return append([]bool{}, typed...)
	case []int:
		return append([]int{}, typed...)
	case []int64:
		return append([]int64{}, typed...)
	default:
		return value
	}
}

// copyAttributes copies attributes, skipping absent (nil) values. An
// unusable value fails the whole copy, which callers use for atomicity.
func copyAttributes(attributes SpanAttributes) (SpanAttributes, error) {
	copy := SpanAttributes{}
	for name, value := range attributes {
		if value == nil {
			continue
		}
		if err := validateAttributeValue(value); err != nil {
			return nil, err
		}
		copy[name] = copyAttributeValue(value)
	}
	return copy, nil
}

func copyAttributesOrEmpty(attributes SpanAttributes) SpanAttributes {
	copied, err := copyAttributes(attributes)
	if err != nil {
		return SpanAttributes{}
	}
	return copied
}

func mergeAttributes(current SpanAttributes, attributes SpanAttributes) (SpanAttributes, error) {
	merged, err := copyAttributes(current)
	if err != nil {
		return nil, err
	}
	additions, err := copyAttributes(attributes)
	if err != nil {
		return nil, err
	}
	for name, value := range additions {
		merged[name] = value
	}
	return merged, nil
}

func copyStatus(status SpanStatus) SpanStatus {
	if status.Status == StatusError && status.Error != nil {
		return SpanStatus{Status: StatusError, Error: &SpanError{Name: status.Error.Name, Message: status.Error.Message}}
	}
	return SpanStatus{Status: status.Status}
}

// validateStatus rejects unknown status values (the Go analogue of upstream's
// unreadable status payload that makes the set call fail atomically).
func validateStatus(status SpanStatus) error {
	if status.Status != StatusOK && status.Status != StatusError {
		return fmt.Errorf("telemetry: unknown span status %q", status.Status)
	}
	if status.Status == StatusError && status.Error != nil {
		if status.Error.Name == "" && status.Error.Message == "" {
			return fmt.Errorf("telemetry: empty error status")
		}
	}
	return nil
}

// automaticErrorStatus derives an error status from a failure, tolerating
// errors that cannot be inspected (the Go analogue of upstream's unreadable
// error payloads).
func automaticErrorStatus(err error) (status SpanStatus) {
	status = SpanStatus{Status: StatusError}
	if err == nil {
		return status
	}
	defer func() {
		if recover() != nil {
			status = SpanStatus{Status: StatusError}
		}
	}()
	status = SpanStatus{Status: StatusError, Error: &SpanError{Name: errorName(err), Message: err.Error()}}
	return status
}

func errorName(err error) string {
	if named, ok := err.(interface{ ErrorName() string }); ok {
		return named.ErrorName()
	}
	return fmt.Sprintf("%T", err)
}

// settleSpan marks a span settled (idempotent) and assigns the end sequence.
func settleSpan(state *inMemoryState, span *mutableSpan, failed bool, err error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if span.settled {
		return
	}
	if failed && !span.explicitStatus {
		span.status = automaticErrorStatus(err)
	}
	span.settled = true
	sequence := state.nextEndSequence
	state.nextEndSequence++
	span.endSequence = &sequence
}

func createSpan(state *inMemoryState, parent *mutableSpan, options SpanOptions) (*mutableSpan, error) {
	attributes, err := copyAttributes(options.Attributes)
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	span := &mutableSpan{
		id: state.nextSpanID, name: options.Name, attributes: attributes,
		status: OKStatus(),
	}
	state.nextSpanID++
	if parent != nil {
		parentID := parent.id
		span.parentID = &parentID
	}
	state.spans = append(state.spans, span)
	return span, nil
}

func startInMemorySpan(state *inMemoryState, parent *mutableSpan, options SpanOptions, fn func(span TelemetrySpan) error) error {
	state.mu.Lock()
	parentSettled := parent != nil && parent.settled
	state.mu.Unlock()
	if parentSettled {
		// A settled parent cannot admit children (upstream falls back to noop).
		return NoopTelemetryContext.StartSpan(options, fn)
	}

	recordedSpan, err := createSpan(state, parent, options)
	if err != nil {
		// Recording is passive: an unreadable payload must not break the work.
		return NoopTelemetryContext.StartSpan(options, fn)
	}

	span := &inMemorySpan{state: state, recorded: recordedSpan}
	if fnErr := runCallback(fn, span); fnErr != nil {
		settleSpan(state, recordedSpan, true, fnErr)
		return fnErr
	}
	settleSpan(state, recordedSpan, false, nil)
	return nil
}

// runCallback invokes fn, converting a panic into an error so the span still
// settles as a failure.
func runCallback(fn func(span TelemetrySpan) error, span TelemetrySpan) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if asError, ok := recovered.(error); ok {
				err = asError
			} else {
				err = fmt.Errorf("%v", recovered)
			}
		}
	}()
	return fn(span)
}

type inMemorySpan struct {
	state    *inMemoryState
	recorded *mutableSpan
}

func (s *inMemorySpan) StartSpan(options SpanOptions, fn func(span TelemetrySpan) error) error {
	return startInMemorySpan(s.state, s.recorded, options, fn)
}

func (s *inMemorySpan) AddEvent(name string, attributes SpanAttributes) {
	s.state.mu.Lock()
	if s.recorded.settled {
		s.state.mu.Unlock()
		return
	}
	s.state.mu.Unlock()
	copied, err := copyAttributes(attributes)
	if err != nil {
		return // recording is passive
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.recorded.settled {
		return
	}
	s.recorded.events = append(s.recorded.events, RecordedTelemetryEvent{Name: name, Attributes: copied})
}

func (s *inMemorySpan) SetAttributes(attributes SpanAttributes) {
	s.state.mu.Lock()
	if s.recorded.settled {
		s.state.mu.Unlock()
		return
	}
	s.state.mu.Unlock()
	merged, err := mergeAttributes(s.recorded.attributes, attributes)
	if err != nil {
		return // atomic: a failed merge leaves the span untouched
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.recorded.settled {
		return
	}
	s.recorded.attributes = merged
}

func (s *inMemorySpan) SetStatus(status SpanStatus) {
	if err := validateStatus(status); err != nil {
		return // atomic: a failed status call leaves the span untouched
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.recorded.settled {
		return
	}
	s.recorded.status = copyStatus(status)
	s.recorded.explicitStatus = true
}
