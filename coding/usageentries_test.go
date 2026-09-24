package coding

import (
	"testing"

	"github.com/dat267/pier/ai"
)

// Cache-warming usage is recorded as a `usage` entry carrying its own provider

// and model, and upstream's breakdown attributes it to `provider/model` and its
// tokens to the session totals (usage-totals.ts checks `entry.type === "usage"`,
// getSessionStats adds it to the running totals). The port dropped both.
func TestUsageEntriesAreAttributed(t *testing.T) {
	manager := NewSessionManager(t.TempDir(), &SessionManagerOptions{Persist: boolPtr(false)})
	manager.AppendMessage(&ai.AssistantMessage{
		Provider: "anthropic", Model: "sonnet", Content: ai.ContentList{ai.TextContent{Text: "a"}},
		Usage: ai.Usage{Input: 10, Cost: ai.UsageCost{Total: 0.1}}, StopReason: ai.StopStop,
	})
	manager.AppendUsage("cache_warm", "anthropic", "sonnet-4",
		ai.Usage{Input: 100, Output: 1, CacheRead: 5, CacheWrite: 2, Cost: ai.UsageCost{Total: 0.25}}, "")

	want := []UsageCostBreakdownEntry{
		{Key: "anthropic/sonnet-4", Cost: 0.25, Tokens: 108},
		{Key: "anthropic/sonnet", Cost: 0.1, Tokens: 10},
	}
	got := manager.UsageCostBreakdown()
	if len(got) != len(want) {
		t.Fatalf("breakdown = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if reference := GetUsageCostBreakdown(manager.GetEntries()); len(reference) != len(want) {
		t.Errorf("the reference walk dropped the usage entry too: %+v", reference)
	}

	// The usage entry counts in the totals, but it is not a message.
	stats := manager.SessionStats()
	if stats.TotalMessages != 1 || stats.AssistantMessages != 1 {
		t.Errorf("counts = %+v", stats)
	}
	if stats.Tokens.Input != 110 || stats.Tokens.CacheRead != 5 || stats.Tokens.Total != 118 {
		t.Errorf("tokens = %+v", stats.Tokens)
	}
	if stats.Cost != 0.35 {
		t.Errorf("cost = %v", stats.Cost)
	}
}
