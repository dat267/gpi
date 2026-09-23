package coding

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/tui"
)

// Port of cli/list-models.ts: the --list-models table.

// FormatTokenCount renders a token count the way the model list does (upstream
// formatTokenCount): 200000 -> "200K", 1500000 -> "1.5M", 500 -> "500".
func FormatTokenCount(count int64) string {
	switch {
	case count >= 1_000_000:
		return formatTokenUnit(float64(count)/1_000_000, "M")
	case count >= 1_000:
		return formatTokenUnit(float64(count)/1_000, "K")
	default:
		return strconv.FormatInt(count, 10)
	}
}

func formatTokenUnit(value float64, suffix string) string {
	if value == math.Trunc(value) {
		return strconv.FormatInt(int64(value), 10) + suffix
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + suffix
}

// modelListRow is one line of the table.
type modelListRow struct {
	provider string
	model    string
	context  string
	maxOut   string
	thinking string
	images   string
}

func (r modelListRow) columns() [6]string {
	return [6]string{r.provider, r.model, r.context, r.maxOut, r.thinking, r.images}
}

// modelListHeaders are the fixed column titles.
var modelListHeaders = modelListRow{
	provider: "provider", model: "model", context: "context",
	maxOut: "max-out", thinking: "thinking", images: "images",
}

// ListModelsText renders the available-model table (upstream listModels). It
// produces stdout text only: the models.json load warning upstream writes to
// stderr belongs to the caller, which is the only side that has the error.
func ListModelsText(models []*ai.Model, searchPattern string) string {
	if len(models) == 0 {
		return FormatNoModelsAvailableMessage() + "\n"
	}

	filtered := models
	if searchPattern != "" {
		filtered = tui.FuzzyFilter(models, searchPattern, func(model *ai.Model) string {
			return model.Provider + " " + model.ID
		})
	}
	if len(filtered) == 0 {
		return fmt.Sprintf("No models matching %q\n", searchPattern)
	}

	// Provider, then model id. Filtering sorts by match score, so the order has
	// to be re-established afterwards.
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Provider != filtered[j].Provider {
			return filtered[i].Provider < filtered[j].Provider
		}
		return filtered[i].ID < filtered[j].ID
	})

	rows := make([]modelListRow, 0, len(filtered))
	for _, model := range filtered {
		rows = append(rows, modelListRow{
			provider: model.Provider,
			model:    model.ID,
			context:  FormatTokenCount(model.ContextWindow),
			maxOut:   FormatTokenCount(model.MaxTokens),
			thinking: yesNo(model.Reasoning),
			images:   yesNo(containsString(model.Input, "image")),
		})
	}

	widths := modelListHeaders.columns()
	for _, row := range rows {
		columns := row.columns()
		for i, column := range columns {
			if width := runeWidth(column); width > runeWidth(widths[i]) {
				widths[i] = column
			}
		}
	}

	var builder strings.Builder
	builder.WriteString(modelListHeaders.line(widths))
	builder.WriteString("\n")
	for _, row := range rows {
		builder.WriteString(row.line(widths))
		builder.WriteString("\n")
	}
	return builder.String()
}

// line joins the padded columns with the two spaces upstream uses.
func (r modelListRow) line(widths [6]string) string {
	columns := r.columns()
	padded := make([]string, 0, len(columns))
	for i, column := range columns {
		padded = append(padded, padEnd(column, runeWidth(widths[i])))
	}
	return strings.Join(padded, "  ")
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func runeWidth(value string) int {
	return len([]rune(value))
}

// padEnd pads to a rune count, matching JavaScript's String.prototype.padEnd
// for the ASCII model ids this renders.
func padEnd(value string, width int) string {
	if padding := width - runeWidth(value); padding > 0 {
		return value + strings.Repeat(" ", padding)
	}
	return value
}
