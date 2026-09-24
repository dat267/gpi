package coding

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/dat267/pier/ai"
)

// Port of core/cache-stats.ts: prompt-cache waste accounting.

// CacheTTLMs is Anthropic's default 5 minute cache TTL. Idle gaps longer than
// this are worth mentioning as the likely cause of a miss.
const CacheTTLMs int64 = 5 * 60 * 1000

// noiseFloorTokens is the per-turn miss threshold below which a miss is cache
// breakpoint granularity noise.
const noiseFloorTokens int64 = 1024

// CacheMiss is a counted cache miss on a single assistant message.
type CacheMiss struct {
	// MissedTokens were in the previous turn's prompt but not read from cache.
	MissedTokens int64
	// MissedCost is the extra dollars paid vs. a full cache hit.
	MissedCost float64
	// IdleMs is the time since the previous request (which last refreshed the
	// cache).
	IdleMs int64
	// ModelChanged reports a model switch relative to the previous request.
	ModelChanged bool
}

// CacheWasteTotals is the cumulative cache waste across a session.
type CacheWasteTotals struct {
	MissedTokens int64
	MissedCost   float64
	MissCount    int
}

// ModelCachePrice is the minimal pricing lookup needed, satisfied by the model
// runtime. Cost is dollars per million tokens.
type ModelCachePrice struct {
	CacheRead float64
}

// ModelPriceSource resolves model pricing (upstream ModelPriceSource).
type ModelPriceSource interface {
	// GetCachePrice returns the cache-read price for a provider/model, if known.
	GetCachePrice(provider string, modelID string) *ModelCachePrice
}

// previousRequest is the last request seen by the scan; everything in its
// prompt should be cached.
type previousRequest struct {
	PromptTokens int64
	ModelKey     string
	Timestamp    int64
	// reportedCache is sticky: some earlier request in this scan segment
	// reported cache activity, which distinguishes a total miss on a
	// cache-read-only provider from one that never reports caching.
	reportedCache bool
}

// DetectCacheMiss computes the cache miss for one assistant message relative to
// the previous request; ok is false when nothing is counted. entries must not
// yet contain message (message_end fires before persistence).
func DetectCacheMiss(entries []SessionEntry, message *ai.AssistantMessage, models ModelPriceSource) (CacheMiss, bool) {
	scanState := scanCacheEntries(entries, models, nil)
	return detectCacheMissFor(scanState.prev, message, models)
}

func detectCacheMissFor(prev *previousRequest, message *ai.AssistantMessage, models ModelPriceSource) (CacheMiss, bool) {
	usage := message.Usage
	promptTokens := promptTokensOf(usage)
	// A zero-cache turn only counts when cache activity was reported before: on
	// cache-read-only providers that is a total miss, while on providers that
	// never report caching it means nothing.
	if prev == nil || promptTokens <= 0 || (usage.CacheRead+usage.CacheWrite == 0 && !prev.reportedCache) {
		return CacheMiss{}, false
	}

	missedTokens := min(prev.PromptTokens, promptTokens) - usage.CacheRead
	if missedTokens <= noiseFloorTokens {
		return CacheMiss{}, false
	}

	// Extra cost = missed tokens billed at the actual paid rate
	// (input/cacheWrite, including the write premium) instead of the cache-read
	// rate. Missed tokens can only land in the input or cacheWrite buckets, so
	// the paid rate comes from this message's own cost breakdown.
	paidTokens := usage.Input + usage.CacheWrite
	paidPerToken := 0.0
	if paidTokens > 0 {
		paidPerToken = (usage.Cost.Input + usage.Cost.CacheWrite) / float64(paidTokens)
	}
	readPerToken := 0.0
	if usage.CacheRead > 0 {
		readPerToken = usage.Cost.CacheRead / float64(usage.CacheRead)
	} else if models != nil {
		if price := models.GetCachePrice(message.Provider, message.Model); price != nil {
			readPerToken = price.CacheRead / 1e6
		}
	}

	return CacheMiss{
		MissedTokens: missedTokens,
		MissedCost:   float64(missedTokens) * math.Max(0, paidPerToken-readPerToken),
		IdleMs:       max(0, message.Timestamp-prev.Timestamp),
		ModelChanged: fmt.Sprintf("%s/%s", message.Provider, message.Model) != prev.ModelKey,
	}, true
}

func asPreviousRequest(message *ai.AssistantMessage, reportedCache bool) *previousRequest {
	return newPreviousRequest(message.Usage, message.Provider, message.Model, message.Timestamp, reportedCache)
}

// newPreviousRequest is the previousRequest an assistant message reports, shared
// so the running scan state and the full scan cannot drift.
func newPreviousRequest(usage ai.Usage, provider string, model string, timestamp int64, reportedCache bool) *previousRequest {
	promptTokens := promptTokensOf(usage)
	if promptTokens <= 0 {
		return nil
	}
	return &previousRequest{
		PromptTokens:  promptTokens,
		ModelKey:      fmt.Sprintf("%s/%s", provider, model),
		Timestamp:     timestamp,
		reportedCache: reportedCache || usage.CacheRead+usage.CacheWrite > 0,
	}
}

