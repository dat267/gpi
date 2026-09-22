package coding

import (
	"runtime"
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
			projected := m.BuildSessionContext()
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

// TestBuildSessionContextDoesNotHoldTheSessionLock keeps the UI responsive: the
// footer reads the session every frame, so a projection must not park other
// readers behind the session mutex.
func TestBuildSessionContextDoesNotHoldTheSessionLock(t *testing.T) {
	persist := false
	m := NewSessionManager(t.TempDir(), &SessionManagerOptions{SessionDir: t.TempDir(), Persist: &persist})
	for i := 0; i < 60000; i++ {
		m.AppendMessage(createUserMessage("a message with enough text that the projection takes a while"))
	}

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		_ = m.BuildSessionContext()
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
