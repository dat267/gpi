package coding

import (
	"reflect"
	"testing"

	"github.com/dat267/pier/ai"
)

// TestMessageCacheDecodesAnEntryOnce pins the memo: an entry's messages and
// role are derived from its immutable Message bytes, so repeat projections
// reuse them instead of decoding the session again.
func TestMessageCacheDecodesAnEntryOnce(t *testing.T) {
	decodes := 0
	cache := &messageCache{decode: func(entry *SessionEntry) []ai.Message {
		decodes++
		return SessionEntryToContextMessages(entry)
	}}
	raw, err := ai.MarshalMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	entry := SessionEntry{
		Type:             "message",
		SessionEntryBase: SessionEntryBase{ID: "entry-1"},
		Message:          raw,
	}
	if messages := cache.messages(&entry); len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages := cache.messages(&entry); len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	if decodes != 1 {
		t.Fatalf("the entry was decoded %d times; want 1", decodes)
	}
	if role := cache.role(&entry); role != "user" {
		t.Fatalf("role = %q; want user", role)
	}
	if role := cache.role(&entry); role != "user" {
		t.Fatalf("role = %q; want user", role)
	}
	// The role has its own slot: reading it does not force the messages.
	if !cacheRoleIsMemoized(cache, "entry-1") {
		t.Fatal("the role was not memoized")
	}

	// An entry without an ID has nothing stable to key on and is never cached.
	anonymous := entry
	anonymous.ID = ""
	cache.messages(&anonymous)
	cache.messages(&anonymous)
	if decodes != 3 {
		t.Fatalf("an anonymous entry was cached (decodes = %d)", decodes)
	}

	// Non-message entries have no role.
	custom := SessionEntry{Type: "compaction", SessionEntryBase: SessionEntryBase{ID: "entry-2"}, Summary: "summary"}
	if role := cache.role(&custom); role != "" {
		t.Fatalf("role = %q for a compaction entry", role)
	}

	// Reset drops the memo (a manager starting a new session).
	cache.reset()
	cache.messages(&entry)
	if decodes != 4 {
		t.Fatalf("reset did not drop the memo (decodes = %d)", decodes)
	}
}

// cacheRoleIsMemoized reports whether the cache holds a role for the entry,
// which the fact that no decode happens on a second read would otherwise hide.
func cacheRoleIsMemoized(cache *messageCache, id string) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cached := cache.byID[id]
	return cached != nil && cached.haveRole
}

// The whole-session scans behind /session used to re-parse every message on the
// UI thread: on a 19k-entry session that measured 633ms for the stats, 458ms for
// the cache-waste scan and 387ms for the cost breakdown — about 1.5s of JSON
// work to open a panel, which is what froze the TUI. They now read through the
// session's message memo, so a repeat scan decodes nothing, and the numbers are
// unchanged.
func TestSessionScansReuseTheMessageCache(t *testing.T) {
	manager := NewSessionManager(t.TempDir(), &SessionManagerOptions{Persist: boolPtr(false)})
	manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
	manager.AppendMessage(&ai.AssistantMessage{
		Provider: "anthropic", Model: "sonnet", Usage: ai.Usage{Input: 100, Output: 20, CacheRead: 900},
		StopReason: ai.StopStop,
	})
	manager.AppendMessage(&ai.ToolResultMessage{
		ToolCallID: "call-1", ToolName: "bash",
		Usage: &ai.Usage{Input: 10, Output: 2},
	})
	manager.AppendMessage(&ai.AssistantMessage{
		Provider: "anthropic", Model: "sonnet", Usage: ai.Usage{Input: 50, Output: 5},
		StopReason: ai.StopStop,
	})

	decodes := 0
	manager.messages.decode = func(entry *SessionEntry) []ai.Message {
		decodes++
		return SessionEntryToContextMessages(entry)
	}

	wantBreakdown := GetUsageCostBreakdown(manager.GetEntries())
	wantWaste := ComputeCacheWaste(manager.GetEntries(), nil)
	wantMisses := CollectCacheMisses(manager.GetEntries(), nil)

	// The first pass fills the memo; later ones must not decode anything.
	first := manager.UsageCostBreakdown()
	afterFirst := decodes
	if afterFirst == 0 {
		t.Fatal("the scan did not read through the message cache")
	}
	if got := manager.UsageCostBreakdown(); !reflect.DeepEqual(got, first) {
		t.Errorf("breakdown = %+v, then %+v", first, got)
	}
	if got := manager.ComputeCacheWaste(nil); !reflect.DeepEqual(got, wantWaste) {
		t.Errorf("cache waste = %+v, want %+v", got, wantWaste)
	}
	if got := manager.CollectCacheMisses(nil); !reflect.DeepEqual(got, wantMisses) {
		t.Errorf("misses = %+v, want %+v", got, wantMisses)
	}
	if decodes != afterFirst {
		t.Errorf("a repeat scan decoded %d more entries", decodes-afterFirst)
	}

	// The cached path and the uncached reference path agree.
	if !reflect.DeepEqual(first, wantBreakdown) {
		t.Errorf("cached breakdown = %+v, want %+v", first, wantBreakdown)
	}
}
