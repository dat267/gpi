package coding

import (
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