func promptTokensOf(usage ai.Usage) int64 {
	return usage.Input + usage.CacheRead + usage.CacheWrite
}

type cacheScanResult struct {
	prev   *previousRequest
	totals CacheWasteTotals
	misses []CacheMissEntry
}

// CacheMissEntry attaches a counted cache miss to the entry that paid for it.
// Upstream keys these by assistant-message reference; the Go port decodes
// session entries from JSON, so the entry index and decoded message are
// returned instead.
type CacheMissEntry struct {
	EntryIndex int
	Message    *ai.AssistantMessage
	Miss       CacheMiss
}

// cacheScanState is the running cache-miss scan state: the prev a full
// scanCacheEntries would end with for the entries appended so far. Upstream
// rescans parsed session entries per assistant message; the port stores raw
// JSON, so a rescan decoded every message entry, measured at 700 ms on a 45 MB
// session. Advancing the state as entries append makes a notice O(1) after the
// load seeds it.
type cacheScanState struct {
	prev *previousRequest

	// candidates are the entries the scan may count against, recorded in file
	// order as the state walks the session. That walk already happened for prev,
	// so the accounting CollectCacheMisses and ComputeCacheWaste need is a fold
	// over this list rather than a second pass over the session — 563ms cold on
	// a 19k-entry session, on the transcript rebuild's UI-loop path (startup,
	// /reload, /tree, a session switch, compaction end).
	candidates []cacheCandidate
	// entryIndex counts every entry consumed, so a candidate reports the index
	// the full scan would have (CacheMissEntry.EntryIndex).
	entryIndex int
}

// cacheCandidate is one entry the scan can count against, as the scan sees it.
type cacheCandidate struct {
	// reset marks a compaction or branch summary: the context legitimately
	// changed, so the scan's prev restarts here.
	reset bool
	// prev is the scan state before this entry, and assistant the message as the
	// scan reads it (usage, provider, model, timestamp — the fields
	// detectCacheMissFor uses). Detection runs later, when the price source is
	// available, through that same helper, so this cannot drift from the scan.
	prev      *previousRequest
	assistant *ai.AssistantMessage
	// entryID resolves the entry later, for the memoized message the transcript
	// keys its notices by.
	entryID string
	index   int
}

// consume advances the state by one entry.
func (s *cacheScanState) consume(entry *SessionEntry) {
	index := s.entryIndex
	s.entryIndex++
	if entry.Type == "compaction" || entry.Type == "branch_summary" {
		// The context legitimately changed; the next turn's prompt is new
		// content, not re-billed content. Model switches are NOT exempt: they
		// re-bill the full prompt and should be counted.
		s.prev = nil
		s.candidates = append(s.candidates, cacheCandidate{reset: true})
		return
	}
	if entry.Type != "message" {
		return
	}
	assistant := scanAssistantFacts(entry.Message)
	next := newPreviousRequest(assistant.Usage, assistant.Provider, assistant.Model, assistant.Timestamp,
		s.prev != nil && s.prev.reportedCache)
	if next == nil {
		return
	}
	// Only the fields detection reads are kept: the content stays in the entry.
	s.candidates = append(s.candidates, cacheCandidate{
		prev:      s.prev,
		assistant: &assistant,
		entryID:   entry.ID,
		index:     index,
	})
	s.prev = next
}

// scanAssistantFacts reads the fields the cache scan compares: usage, provider,
// model and timestamp.
func scanAssistantFacts(raw json.RawMessage) ai.AssistantMessage {
	var assistant struct {
		Role      string   `json:"role"`
		Provider  string   `json:"provider"`
		Model     string   `json:"model"`
		Timestamp int64    `json:"timestamp"`
		Usage     ai.Usage `json:"usage"`
	}
	if json.Unmarshal(raw, &assistant) != nil || assistant.Role != "assistant" {
		return ai.AssistantMessage{}
	}
	return ai.AssistantMessage{
		Provider:  ai.ProviderId(assistant.Provider),
		Model:     assistant.Model,
		Timestamp: assistant.Timestamp,
		Usage:     assistant.Usage,
	}
}

// scanAssistantRequest builds the scan state an assistant message contributes.
// It shares the facts decode with consume, so the running prev and the recorded
// candidates cannot disagree.
func scanAssistantRequest(raw json.RawMessage, reportedCache bool) *previousRequest {
	assistant := scanAssistantFacts(raw)
	if assistant.Usage == (ai.Usage{}) && assistant.Provider == "" && assistant.Model == "" {
		return nil
	}
	return newPreviousRequest(assistant.Usage, assistant.Provider, assistant.Model, assistant.Timestamp, reportedCache)
}

// reset drops the state, for a fresh session or a load that reseeds it.
func (s *cacheScanState) reset() {
	s.prev = nil
	s.candidates = nil
	s.entryIndex = 0
}

