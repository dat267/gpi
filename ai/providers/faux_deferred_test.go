package providers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dat267/gpi/ai"
)

// Behavior tests for the faux deferred-response protocol (upstream
// providers/faux.ts). Upstream tests this surface through the Models
// registry (test/providers.test.ts), which is not yet ported; these assert
// the core's contract directly.

func fauxDeferredOpts() *ai.SimpleStreamOptions {
	return &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{Deferred: &ai.DeferredRequest{}}}
}

func TestFauxDeferredReturnsHandleThenScriptedResponse(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("final text", FauxMessageOptions{})}})
	model := core.GetModel("")

	// Submission returns a deferred message with a handle.
	submission := fauxComplete(t, core, model, ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}, fauxDeferredOpts())
	if submission.StopReason != ai.StopDeferred {
		t.Fatalf("stopReason = %s; want deferred", submission.StopReason)
	}
	if submission.Deferred == nil || submission.Deferred.ID == "" {
		t.Fatalf("deferred handle missing: %+v", submission.Deferred)
	}
	if len(submission.Content) != 0 {
		t.Fatalf("deferred submission should carry no content: %v", submission.Content)
	}
	handle := submission.Deferred
	if handle.Provider != model.Provider || handle.ModelID != model.ID || handle.API != model.API {
		t.Fatalf("handle attribution = %+v", handle)
	}

	// Fetch resolves the scripted response.
	fetched := core.FetchDeferred(model, handle, nil)
	result, err := fetched.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != ai.StopStop || result.Content[0].(ai.TextContent).Text != "final text" {
		t.Fatalf("fetched = %s / %v", result.StopReason, result.Content)
	}
	if core.State().DeferredFetchCount != 1 {
		t.Fatalf("deferredFetchCount = %d", core.State().DeferredFetchCount)
	}
}

func TestFauxDeferredPendingFetchesReturnHandle(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("final", FauxMessageOptions{})}})
	model := core.GetModel("")

	// Core with pendingFetches=1: first fetch returns the handle again.
	deferredCore := NewFauxCore(func() FauxOptions {
		o := FauxOptions{}
		o.Deferred.PendingFetches = 1
		return o
	}())
	deferredCore.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("final", FauxMessageOptions{})}})

	submission := fauxComplete(t, deferredCore, model, ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}, fauxDeferredOpts())
	handle := submission.Deferred

	pending := deferredCore.FetchDeferred(model, handle, nil)
	pendingResult, err := pending.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pendingResult.StopReason != ai.StopDeferred || pendingResult.Deferred == nil || pendingResult.Deferred.ID != handle.ID {
		t.Fatalf("pending fetch = %+v", pendingResult)
	}

	final := deferredCore.FetchDeferred(model, handle, nil)
	finalResult, err := final.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if finalResult.StopReason != ai.StopStop {
		t.Fatalf("final stopReason = %s", finalResult.StopReason)
	}
}

func TestFauxDeferredUnknownAndCancelledHandles(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("final", FauxMessageOptions{})}})
	model := core.GetModel("")

	unknown := core.FetchDeferred(model, &ai.DeferredHandle{Provider: model.Provider, ModelID: model.ID, API: model.API, ID: "nope"}, nil)
	result, err := unknown.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != ai.StopError || *result.ErrorMessage != "Unknown faux deferred response: nope" {
		t.Fatalf("unknown = %s / %v", result.StopReason, result.ErrorMessage)
	}

	submission := fauxComplete(t, core, model, ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}, fauxDeferredOpts())
	handle := submission.Deferred
	if err := core.CancelDeferred(model, handle, nil); err != nil {
		t.Fatal(err)
	}
	if len(core.State().CancelledDeferred) != 1 || core.State().CancelledDeferred[0].ID != handle.ID {
		t.Fatalf("cancelled = %+v", core.State().CancelledDeferred)
	}
	cancelled := core.FetchDeferred(model, handle, nil)
	cancelledResult, err := cancelled.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cancelledResult.StopReason != ai.StopError || *cancelledResult.ErrorMessage != "Faux deferred response was cancelled: "+handle.ID {
		t.Fatalf("cancelled fetch = %s / %v", cancelledResult.StopReason, cancelledResult.ErrorMessage)
	}
}

func TestFauxDeferredSubmissionCarriesPollAfterMs(t *testing.T) {
	core := NewFauxCore(func() FauxOptions {
		o := FauxOptions{}
		o.Deferred.PollAfterMs = 1500
		o.Deferred.HasPollAfterMs = true
		return o
	}())
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("final", FauxMessageOptions{})}})
	model := core.GetModel("")
	submission := fauxComplete(t, core, model, ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}, fauxDeferredOpts())
	if submission.Deferred == nil || submission.Deferred.PollAfterMs == nil || *submission.Deferred.PollAfterMs != 1500 {
		t.Fatalf("pollAfterMs = %+v", submission.Deferred)
	}
	// JSON key parity for the handle wire shape.
	enc, err := ai.MarshalJSON(submission.Deferred)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(enc, &probe); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"provider", "modelId", "api", "id", "pollAfterMs"} {
		if _, ok := probe[key]; !ok {
			t.Fatalf("handle JSON missing %q: %s", key, enc)
		}
	}
}
