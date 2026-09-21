package interactive

import (
	"strings"

	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/{status-indicator,
// branch-summary-message, compaction-summary-message}.ts.

// StatusIndicatorKind classifies the status indicator.
type StatusIndicatorKind string

const (
	StatusWorking       StatusIndicatorKind = "working"
	StatusRetry         StatusIndicatorKind = "retry"
	StatusCompaction    StatusIndicatorKind = "compaction"
	StatusBranchSummary StatusIndicatorKind = "branchSummary"
)

// StatusIndicator is a loader with a kind and border-aware rendering.
type StatusIndicator struct {
	*tui.Loader

	Kind StatusIndicatorKind
}

// IndicatorKind returns the indicator kind (StatusIndicatorLike).
func (s *StatusIndicator) IndicatorKind() StatusIndicatorKind { return s.Kind }

// StatusIndicatorLike is the indicator surface the UI state manages.
type StatusIndicatorLike interface {
	tui.Component
	IndicatorKind() StatusIndicatorKind
	Dispose()
}

// NewStatusIndicator creates the indicator.
func NewStatusIndicator(kind StatusIndicatorKind, host tui.RenderRequester, spinnerColor func(string) string, messageColor func(string) string, message string, indicator *tui.LoaderIndicatorOptions) *StatusIndicator {
	return &StatusIndicator{
		Loader: tui.NewLoader(host, spinnerColor, messageColor, message, indicator),
		Kind:   kind,
	}
}

// RenderInBorder renders the message line without the leading space.
func (s *StatusIndicator) RenderInBorder(width int) string {
	lines := s.Loader.Render(width + 2)
	line := ""
	if len(lines) > 1 {
		line = lines[1]
	}
	if len(line) > 0 && line[0] == ' ' {
		line = line[1:]
	}
	return tui.TruncateToWidth(trimRightSpaces(line), width, "", false)
}

// RenderSpinnerInBorder renders just the spinner frame.
func (s *StatusIndicator) RenderSpinnerInBorder(width int) string {
	return tui.TruncateToWidth(s.Loader.RenderedIndicator(), width, "", false)
}

// Dispose stops the loader.
func (s *StatusIndicator) Dispose() { s.Loader.Stop() }

// WorkingStatusIndicator shows the working spinner.
func NewWorkingStatusIndicator(host tui.RenderRequester, message string, indicator *tui.LoaderIndicatorOptions, colorFn func(string) string) *StatusIndicator {
	spinnerColor := colorFn
	messageColor := colorFn
	if spinnerColor == nil {
		spinnerColor = func(text string) string { return ActiveTheme().Fg("accent", text) }
	}
	if messageColor == nil {
		messageColor = func(text string) string { return ActiveTheme().Fg("muted", text) }
	}
	return NewStatusIndicator(StatusWorking, host, spinnerColor, messageColor, message, indicator)
}

// RetryStatusIndicator shows the retry countdown.
type RetryStatusIndicator struct {
	*StatusIndicator

	countdown *CountdownTimer
}

// NewRetryStatusIndicator creates the retry indicator.
func NewRetryStatusIndicator(host tui.RenderRequester, attempt int, maxAttempts int, delayMS int) *RetryStatusIndicator {
	retryMessage := func(seconds int) string {
		return "Retrying (" + itoa(attempt) + "/" + itoa(maxAttempts) + ") in " + itoa(seconds) +
			"s... (" + KeyText("app.interrupt") + " to cancel)"
	}
	indicator := &RetryStatusIndicator{
		StatusIndicator: NewStatusIndicator(StatusRetry, host,
			func(spinner string) string { return ActiveTheme().Fg("warning", spinner) },
			func(text string) string { return ActiveTheme().Fg("muted", text) },
			retryMessage((delayMS+999)/1000), nil),
	}
	if host != nil {
		indicator.countdown = NewCountdownTimer(delayMS, host,
			func(seconds int) { indicator.Loader.SetMessage(retryMessage(seconds)) },
			func() { indicator.countdown = nil })
	}
	return indicator
}

// Dispose stops the indicator and the countdown.
func (r *RetryStatusIndicator) Dispose() {
	if r.countdown != nil {
		r.countdown.Dispose()
		r.countdown = nil
	}
	r.StatusIndicator.Dispose()
}

// NewCompactionStatusIndicator creates the compaction indicator.
func NewCompactionStatusIndicator(host tui.RenderRequester, reason string) *StatusIndicator {
	cancelHint := "(" + KeyText("app.interrupt") + " to cancel)"
	label := ""
	switch reason {
	case "manual":
		label = "Compacting context... " + cancelHint
	default:
		prefix := ""
		if reason == "overflow" {
			prefix = "Context overflow detected, "
		}
		label = prefix + "Auto-compacting... " + cancelHint
	}
	return NewStatusIndicator(StatusCompaction, host,
		func(spinner string) string { return ActiveTheme().Fg("accent", spinner) },
		func(text string) string { return ActiveTheme().Fg("muted", text) },
		label, nil)
}

// NewBranchSummaryStatusIndicator creates the branch summary indicator.
func NewBranchSummaryStatusIndicator(host tui.RenderRequester) *StatusIndicator {
	return NewStatusIndicator(StatusBranchSummary, host,
		func(spinner string) string { return ActiveTheme().Fg("accent", spinner) },
		func(text string) string { return ActiveTheme().Fg("muted", text) },
		"Summarizing branch... ("+KeyText("app.interrupt")+" to cancel)", nil)
}

// IdleStatus renders two empty lines.
type IdleStatus struct{}

// Invalidate drops cached state (none).
func (IdleStatus) Invalidate() {}

// Render renders the empty lines.
func (IdleStatus) Render(width int) []string {
	emptyLine := repeatSpacesLocal(width)
	return []string{emptyLine, emptyLine}
}

func repeatSpacesLocal(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.Repeat(" ", count)
}

func trimRightSpaces(value string) string {
	end := len(value)
	for end > 0 && value[end-1] == ' ' {
		end--
	}
	return value[:end]
}

// BranchSummaryMessageComponent renders a branch summary with the
// collapsed/expanded state.
type BranchSummaryMessageComponent struct {
	*tui.Box

	summary       string
	markdownTheme tui.MarkdownTheme
	expanded      bool
}

// NewBranchSummaryMessageComponent creates the component.
func NewBranchSummaryMessageComponent(summary string, markdownTheme *tui.MarkdownTheme) *BranchSummaryMessageComponent {
	theme := ActiveTheme()
	component := &BranchSummaryMessageComponent{
		Box:           tui.NewBox(1, 1, func(text string) string { return theme.Bg("customMessageBg", text) }),
		summary:       summary,
		markdownTheme: markdownThemeValue(markdownTheme),
	}
	component.updateDisplay()
	return component
}

// formatThousands formats an integer like JS's Number.toLocaleString() in the
// default (en-US) locale.
func formatThousands(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	digits := itoa64(value)
	var builder strings.Builder
	for index, digit := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			builder.WriteByte(',')
		}
		builder.WriteRune(digit)
	}
	result := builder.String()
	if negative {
		return "-" + result
	}
	return result
}

