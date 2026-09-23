package interactive

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// The footer renders a single dim line: "3.5%/1M · <statuses> · model · cwd".
//
// Divergence D141: upstream's builtin footer (src/modes/interactive/components/
// footer.ts) walks every session entry per render. Upstream reads plain JS
// objects there; the port stores messages as raw JSON, so the same walk means
// json.Unmarshal per message per frame — O(session size) per frame, which made
// scrolling long sessions crawl (445ms/frame on a 10k-entry session). The
// builtin footer therefore renders the user's footer extension instead
// (~/.pi/agent/extensions/footer, format.ts): one line from cached session
// state only. The upstream formatting helpers that remain
// (FormatTokens, FormatCwdForFooter) keep their upstream behavior.
//
// Divergence D108: the footer takes a narrow FooterSession interface instead of
// the concrete AgentSession so the formatting logic is testable.

// FooterSession is the session surface the footer needs.
type FooterSession interface {
	Model() *ai.Model
	SessionManager() *coding.SessionManager
	GetContextUsage() *coding.ContextUsageReport
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

// FormatTokens formats token counts for the compact footer display (upstream
// footer.ts formatTokens).
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

// FormatTokensSimple is the footer extension's token format
// (format.ts formatTokens): no fractional megabytes.
func FormatTokensSimple(count int64) string {
	switch {
	case count < 1000:
		return strconv.FormatInt(count, 10)
	case count < 10000:
		return formatFixed(float64(count)/1000, 1) + "k"
	case count < 1000000:
		return strconv.FormatInt(int64(math.Round(float64(count)/1000)), 10) + "k"
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

// FooterTruncate truncates by display width (CJK-safe); plain text only, no
// ANSI (format.ts truncate).
func FooterTruncate(text string, maxWidth int) string {
	if maxWidth <= 3 {
		return "..."[:max(0, maxWidth)]
	}
	if tui.VisibleWidth(text) <= maxWidth {
		return text
	}
	var out strings.Builder
	w := 0
	for _, ch := range text {
		cw := tui.VisibleWidth(string(ch))
		if w+cw > maxWidth-3 {
			break
		}
		out.WriteRune(ch)
		w += cw
	}
	return out.String() + "..."
}

// FooterTruncateLeft keeps the rightmost chars with an ellipsis prefix
// (format.ts truncateLeft).
func FooterTruncateLeft(text string, maxWidth int) string {
	if maxWidth <= 3 {
		return "..."[:max(0, maxWidth)]
	}
	if tui.VisibleWidth(text) <= maxWidth {
		return text
	}
	runes := []rune(text)
	var out []rune
	w := 0
	for index := len(runes) - 1; index >= 0; index-- {
		cw := tui.VisibleWidth(string(runes[index]))
		if w+cw > maxWidth-3 {
			break
		}
		out = append([]rune{runes[index]}, out...)
		w += cw
	}
	return "..." + string(out)
}

// FooterInput is the footer line's input (format.ts FooterInput).
type FooterInput struct {
	ContextUsage *coding.ContextUsageReport
	// ModelWindow is the fallback window when ContextUsage has none.
	ModelWindow int64
	ModelID     string
	Cwd         string
	// Statuses sit after the context count, before the model.
	Statuses []string
}

// FooterLine composes the full footer line:
// "3.5%/1M · <statuses> · model · cwd" (format.ts footerLine).
func FooterLine(input FooterInput, width int) string {
	window := input.ModelWindow
	if input.ContextUsage != nil && input.ContextUsage.ContextWindow > 0 {
		window = input.ContextUsage.ContextWindow
	}
	var contextDisplay string
	if input.ContextUsage == nil || input.ContextUsage.Percent == nil {
		contextDisplay = "?/" + FormatTokensSimple(window)
	} else {
		contextDisplay = formatFixed(*input.ContextUsage.Percent, 1) + "%/" + FormatTokensSimple(window)
	}

	parts := []string{contextDisplay}
	for _, text := range input.Statuses {
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	if input.ModelID != "" {
		parts = append(parts, FooterTruncateLeft(input.ModelID, 25))
	}
	parts = append(parts, FooterTruncate(filepath.Base(input.Cwd), 25))
	return FooterTruncate(strings.Join(parts, " · "), min(80, max(0, width)))
}

// FooterComponent renders the footer line.
type FooterComponent struct {
	autoCompactEnabled bool
	session            FooterSession
	footerData         coding.ReadonlyFooterDataProvider

	// Render cache. The dispatcher invalidates the footer on every session
	// event, so a cache hit means nothing that the line displays can have
	// changed; the fingerprint guards the inputs Invalidate cannot cover
	// (model switches, status edits, cwd moves). Invalidate arrives on the
	// session's event goroutine while Render runs under the render lock (D141).
	cachedWidth   int
	cachedLines   []string
	fingerprint   string
	hasCachedLine bool
}

// NewFooterComponent creates the footer.
func NewFooterComponent(session FooterSession, footerData coding.ReadonlyFooterDataProvider) *FooterComponent {
	return &FooterComponent{autoCompactEnabled: true, session: session, footerData: footerData}
}

// SetSession replaces the session.
func (f *FooterComponent) SetSession(session FooterSession) { f.session = session }

// SetAutoCompactEnabled is kept for compatibility with the wiring; the simple
// footer does not display the auto-compact indicator.
func (f *FooterComponent) SetAutoCompactEnabled(enabled bool) { f.autoCompactEnabled = enabled }

// Invalidate drops the render cache (the dispatcher calls it on every session
// event).
func (f *FooterComponent) Invalidate() {
	// Loop-owned: invalidations arrive as session events and Render runs on the
	// loop (stage 4: no cache lock).
	f.hasCachedLine = false
	f.cachedLines = nil
}

// Dispose is a no-op (the provider owns the watcher).
func (f *FooterComponent) Dispose() {}

// Render renders the single dim footer line.
func (f *FooterComponent) Render(width int) []string {
	theme := ActiveTheme()
	model := f.session.Model()

	var statuses []string
	extensionStatuses := f.footerData.GetExtensionStatuses()
	if len(extensionStatuses) > 0 {
		keys := make([]string, 0, len(extensionStatuses))
		for key := range extensionStatuses {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			statuses = append(statuses, extensionStatuses[key])
		}
	}

	modelWindow := int64(0)
	var modelID string
	if model != nil {
		modelWindow = model.ContextWindow
		modelID = model.ID
	}
	cwd := f.session.SessionManager().GetCwd()
	fingerprint := strings.Join([]string{
		modelID, strconv.FormatInt(modelWindow, 10), cwd,
		strconv.Quote(strings.Join(statuses, "\x00")),
	}, "\x1f")
	if f.hasCachedLine && f.cachedWidth == width && f.fingerprint == fingerprint {
		return f.cachedLines
	}

	line := FooterLine(FooterInput{
		ContextUsage: f.session.GetContextUsage(),
		ModelWindow:  modelWindow,
		ModelID:      modelID,
		Cwd:          cwd,
		Statuses:     statuses,
	}, width)
	f.cachedWidth = width
	f.cachedLines = []string{theme.Fg("dim", line)}
	f.fingerprint = fingerprint
	f.hasCachedLine = true
	return f.cachedLines
}
