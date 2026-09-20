package interactive

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// Port of src/modes/interactive/components/footer.ts: the footer with the
// working directory, token stats and context usage.
//
// Divergence D108: the footer takes a narrow FooterSession interface instead of
// the concrete AgentSession so the formatting logic is testable.

// FooterSession is the session surface the footer needs.
type FooterSession interface {
	Model() *ai.Model
	ThinkingLevel() ai.ThinkingLevel
	SessionManager() *coding.SessionManager
	GetContextUsage() *coding.ContextUsageReport
	IsUsingSubscription(providerID string) bool
}

// FooterHomeDir returns the home directory used for path shortening.
var FooterHomeDir = func() string { return homeDirValue() }

func homeDirValue() string {
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

// sanitizeStatusText collapses control characters for single-line statuses.
func sanitizeStatusText(text string) string {
	replaced := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(text)
	for strings.Contains(replaced, "  ") {
		replaced = strings.ReplaceAll(replaced, "  ", " ")
	}
	return strings.TrimSpace(replaced)
}

// FormatTokens formats token counts for the compact footer display.
func FormatTokens(count int64) string {
	switch {
	case count < 1000:
		return strconv.FormatInt(count, 10)
	case count < 10000:
		return formatFixed(float64(count)/1000, 1) + "k"
	case count < 1000000:
		return strconv.FormatInt(int64(math.Round(float64(count)/1000)), 10) + "k"
	case count < 10000000:
		return formatFixed(float64(count)/1000000, 1) + "M"
	default:
		return strconv.FormatInt(int64(math.Round(float64(count)/1000000)), 10) + "M"
	}
}

// formatFixed mirrors JS Number.toFixed (round-half-away-from-zero is not
// exactly IEEE round-half-even, but matches for the token magnitudes here).
func formatFixed(value float64, digits int) string {
	return strconv.FormatFloat(value, 'f', digits, 64)
}

// FormatCwdForFooter shortens a working directory with a home prefix.
func FormatCwdForFooter(cwd string, home string) string {
	if home == "" {
		return cwd
	}
	resolvedCwd := filepath.Clean(cwd)
	resolvedHome := filepath.Clean(home)
	relativeToHome, err := filepath.Rel(resolvedHome, resolvedCwd)
	if err != nil {
		return cwd
	}
	// filepath.Rel returns "." for equal paths; Node's path.relative returns "".
	isInsideHome := relativeToHome == "." ||
		(relativeToHome != ".." && !strings.HasPrefix(relativeToHome, ".."+string(filepath.Separator)) &&
			!filepath.IsAbs(relativeToHome))
	if !isInsideHome {
		return cwd
	}
	if relativeToHome == "." {
		return "~"
	}
	return "~" + string(filepath.Separator) + relativeToHome
}

// FooterComponent shows the working directory, token stats and context usage.
type FooterComponent struct {
	autoCompactEnabled bool
	session            FooterSession
	footerData         coding.ReadonlyFooterDataProvider
}

// NewFooterComponent creates the footer.
func NewFooterComponent(session FooterSession, footerData coding.ReadonlyFooterDataProvider) *FooterComponent {
	return &FooterComponent{autoCompactEnabled: true, session: session, footerData: footerData}
}

// SetSession replaces the session.
func (f *FooterComponent) SetSession(session FooterSession) { f.session = session }

// SetAutoCompactEnabled updates the auto-compact indicator.
func (f *FooterComponent) SetAutoCompactEnabled(enabled bool) { f.autoCompactEnabled = enabled }

// Invalidate is a no-op (the provider caches the branch).
func (f *FooterComponent) Invalidate() {}

// Dispose is a no-op (the provider owns the watcher).
func (f *FooterComponent) Dispose() {}

// Render renders the footer lines.
func (f *FooterComponent) Render(width int) []string {
	theme := ActiveTheme()
	model := f.session.Model()
	thinkingLevel := string(f.session.ThinkingLevel())

	usageTotals := coding.CreateUsageTotals()
	var latestCacheHitRate *float64

	manager := f.session.SessionManager()
	entries := manager.GetEntries()
	for index := range entries {
		entry := &entries[index]
		switch {
		case entry.Type == "usage" && entry.Usage != nil:
			coding.AddUsageToTotals(&usageTotals, *entry.Usage)
		case entry.Type == "message":
			message := decodeTreeMessage(entry.Message)
			if message.Role == "assistant" {
				usage := decodeMessageUsage(entry.Message)
				coding.AddUsageToTotals(&usageTotals, usage)
				latestPromptTokens := usage.Input + usage.CacheRead + usage.CacheWrite
				if latestPromptTokens > 0 {
					rate := float64(usage.CacheRead) / float64(latestPromptTokens) * 100
					latestCacheHitRate = &rate
				}
			} else if message.Role == "toolResult" {
				if usage, ok := decodeOptionalMessageUsage(entry.Message); ok {
					coding.AddUsageToTotals(&usageTotals, usage)
				}
			}
		case (entry.Type == "branch_summary" || entry.Type == "compaction") && entry.Usage != nil:
			coding.AddUsageToTotals(&usageTotals, *entry.Usage)
		}
	}

	contextUsage := f.session.GetContextUsage()
	contextWindow := int64(0)
	if contextUsage != nil {
		contextWindow = contextUsage.ContextWindow
	} else if model != nil {
		contextWindow = model.ContextWindow
	}
	// Upstream: contextUsage?.percent !== null — a missing usage object still
	// counts as known (0%), only an explicit null percent shows "?".
	contextPercentValue := 0.0
	hasPercent := true
	if contextUsage != nil {
		if contextUsage.Percent == nil {
			hasPercent = false
		} else {
			contextPercentValue = *contextUsage.Percent
		}
	}
	contextPercent := "?"
	if hasPercent {
		contextPercent = formatFixed(contextPercentValue, 1)
	}

	pwd := FormatCwdForFooter(manager.GetCwd(), FooterHomeDir())
	if branch, ok := f.footerData.GetGitBranch(); ok && branch != "" {
		pwd = pwd + " (" + branch + ")"
	}
	if sessionName := manager.GetSessionName(); sessionName != "" {
		pwd = pwd + " • " + sessionName
	}

	var statsParts []string
	if usageTotals.Input != 0 {
		statsParts = append(statsParts, "↑"+FormatTokens(usageTotals.Input))
	}
	if usageTotals.Output != 0 {
		statsParts = append(statsParts, "↓"+FormatTokens(usageTotals.Output))
	}
	if usageTotals.CacheRead != 0 {
		statsParts = append(statsParts, "R"+FormatTokens(usageTotals.CacheRead))
	}
	if usageTotals.CacheWrite != 0 {
		statsParts = append(statsParts, "W"+FormatTokens(usageTotals.CacheWrite))
	}
	if (usageTotals.CacheRead > 0 || usageTotals.CacheWrite > 0) && latestCacheHitRate != nil {
		statsParts = append(statsParts, "CH"+formatFixed(*latestCacheHitRate, 1)+"%")
	}

	usingSubscription := false
	if model != nil {
		usingSubscription = model.Provider == "kimi-coding" || f.session.IsUsingSubscription(model.Provider)
	}
	if usageTotals.Cost != 0 || usingSubscription {
		costStr := "$" + formatFixed(usageTotals.Cost, 3)
		if usingSubscription {
			costStr += " (sub)"
		}
		statsParts = append(statsParts, costStr)
	}

	autoIndicator := ""
	if f.autoCompactEnabled {
		autoIndicator = " (auto)"
	}
	contextPercentDisplay := contextPercent + "%/" + FormatTokens(contextWindow) + autoIndicator
	if !hasPercent {
		contextPercentDisplay = "?/" + FormatTokens(contextWindow) + autoIndicator
	}
	var contextPercentStr string
	switch {
	case contextPercentValue > 90:
		contextPercentStr = theme.Fg("error", contextPercentDisplay)
	case contextPercentValue > 70:
		contextPercentStr = theme.Fg("warning", contextPercentDisplay)
	default:
		contextPercentStr = contextPercentDisplay
	}
	statsParts = append(statsParts, contextPercentStr)
	if coding.AreExperimentalFeaturesEnabled() {
		statsParts = append(statsParts, theme.Fg("dim", "•")+" "+theme.Bold(theme.Fg("warning", "xp")))
	}

	statsLeft := strings.Join(statsParts, " ")
	modelName := "no-model"
	if model != nil && model.ID != "" {
		modelName = model.ID
	}

	statsLeftWidth := tui.VisibleWidth(statsLeft)
	if statsLeftWidth > width {
		statsLeft = tui.TruncateToWidth(statsLeft, width, "...", false)
		statsLeftWidth = tui.VisibleWidth(statsLeft)
	}

	const minPadding = 2

	rightSideWithoutProvider := modelName
	if model != nil && model.Reasoning {
		if thinkingLevel == "" || thinkingLevel == "off" {
			rightSideWithoutProvider = modelName + " • thinking off"
		} else {
			rightSideWithoutProvider = modelName + " • " + thinkingLevel
		}
	}

	rightSide := rightSideWithoutProvider
	if f.footerData.GetAvailableProviderCount() > 1 && model != nil {
		rightSide = "(" + model.Provider + ") " + rightSideWithoutProvider
		if statsLeftWidth+minPadding+tui.VisibleWidth(rightSide) > width {
			rightSide = rightSideWithoutProvider
		}
	}

	rightSideWidth := tui.VisibleWidth(rightSide)
	totalNeeded := statsLeftWidth + minPadding + rightSideWidth

	var statsLine string
	if totalNeeded <= width {
		padding := strings.Repeat(" ", maxIntLocal(0, width-statsLeftWidth-rightSideWidth))
		statsLine = statsLeft + padding + rightSide
	} else {
		availableForRight := width - statsLeftWidth - minPadding
		if availableForRight > 0 {
			truncatedRight := tui.TruncateToWidth(rightSide, availableForRight, "", false)
			truncatedRightWidth := tui.VisibleWidth(truncatedRight)
			padding := strings.Repeat(" ", maxIntLocal(0, width-statsLeftWidth-truncatedRightWidth))
			statsLine = statsLeft + padding + truncatedRight
		} else {
			statsLine = statsLeft
		}
	}

	dimStatsLeft := theme.Fg("dim", statsLeft)
	remainder := ""
	if len(statsLine) >= len(statsLeft) {
		remainder = statsLine[len(statsLeft):]
	}
	dimRemainder := theme.Fg("dim", remainder)

	pwdLine := tui.TruncateToWidth(theme.Fg("dim", pwd), width, theme.Fg("dim", "..."), false)
	lines := []string{pwdLine, dimStatsLeft + dimRemainder}

	statuses := f.footerData.GetExtensionStatuses()
	if len(statuses) > 0 {
		keys := make([]string, 0, len(statuses))
		for key := range statuses {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		texts := make([]string, 0, len(keys))
		for _, key := range keys {
			texts = append(texts, sanitizeStatusText(statuses[key]))
		}
		lines = append(lines, tui.TruncateToWidth(strings.Join(texts, " "), width, theme.Fg("dim", "..."), false))
	}
	return lines
}

// decodeMessageUsage extracts an assistant message's usage (zero when absent).
func decodeMessageUsage(raw []byte) ai.Usage {
	usage, _ := decodeOptionalMessageUsage(raw)
	return usage
}

func decodeOptionalMessageUsage(raw []byte) (ai.Usage, bool) {
	var decoded struct {
		Usage *ai.Usage `json:"usage"`
	}
	if len(raw) == 0 {
		return ai.Usage{}, false
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ai.Usage{}, false
	}
	if decoded.Usage == nil {
		return ai.Usage{}, false
	}
	return *decoded.Usage, true
}
