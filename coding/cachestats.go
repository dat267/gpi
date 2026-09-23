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
	scanState := scanCacheEntries(entries, models)
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

	missedTokens := min64(prev.PromptTokens, promptTokens) - usage.CacheRead
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
		IdleMs:       max64(0, message.Timestamp-prev.Timestamp),
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
}

// consume advances the state by one entry. It mirrors the body of
// scanCacheEntries, which still accounts the misses for the rebuild path.
func (s *cacheScanState) consume(entry *SessionEntry) {
	if entry.Type == "compaction" || entry.Type == "branch_summary" {
		// The context legitimately changed; the next turn's prompt is new
		// content, not re-billed content. Model switches are NOT exempt: they
		// re-bill the full prompt and should be counted.
		s.prev = nil
		return
	}
	if entry.Type != "message" {
		return
	}
	if next := scanAssistantRequest(entry.Message, s.prev != nil && s.prev.reportedCache); next != nil {
		s.prev = next
	}
}

// scanAssistantRequest builds the scan state an assistant message contributes,
// reading its role, usage, model and timestamp only. Decoding the whole message
// (thinking, tool arguments) made seeding a 45 MB session's state cost 500 ms.
func scanAssistantRequest(raw json.RawMessage, reportedCache bool) *previousRequest {
	var assistant struct {
		Role      string   `json:"role"`
		Provider  string   `json:"provider"`
		Model     string   `json:"model"`
		Timestamp int64    `json:"timestamp"`
		Usage     ai.Usage `json:"usage"`
	}
	if json.Unmarshal(raw, &assistant) != nil || assistant.Role != "assistant" {
		return nil
	}
	if assistant.Usage == (ai.Usage{}) && assistant.Provider == "" && assistant.Model == "" {
		return nil
	}
	return newPreviousRequest(assistant.Usage, assistant.Provider, assistant.Model, assistant.Timestamp, reportedCache)
}

// reset drops the state, for a fresh session or a load that reseeds it.
func (s *cacheScanState) reset() { s.prev = nil }

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

func scanCacheEntries(entries []SessionEntry, models ModelPriceSource) cacheScanResult {
	result := cacheScanResult{}

	for index, entry := range entries {
		if entry.Type == "compaction" || entry.Type == "branch_summary" {
			// The context legitimately changed; the next turn's prompt is new
			// content, not re-billed content. Model switches are NOT exempt:
			// they re-bill the full prompt and should be counted.
			result.prev = nil
			continue
		}
		if entry.Type != "message" || !messageRoleAssistant(entry.Message) {
			continue
		}
		assistant := decodeAssistantMessage(entry.Message)
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
// tokens that should have been cache reads but were re-billed.
func ComputeCacheWaste(entries []SessionEntry, models ModelPriceSource) CacheWasteTotals {
	return scanCacheEntries(entries, models).totals
}

// CollectCacheMisses returns every counted cache miss across a session. Used to
// re-derive transcript notices when rebuilding the chat from entries.
func CollectCacheMisses(entries []SessionEntry, models ModelPriceSource) []CacheMissEntry {
	return scanCacheEntries(entries, models).misses
}

// decodeAssistantMessage parses an assistant message from a stored entry.
// messageRoleAssistant reports whether a raw session message is an assistant
// message, reading only the role. Reading the role through a decoded map (as
// the scan did) decoded every message entry in full.
func messageRoleAssistant(raw json.RawMessage) bool {
	var header struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return false
	}
	return header.Role == "assistant"
}

func decodeAssistantMessage(raw json.RawMessage) *ai.AssistantMessage {
	if len(raw) == 0 {
		return nil
	}
	var message ai.AssistantMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil
	}
	if message.Usage == (ai.Usage{}) && message.Provider == "" && message.Model == "" {
		return nil
	}
	return &message
}

func min64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
