package coding

import (
	"sort"

	"github.com/dat267/pier/ai"
)

// Port of core/usage-totals.ts.

// UsageTotals aggregates token counts and cost.
type UsageTotals struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Cost       float64
}

// CreateUsageTotals returns zeroed totals (upstream createUsageTotals).
func CreateUsageTotals() UsageTotals {
	return UsageTotals{}
}

// AddUsageToTotals folds one usage record in.
func AddUsageToTotals(totals *UsageTotals, usage ai.Usage) {
	totals.Input += usage.Input
	totals.Output += usage.Output
	totals.CacheRead += usage.CacheRead
	totals.CacheWrite += usage.CacheWrite
	totals.Cost += usage.Cost.Total
}

// UsageCostBreakdownEntry is one model bucket in the cost breakdown.
type UsageCostBreakdownEntry struct {
	Key    string
	Cost   float64
	Tokens int64
}

// GetUsageCostBreakdown groups attributable assistant usage by model and all
// other usage into a separate bucket. This is the reference path (no cache);
// callers holding a session use SessionManager.UsageCostBreakdown.
func GetUsageCostBreakdown(entries []SessionEntry) []UsageCostBreakdownEntry {
	return usageCostBreakdown(entries, nil)
}

func usageCostBreakdown(entries []SessionEntry, cache *messageCache) []UsageCostBreakdownEntry {
	totalsByKey := map[string]UsageTotals{}
	var order []string

	for index := range entries {
		entry := &entries[index]
		key := ""
		var usage *ai.Usage
		switch entry.Type {
		case "message":
			messages := projectedMessages(entry, cache)
			if len(messages) == 0 {
				continue
			}
			switch message := messages[0].(type) {
			case *ai.AssistantMessage:
				model := message.Model
				if message.ResponseModel != nil {
					model = *message.ResponseModel
				}
				key = string(message.Provider) + "/" + model
				value := message.Usage
				usage = &value
			case *ai.ToolResultMessage:
				if message.Usage != nil {
					key = "Tools/summaries"
					usage = message.Usage
				}
			}
		case "branch_summary", "compaction":
			if entry.Usage != nil {
				key = "Tools/summaries"
				usage = entry.Usage
			}
		}
		if key == "" || usage == nil {
			continue
		}

		totals, ok := totalsByKey[key]
		if !ok {
			totals = CreateUsageTotals()
			order = append(order, key)
		}
		AddUsageToTotals(&totals, *usage)
		totalsByKey[key] = totals
	}

	out := make([]UsageCostBreakdownEntry, 0, len(order))
	for _, key := range order {
		totals := totalsByKey[key]
		out = append(out, UsageCostBreakdownEntry{
			Key:    key,
			Cost:   totals.Cost,
			Tokens: totals.Input + totals.Output + totals.CacheRead + totals.CacheWrite,
		})
	}
	filtered := out[:0]
	for _, entry := range out {
		if entry.Cost > 0 || entry.Tokens > 0 {
			filtered = append(filtered, entry)
		}
	}
	// Upstream sorts descending by cost; equal costs keep insertion order.
	sort.SliceStable(filtered, func(left, right int) bool {
		return filtered[left].Cost > filtered[right].Cost
	})
	return filtered
}