// CacheMissFor computes the cache miss for a message that is not yet in the
// session (message_end fires before persistence), from the running scan state.
// It replaces DetectCacheMiss(GetEntries(), message, models) on the notice path,
// which copied the entry tree and decoded every message entry per assistant
// message. CollectCacheMisses keeps the full scan for transcript rebuilds.
func (m *SessionManager) CacheMissFor(message *ai.AssistantMessage, models ModelPriceSource) (CacheMiss, bool) {
	m.mu.Lock()
	prev := m.cacheScan.prev
	m.mu.Unlock()
	return detectCacheMissFor(prev, message, models)
}

// scanCacheEntries walks the session's assistant messages. The cache, when
// there is one, supplies their already-decoded messages (the session manager's
// memo), so the scan stops unmarshalling every message it walks past: on a
// 19k-entry session this was 458ms of the /session panel's ~1.5s. The role is
// read the same way — memoized with a cache, a header-only unmarshal without.
func scanCacheEntries(entries []SessionEntry, models ModelPriceSource, cache *messageCache) cacheScanResult {
	result := cacheScanResult{}

	for index := range entries {
		entry := &entries[index]
		if entry.Type == "compaction" || entry.Type == "branch_summary" {
			// The context legitimately changed; the next turn's prompt is new
			// content, not re-billed content. Model switches are NOT exempt:
			// they re-bill the full prompt and should be counted.
			result.prev = nil
			continue
		}
		if entry.Type != "message" || projectedRole(entry, cache) != "assistant" {
			continue
		}
		assistant := assistantMessageOf(entry, cache)
		if assistant == nil {
			continue
		}
		if miss, ok := detectCacheMissFor(result.prev, assistant, models); ok {
			result.totals.MissedTokens += miss.MissedTokens
			result.totals.MissedCost += miss.MissedCost
			result.totals.MissCount++
			result.misses = append(result.misses, CacheMissEntry{EntryIndex: index, Message: assistant, Miss: miss})
		}
		if next := asPreviousRequest(assistant, result.prev != nil && result.prev.reportedCache); next != nil {
			result.prev = next
		}
	}
	return result
}

// ComputeCacheWaste returns cumulative cache waste across a session: prompt
// tokens that should have been cache reads but were re-billed. This is the
// reference path (no cache); callers that hold a session use
// SessionManager.ComputeCacheWaste.
func ComputeCacheWaste(entries []SessionEntry, models ModelPriceSource) CacheWasteTotals {
	return scanCacheEntries(entries, models, nil).totals
}

// CollectCacheMisses returns every counted cache miss across a session. Used to
// re-derive transcript notices when rebuilding the chat from entries.
func CollectCacheMisses(entries []SessionEntry, models ModelPriceSource) []CacheMissEntry {
	return scanCacheEntries(entries, models, nil).misses
}

// ComputeCacheWaste is ComputeCacheWaste over this session, accounted from the
// scan state the manager keeps as entries arrive instead of walking (and
// decoding) the session again.
func (m *SessionManager) ComputeCacheWaste(models ModelPriceSource) CacheWasteTotals {
	return m.cacheScanResult(models).totals
}

// CollectCacheMisses is CollectCacheMisses over this session.
func (m *SessionManager) CollectCacheMisses(models ModelPriceSource) []CacheMissEntry {
	return m.cacheScanResult(models).misses
}

// cacheScanResult accounts the recorded candidates, with the price source the
// caller has.
func (m *SessionManager) cacheScanResult(models ModelPriceSource) cacheScanResult {
	m.mu.Lock()
	candidates := append([]cacheCandidate{}, m.cacheScan.candidates...)
	m.mu.Unlock()

	result := cacheScanResult{}
	for _, candidate := range candidates {
		if candidate.reset {
			result.prev = nil
			continue
		}
		if candidate.assistant == nil {
			continue
		}
		if miss, ok := detectCacheMissFor(candidate.prev, candidate.assistant, models); ok {
			result.totals.MissedTokens += miss.MissedTokens
			result.totals.MissedCost += miss.MissedCost
			result.totals.MissCount++
			// The transcript keys its notices by the memoized message pointer, so
			// decode that one — only for the misses that were actually counted.
			result.misses = append(result.misses, CacheMissEntry{
				EntryIndex: candidate.index,
				Message:    m.memoizedAssistant(candidate.entryID),
				Miss:       miss,
			})
		}
	}
	return result
}

// memoizedAssistant returns an entry's assistant message through the session's
// message memo (the shared read-only message), or nil.
func (m *SessionManager) memoizedAssistant(entryID string) *ai.AssistantMessage {
	m.mu.Lock()
	entry := m.byID[entryID]
	m.mu.Unlock()
	if entry == nil {
		return nil
	}
	messages := projectedMessages(entry, &m.messages)
	if len(messages) == 0 {
		return nil
	}
	assistant, _ := messages[0].(*ai.AssistantMessage)
	return assistant
}

// assistantMessageOf returns the entry's assistant message, decoded through the
// cache when there is one.
func assistantMessageOf(entry *SessionEntry, cache *messageCache) *ai.AssistantMessage {
	messages := projectedMessages(entry, cache)
	if len(messages) == 0 {
		return nil
	}
	assistant, _ := messages[0].(*ai.AssistantMessage)
	return assistant
}
