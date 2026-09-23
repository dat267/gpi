package coding

import (
	"encoding/json"
	"sync"

	"github.com/dat267/pier/ai"
)

// messageCache memoizes what a session entry projects to: its LLM messages and,
// for message entries, the JSON role a compaction window matches "system"
// against.
//
// Session entries are append-only. An entry's Message bytes are written when it
// is appended or loaded and never rewritten afterwards (the only rewrite, the
// v2 to v3 role migration, runs during load, before any projection), so the
// cache needs no invalidation: a projection after an append decodes only the
// appended entries instead of the whole session. Before this, every append
// invalidated the projection and the next resolution re-decoded every message
// in the window, which is what made any new reader of the session a latency
// hazard.
//
// The memoized messages are shared across projections, matching upstream, which
// holds the message objects in agent state. Callers must treat them as
// read-only; the immutable entry they came from is never rewritten, so nothing
// can invalidate them.
type messageCache struct {
	mu sync.Mutex

	byID map[string]*cachedEntry
	// decode is the entry-to-messages step, injectable for tests.
	decode func(*SessionEntry) []ai.Message
}

// cachedEntry is one entry's memoized projection facts. The halves are filled
// independently: only the entries a compaction window might drop need a role.
type cachedEntry struct {
	messages     []ai.Message
	haveMessages bool
	role         string
	haveRole     bool
}

// messages returns the entry's projected LLM messages, decoding them once.
// Only message entries are memoized: the other entry types project to a
// synthetic message whose timestamp the port stamps at projection time
// (CreateCompactionSummaryMessage and friends; upstream passes the entry's own
// timestamp), so caching one would freeze the first resolution's stamp.
func (c *messageCache) messages(entry *SessionEntry) []ai.Message {
	if entry.ID == "" || entry.Type != "message" {
		return c.decodeEntry(entry)
	}
	c.mu.Lock()
	if cached := c.byID[entry.ID]; cached != nil && cached.haveMessages {
		messages := cached.messages
		c.mu.Unlock()
		return messages
	}
	c.mu.Unlock()

	decoded := c.decodeEntry(entry)
	c.mu.Lock()
	cached := c.lookupLocked(entry.ID)
	if !cached.haveMessages {
		cached.messages, cached.haveMessages = decoded, true
	}
	messages := cached.messages
	c.mu.Unlock()
	return messages
}

// role returns the entry's JSON role ("" for non-message entries), decoding it
// once. It is what applyCompactionWindow matches "system" against.
func (c *messageCache) role(entry *SessionEntry) string {
	if entry.Type != "message" || len(entry.Message) == 0 {
		return ""
	}
	if entry.ID == "" {
		return decodeEntryRole(entry)
	}
	c.mu.Lock()
	if cached := c.byID[entry.ID]; cached != nil && cached.haveRole {
		role := cached.role
		c.mu.Unlock()
		return role
	}
	c.mu.Unlock()

	decoded := decodeEntryRole(entry)
	c.mu.Lock()
	cached := c.lookupLocked(entry.ID)
	if !cached.haveRole {
		cached.role, cached.haveRole = decoded, true
	}
	role := cached.role
	c.mu.Unlock()
	return role
}

// reset drops every memoized entry (a manager starting a new session).
func (c *messageCache) reset() {
	c.mu.Lock()
	c.byID = nil
	c.mu.Unlock()
}

// lookupLocked returns the entry's cache slot, creating the map on first use.
func (c *messageCache) lookupLocked(id string) *cachedEntry {
	if c.byID == nil {
		c.byID = map[string]*cachedEntry{}
	}
	cached := c.byID[id]
	if cached == nil {
		cached = &cachedEntry{}
		c.byID[id] = cached
	}
	return cached
}

// decodeEntry projects the entry's messages. Decoding happens outside the cache
// lock: a large session's first projection decodes thousands of entries, and
// holding the cache lock for it would park every other decoder. A duplicate
// decode of the same entry is harmless; the first stored result wins.
func (c *messageCache) decodeEntry(entry *SessionEntry) []ai.Message {
	decode := c.decode
	if decode == nil {
		decode = SessionEntryToContextMessages
	}
	return decode(entry)
}

// decodeEntryRole reads only the role field, which is cheaper than decoding the
// whole message.
func decodeEntryRole(entry *SessionEntry) string {
	var header struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(entry.Message, &header) != nil {
		return ""
	}
	return header.Role
}

// projectedMessages projects an entry's LLM messages, reading through the
// session's cache when there is one. A nil cache is the reference path used by
// BuildSessionContext and BuildContextEntries.
func projectedMessages(entry *SessionEntry, cache *messageCache) []ai.Message {
	if cache != nil {
		return cache.messages(entry)
	}
	return SessionEntryToContextMessages(entry)
}

// projectedRole reads the role a compaction window matches "system" against.
func projectedRole(entry *SessionEntry, cache *messageCache) string {
	if entry.Type != "message" || len(entry.Message) == 0 {
		return ""
	}
	if cache != nil {
		return cache.role(entry)
	}
	return decodeEntryRole(entry)
}
