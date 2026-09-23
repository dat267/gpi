package coding

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
)

// TestContextSignatureMatchesTheProjectedContext pins the cheap signature to
// the real projection across session shapes: the count must equal the projected
// message count and the model ref must equal the projected one.
func TestContextSignatureMatchesTheProjectedContext(t *testing.T) {
	shapes := map[string]func(m *SessionManager){
		"plain messages": func(m *SessionManager) {
			m.AppendMessage(createUserMessage("one"))
			m.AppendMessage(createAssistantMessageT("two"))
		},
		"model change": func(m *SessionManager) {
			m.AppendMessage(createUserMessage("one"))
			m.AppendModelChange("anthropic", "claude-opus-4-5")
			m.AppendMessage(createAssistantMessageT("two"))
		},
		"assistant model wins later": func(m *SessionManager) {
			m.AppendModelChange("anthropic", "older-model")
			m.AppendMessage(createUserMessage("one"))
			assistant := createAssistantMessageT("two")
			assistant.Provider, assistant.Model = "openai", "gpt-5"
			m.AppendMessage(assistant)
		},
		"compaction window": func(m *SessionManager) {
			m.AppendMessage(createUserMessage("old-1"))
			m.AppendMessage(createAssistantMessageT("old-reply"))
			m.AppendMessage(createUserMessage("kept"))
			entries := m.GetEntries()
			m.AppendCompaction("summary of old", entries[2].ID, 1000, nil, false, nil)
			m.AppendMessage(createUserMessage("after compaction"))
			m.AppendMessage(createAssistantMessageT("reply after compaction"))
		},
		"branch summary": func(m *SessionManager) {
			m.AppendMessage(createUserMessage("one"))
			m.BranchWithSummary("", "a branch", nil, false, nil)
			m.AppendMessage(createAssistantMessageT("two"))
		},
		"custom message": func(m *SessionManager) {
			m.AppendMessage(createUserMessage("one"))
			m.AppendCustomMessageEntry("note", "hello", true, nil)
			m.AppendMessage(createAssistantMessageT("two"))
		},
		"thinking level only": func(m *SessionManager) {
			m.AppendThinkingLevelChange("high")
			m.AppendMessage(createUserMessage("one"))
		},
	}
	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			m, _ := newTestSession(t)
			shape(m)
			projected := m.Projection()
			signature := m.ContextSignature()
			if signature.MessageCount != len(projected.Messages) {
				t.Fatalf("signature count = %d; projected = %d", signature.MessageCount, len(projected.Messages))
			}
			projectedProvider, projectedModel := "", ""
			if projected.Model != nil {
				projectedProvider, projectedModel = projected.Model.Provider, projected.Model.ModelID
			}
			if signature.Provider != projectedProvider || signature.ModelID != projectedModel {
				t.Fatalf("signature model = %s/%s; projected = %s/%s",
					signature.Provider, signature.ModelID, projectedProvider, projectedModel)
			}
		})
	}
}

// TestCacheContextIsCurrentDoesNotProjectTheSession guards the request path: the
// currency check runs on every request (and while the warmer is active), so it
// must not rebuild the session context. Projecting a large session allocated
// megabytes per call.
func TestCacheContextIsCurrentDoesNotProjectTheSession(t *testing.T) {
	persist := false
	m := NewSessionManager(t.TempDir(), &SessionManagerOptions{SessionDir: t.TempDir(), Persist: &persist})
	for i := 0; i < 3000; i++ {
		m.AppendMessage(createUserMessage("a message with enough text to make the projection allocate"))
		assistant := createAssistantMessageT("a reply")
		assistant.Provider, assistant.Model = "anthropic", "claude-opus-4-5"
		m.AppendMessage(assistant)
	}
	model := &ai.Model{Provider: "anthropic", ID: "claude-opus-4-5"}
	isCurrent := cacheContextIsCurrent(m, model)
	if !isCurrent() {
		t.Fatal("an unchanged session must be current")
	}

	// Larger sessions must not matter: the call is a branch-cache lookup plus a
	// scan, not a projection.
	before := runtime.MemStats{}
	runtime.ReadMemStats(&before)
	const runs = 50
	for i := 0; i < runs; i++ {
		if !isCurrent() {
			t.Fatal("an unchanged session must stay current")
		}
	}
	after := runtime.MemStats{}
	runtime.ReadMemStats(&after)
	perCall := int64(after.TotalAlloc-before.TotalAlloc) / runs
	if perCall > 64*1024 {
		t.Fatalf("cache currency check allocated %d bytes per call; want no projection", perCall)
	}
}

// TestProjectionDoesNotHoldTheSessionLock keeps the UI responsive: the footer
// reads the session every frame, so resolving a projection must not park other
// readers behind the session mutex.
func TestProjectionDoesNotHoldTheSessionLock(t *testing.T) {
	persist := false
	m := NewSessionManager(t.TempDir(), &SessionManagerOptions{SessionDir: t.TempDir(), Persist: &persist})
	for i := 0; i < 60000; i++ {
		m.AppendMessage(createUserMessage("a message with enough text that the projection takes a while"))
	}

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		_ = m.Projection()
	}()
	var projection, longestHold, holdStart time.Duration
	for {
		select {
		case <-done:
			projection = time.Since(start)
		default:
		}
		if m.mu.TryLock() {
			m.mu.Unlock()
			if holdStart > 0 {
				if long := time.Since(start) - holdStart; long > longestHold {
					longestHold = long
				}
				holdStart = 0
			}
		} else if holdStart == 0 {
			holdStart = time.Since(start)
		}
		if projection != 0 {
			break
		}
		time.Sleep(200 * time.Microsecond)
	}
	if projection < 50*time.Millisecond {
		t.Skipf("projection too fast to observe the lock (%v)", projection)
	}
	// Snapshotting the entries holds the lock briefly; projecting them must not.
	if longestHold > projection/3 {
		t.Fatalf("the session lock was held for %v of a %v projection", longestHold, projection)
	}
}