func itoa64(value int64) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}

func markdownThemeValue(theme *tui.MarkdownTheme) tui.MarkdownTheme {
	if theme != nil {
		return *theme
	}
	return GetMarkdownTheme()
}

// SetExpanded toggles the expanded state.
func (c *BranchSummaryMessageComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.updateDisplay()
}

// Invalidate rebuilds the content.
func (c *BranchSummaryMessageComponent) Invalidate() {
	c.Box.Invalidate()
	c.updateDisplay()
}

func (c *BranchSummaryMessageComponent) updateDisplay() {
	theme := ActiveTheme()
	c.Box.Clear()
	content := &tui.Container{}
	label := theme.Fg("customMessageLabel", "\x1b[1m[branch]\x1b[22m")
	content.AddChild(tui.NewText(label, 0, 0, nil))
	content.AddChild(tui.NewSpacer(1))
	if c.expanded {
		header := "**Branch Summary**\n\n"
		content.AddChild(tui.NewMarkdown(header+c.summary, 0, 0, c.markdownTheme,
			&tui.DefaultTextStyle{Color: func(text string) string { return theme.Fg("customMessageText", text) }},
			tui.MarkdownOptions{}))
	} else {
		content.AddChild(tui.NewText(
			theme.Fg("customMessageText", "Branch summary (")+
				theme.Fg("dim", KeyText("app.tools.expand"))+
				theme.Fg("customMessageText", " to expand)"), 0, 0, nil))
	}
	c.Box.AddChild(tui.NewMouseRegion(content, func(event tui.TuiMouseEvent) *tui.TuiMouseDispatchResult {
		if event.Type != tui.MouseClick || event.Button != tui.MouseButtonLeft {
			return nil
		}
		c.SetExpanded(!c.expanded)
		return &tui.TuiMouseDispatchResult{TuiMouseEventResult: tui.TuiMouseEventResult{Handled: true}}
	}))
}

