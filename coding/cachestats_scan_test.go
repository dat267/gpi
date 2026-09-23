package coding

import (
	"testing"

	"github.com/dat267/pier/ai"
)

// cacheStatsTestPrices is a ModelPriceSource with a known cache-read price.
type cacheStatsTestPrices struct{}

func (cacheStatsTestPrices) GetCachePrice(provider string, model string) *ModelCachePrice {
	return &ModelCachePrice{CacheRead: 3}
}

// cacheStatsAssistant builds an assistant message with usage.
func cacheStatsAssistant(input, cacheRead, cacheWrite int64, timestamp int64) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Provider: "anthropic", Model: "claude-opus-4-5", Timestamp: timestamp,
		Usage: ai.Usage{
			Input: input, CacheRead: cacheRead, CacheWrite: cacheWrite,
			Cost: ai.UsageCost{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
		},
	}
}

// checkCacheMiss compares the running-state notice against the full scan.
func checkCacheMiss(t *testing.T, m *SessionManager, assistant *ai.AssistantMessage, label string) {
	t.Helper()
	want, wantOK := DetectCacheMiss(m.GetEntries(), assistant, cacheStatsTestPrices{})
	got, gotOK := m.CacheMissFor(assistant, cacheStatsTestPrices{})
	if gotOK != wantOK || got != want {
		t.Fatalf("%s: incremental %+v/%v, full scan %+v/%v", label, got, gotOK, want, wantOK)
	}
}

// TestCacheMissForMatchesTheFullScan is the differential guard for the running
// scan state: for every assistant message the incremental notice must equal the
// one DetectCacheMiss computes from the session's entries, including after a
// compaction and a branch summary (which reset the scan) and for a message with
// no usage (which leaves it alone).
func TestCacheMissForMatchesTheFullScan(t *testing.T) {
	m := newTestSessionForProjection(t)
	steps := []ai.Message{
		&ai.SystemMessage{Content: ai.StringOrBlocks{Text: "prompt"}},
		createUserMessage("hello"),
		cacheStatsAssistant(1000, 0, 0, 1),
		createUserMessage("again"),
		cacheStatsAssistant(200, 800, 0, 2),
		createUserMessage("more"),
		cacheStatsAssistant(200, 900, 0, 3),
		createUserMessage("zero usage round"),
		cacheStatsAssistant(0, 0, 0, 4),
		createUserMessage("after zero"),
		cacheStatsAssistant(300, 700, 0, 5),
	}
	for i, step := range steps {
		if assistant, ok := step.(*ai.AssistantMessage); ok {
			checkCacheMiss(t, m, assistant, "step "+itoa(i))
		}
		m.AppendMessage(step)
	}

	m.AppendCompaction("summary", m.GetEntries()[0].ID, 1000, nil, false, nil)
	afterCompaction := cacheStatsAssistant(500, 0, 0, 6)
	checkCacheMiss(t, m, afterCompaction, "after compaction")
	m.AppendMessage(afterCompaction)

	leaf := m.GetLeafID()
	m.BranchWithSummary(*leaf, "branch summary", nil, false, nil)
	afterBranch := cacheStatsAssistant(600, 0, 0, 7)
	checkCacheMiss(t, m, afterBranch, "after branch summary")
	m.AppendMessage(afterBranch)

	// The state tracks the branch: an assistant message appended on top of the
	// branch sees the branch summary's reset, not the pre-branch request.
	afterAgain := cacheStatsAssistant(700, 100, 0, 8)
	checkCacheMiss(t, m, afterAgain, "after branch append")
}
