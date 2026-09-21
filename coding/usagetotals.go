package coding

import (
	"encoding/json"
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
// other usage into a separate bucket.
func GetUsageCostBreakdown(entries []SessionEntry) []UsageCostBreakdownEntry {
	totalsByKey := map[string]UsageTotals{}
	var order []string

	for _, entry := range entries {
		key := ""
		var usage *ai.Usage
		switch entry.Type {
		case "message":
			message := decodeSessionMessage(entry.Message)
			if message == nil {
				continue
			}
			var usageValue *ai.Usage
			if message["role"] == "assistant" {
				model := stringField(message, "responseModel")
				if model == "" {
					model = stringField(message, "model")
				}
				key = stringField(message, "provider") + "/" + model
				usageValue = usageFromRaw(message["usage"])
			} else if message["role"] == "toolResult" {
				usageValue = usageFromRaw(message["usage"])
				if usageValue != nil {
					key = "Tools/summaries"
				}
			}
			usage = usageValue
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

// decodeSessionMessage parses a stored session message payload.
func decodeSessionMessage(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return decoded
}

func stringField(record map[string]any, key string) string {
	value, _ := record[key].(string)
	return value
}

// usageFromRaw decodes a usage object; explicit zeros are preserved.
func usageFromRaw(raw any) *ai.Usage {
	if raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var usage ai.Usage
	if err := json.Unmarshal(encoded, &usage); err != nil {
		return nil
	}
	return &usage
}