// CompactionSummaryMessageComponent renders a compaction summary.
type CompactionSummaryMessageComponent struct {
	*tui.Box

	summary       string
	tokensBefore  int64
	markdownTheme tui.MarkdownTheme
	expanded      bool
}

// NewCompactionSummaryMessageComponent creates the component.
func NewCompactionSummaryMessageComponent(summary string, tokensBefore int64, markdownTheme *tui.MarkdownTheme) *CompactionSummaryMessageComponent {
	theme := ActiveTheme()
	component := &CompactionSummaryMessageComponent{
		Box:           tui.NewBox(1, 1, func(text string) string { return theme.Bg("customMessageBg", text) }),
		summary:       summary,
		tokensBefore:  tokensBefore,
		markdownTheme: markdownThemeValue(markdownTheme),
	}
	component.updateDisplay()
	return component
}

// SetExpanded toggles the expanded state.
func (c *CompactionSummaryMessageComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.updateDisplay()
}

// Invalidate rebuilds the content.
func (c *CompactionSummaryMessageComponent) Invalidate() {
	c.Box.Invalidate()
	c.updateDisplay()
}

func (c *CompactionSummaryMessageComponent) updateDisplay() {
	theme := ActiveTheme()
	c.Box.Clear()
	content := &tui.Container{}
	label := theme.Fg("customMessageLabel", "\x1b[1m[compaction]\x1b[22m")
	content.AddChild(tui.NewText(label, 0, 0, nil))
	content.AddChild(tui.NewSpacer(1))
	tokenStr := formatThousands(c.tokensBefore)
	if c.expanded {
		header := "**Compacted from " + tokenStr + " tokens**\n\n"
		content.AddChild(tui.NewMarkdown(header+c.summary, 0, 0, c.markdownTheme,
			&tui.DefaultTextStyle{Color: func(text string) string { return theme.Fg("customMessageText", text) }},
			tui.MarkdownOptions{}))
	} else {
		content.AddChild(tui.NewText(
			theme.Fg("customMessageText", "Compacted from "+tokenStr+" tokens (")+
				theme.Fg("dim", KeyText("app.tools.expand"))+
				theme.Fg("customMessageText", " to expand)"), 0, 0, nil))
	}
	c.Box.AddChild(tui.NewMouseRegion(content, func(event tui.TuiMouseEvent) *tui.TuiMouseDispatchResult {
		if event.Type != tui.MouseClick || event.Button != tui.MouseButtonLeft {
			return nil
		}
		c.SetExpanded(!c.expanded)
		return &tui.TuiMouseDispatchResult{TuiMouseEventResult: tui.TuiMouseEventResult{Handled: true}}
	}))
}