// TestProjectedSettingsResolveLastWriteWins pins getSessionContextSettings
// after its backward scan: the model and thinking level are whatever the last
// setting entry in the path says, with an assistant message counting as a model
// setter (upstream's forward scan let later entries win).
func TestProjectedSettingsResolveLastWriteWins(t *testing.T) {
	m, _ := newTestSession(t)
	m.AppendModelChange("anthropic", "claude-opus-4-5")
	m.AppendThinkingLevelChange("low")
	assistant := createAssistantMessageT("reply")
	assistant.Provider, assistant.Model = "openai", "gpt-5"
	m.AppendMessage(assistant)
	m.AppendMessage(createUserMessage("again"))
	m.AppendModelChange("google", "gemini-3-pro")
	m.AppendThinkingLevelChange("high")

	context := m.Projection()
	if context.Model == nil || context.Model.Provider != "google" || context.Model.ModelID != "gemini-3-pro" {
		t.Fatalf("model = %+v", context.Model)
	}
	if context.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q", context.ThinkingLevel)
	}

	// With no model_change after it, the assistant's own model wins.
	m2, _ := newTestSession(t)
	m2.AppendModelChange("anthropic", "older")
	second := createAssistantMessageT("reply")
	second.Provider, second.Model = "openai", "gpt-5"
	m2.AppendMessage(second)
	if context := m2.Projection(); context.Model == nil ||
		context.Model.Provider != "openai" || context.Model.ModelID != "gpt-5" {
		t.Fatalf("model = %+v", context.Model)
	}

	// A session with no settings at all keeps upstream's default.
	m3, _ := newTestSession(t)
	if context := m3.Projection(); context.ThinkingLevel != "off" || context.Model != nil {
		t.Fatalf("context = %+v (model %+v)", context.ThinkingLevel, context.Model)
	}
}

// TestAppendCompactionCarriesTheCurrentSystemMessage pins the entry that
// AppendCompaction writes: the compaction records the projected system message,
// which is now resolved from a projection taken before the session lock rather
// than while holding it.
func TestAppendCompactionCarriesTheCurrentSystemMessage(t *testing.T) {
	m, _ := newTestSession(t)
	m.AppendMessage(&ai.SystemMessage{
		Content:   ai.StringOrBlocks{Text: "You are a test assistant."},
		Timestamp: 1,
	})
	m.AppendMessage(createUserMessage("hi"))
	m.AppendMessage(createAssistantMessageT("hello"))
	entries := m.GetEntries()

	id := m.AppendCompaction("summary of the session", entries[len(entries)-1].ID, 1000, nil, false, nil)

	var compaction *SessionEntry
	for index := range m.GetEntries() {
		if m.GetEntries()[index].ID == id {
			compaction = &m.GetEntries()[index]
		}
	}
	if compaction == nil || compaction.Type != "compaction" {
		t.Fatalf("compaction entry not found: %+v", compaction)
	}
	if !strings.Contains(string(compaction.SystemMessageJSON), "You are a test assistant.") {
		t.Fatalf("system message = %s", compaction.SystemMessageJSON)
	}
	// The projection still carries the system message to the request.
	context := m.Projection()
	if len(context.Messages) == 0 {
		t.Fatal("no projected messages")
	}
}

// TestAppendCompactionDoesNotHoldTheSessionLock keeps compaction from parking
// the UI: the entry records the projected system message, and projecting a
// large session takes hundreds of milliseconds, so that work must happen
// outside the lock (the footer reads the session per frame).
func TestAppendCompactionDoesNotHoldTheSessionLock(t *testing.T) {
	persist := false
	m := NewSessionManager(t.TempDir(), &SessionManagerOptions{SessionDir: t.TempDir(), Persist: &persist})
	m.AppendMessage(&ai.SystemMessage{Content: ai.StringOrBlocks{Text: "system"}, Timestamp: 1})
	for i := 0; i < 12000; i++ {
		m.AppendMessage(createUserMessage("a message with enough text that the projection takes a while"))
	}
	entries := m.GetEntries()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		m.AppendCompaction("summary", entries[len(entries)-1].ID, 1000, nil, false, nil)
	}()
	var total, longestHold, holdStart time.Duration
	for {
		select {
		case <-done:
			total = time.Since(start)
		default:
		}
		if m.mu.TryLock() {
			m.mu.Unlock()
			if holdStart > 0 {
				if held := time.Since(start) - holdStart; held > longestHold {
					longestHold = held
				}
				holdStart = 0
			}
		} else if holdStart == 0 {
			holdStart = time.Since(start)
		}
		if total != 0 {
			break
		}
		time.Sleep(200 * time.Microsecond)
	}
	if total < 50*time.Millisecond {
		t.Skipf("projection too fast to observe the lock (%v)", total)
	}
	if longestHold > total/3 {
		t.Fatalf("the session lock was held for %v of a %v AppendCompaction", longestHold, total)
	}
}
