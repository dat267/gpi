package interactive

import (
	"runtime"
	"strings"
	"time"

	"github.com/dat267/gpi/tui"
)

// Port of src/modes/interactive/components/keybinding-hints.ts,
// countdown-timer.ts, dynamic-border.ts, and visual-truncate.ts.

// KeyTextFormatOptions configure key text formatting.
type KeyTextFormatOptions struct {
	Capitalize bool
}

func formatKeyPart(part string, options KeyTextFormatOptions) string {
	displayPart := part
	if runtime.GOOS == "darwin" && strings.ToLower(part) == "alt" {
		displayPart = "option"
	}
	if options.Capitalize && displayPart != "" {
		displayPart = strings.ToUpper(displayPart[:1]) + displayPart[1:]
	}
	return displayPart
}

// FormatKeyText formats a "/"-separated key list with "+" modifiers.
func FormatKeyText(key string, options KeyTextFormatOptions) string {
	segments := strings.Split(key, "/")
	for index, segment := range segments {
		parts := strings.Split(segment, "+")
		for partIndex, part := range parts {
			parts[partIndex] = formatKeyPart(part, options)
		}
		segments[index] = strings.Join(parts, "+")
	}
	return strings.Join(segments, "/")
}

func formatKeys(keys []string, options KeyTextFormatOptions) string {
	if len(keys) == 0 {
		return ""
	}
	return FormatKeyText(strings.Join(keys, "/"), options)
}

// KeyText formats the keys bound to a keybinding.
func KeyText(keybinding string) string {
	return formatKeys(tui.GetKeybindings().GetKeys(keybinding), KeyTextFormatOptions{})
}

// KeyDisplayText formats the keys with capitalized parts.
func KeyDisplayText(keybinding string) string {
	return formatKeys(tui.GetKeybindings().GetKeys(keybinding), KeyTextFormatOptions{Capitalize: true})
}

// KeyHint renders a dim key label with a muted description.
func KeyHint(keybinding string, description string) string {
	theme := ActiveTheme()
	return theme.Fg("dim", KeyText(keybinding)) + theme.Fg("muted", " "+description)
}

// RawKeyHint renders a raw key label with a muted description.
func RawKeyHint(key string, description string) string {
	theme := ActiveTheme()
	return theme.Fg("dim", FormatKeyText(key, KeyTextFormatOptions{})) + theme.Fg("muted", " "+description)
}

// CountdownTimerHost is the render request surface the timer needs.
type CountdownTimerHost interface {
	RequestRender(force bool)
}

// CountdownTimer ticks down a dialog timeout.
type CountdownTimer struct {
	host     CountdownTimerHost
	onTick   func(seconds int)
	onExpire func()

	stop chan struct{}
}

// NewCountdownTimer creates and starts a countdown timer.
func NewCountdownTimer(timeoutMS int, host CountdownTimerHost, onTick func(seconds int), onExpire func()) *CountdownTimer {
	timer := &CountdownTimer{host: host, onTick: onTick, onExpire: onExpire}
	remainingSeconds := (timeoutMS + 999) / 1000
	if onTick != nil {
		onTick(remainingSeconds)
	}
	stop := make(chan struct{})
	timer.stop = stop
	go func() {
		ticker := newSecondTicker()
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				remainingSeconds--
				if timer.onTick != nil {
					timer.onTick(remainingSeconds)
				}
				if timer.host != nil {
					timer.host.RequestRender(false)
				}
				if remainingSeconds <= 0 {
					timer.Dispose()
					if timer.onExpire != nil {
						timer.onExpire()
					}
					return
				}
			}
		}
	}()
	return timer
}

// Dispose stops the timer.
func (t *CountdownTimer) Dispose() {
	if t.stop != nil {
		close(t.stop)
		t.stop = nil
	}
}

// DynamicBorder is a border that adjusts to the viewport width.
type DynamicBorder struct {
	Color func(str string) string
}

// NewDynamicBorder creates the border; a nil color uses the theme border color.
func NewDynamicBorder(color func(str string) string) *DynamicBorder {
	if color == nil {
		color = func(str string) string { return ActiveTheme().Fg("border", str) }
	}
	return &DynamicBorder{Color: color}
}

// Invalidate drops cached state (none).
func (b *DynamicBorder) Invalidate() {}

// Render renders the border line.
func (b *DynamicBorder) Render(width int) []string {
	return []string{b.Color(strings.Repeat("─", maxIntLocal(1, width)))}
}

// VisualTruncateResult is the truncation outcome.
type VisualTruncateResult struct {
	VisualLines  []string
	SkippedCount int
}

// TruncateToVisualLines truncates text to a maximum number of visual lines
// (from the end), accounting for wrapping.
func TruncateToVisualLines(text string, maxVisualLines int, width int, paddingX int) VisualTruncateResult {
	if text == "" {
		return VisualTruncateResult{}
	}
	tempText := tui.NewText(text, paddingX, 0, nil)
	allVisualLines := tempText.Render(width)
	if len(allVisualLines) <= maxVisualLines {
		return VisualTruncateResult{VisualLines: allVisualLines, SkippedCount: 0}
	}
	// JS's slice(-0) returns the whole array, so a zero limit keeps every
	// line while still reporting them as skipped (upstream quirk).
	start := len(allVisualLines) - maxVisualLines
	if maxVisualLines == 0 {
		start = 0
	}
	return VisualTruncateResult{
		VisualLines:  allVisualLines[start:],
		SkippedCount: len(allVisualLines) - maxVisualLines,
	}
}

func maxIntLocal(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

// secondTicker is time.NewTicker(1s) behind a small indirection so tests can
// reason about it.
func newSecondTicker() *time.Ticker { return time.NewTicker(time.Second) }

// BorderedLoader is a loader wrapped in borders.
type BorderedLoader struct {
	*tui.Container

	cancellable  bool
	loader       *tui.Loader
	cancelLoader *tui.CancellableLoader
	onAbort      func()
}

// NewBorderedLoader creates the bordered loader.
// Cancellable defaults to true.
func NewBorderedLoader(host tui.RenderRequester, theme *Theme, message string, cancellable *bool) *BorderedLoader {
	isCancellable := true
	if cancellable != nil {
		isCancellable = *cancellable
	}
	borderColor := func(s string) string { return theme.Fg("border", s) }
	loader := &BorderedLoader{
		Container:   &tui.Container{},
		cancellable: isCancellable,
	}
	loader.AddChild(NewDynamicBorder(borderColor))
	if isCancellable {
		loader.cancelLoader = tui.NewCancellableLoader(host,
			func(s string) string { return theme.Fg("accent", s) },
			func(s string) string { return theme.Fg("muted", s) },
			message)
	} else {
		loader.loader = tui.NewLoader(host,
			func(s string) string { return theme.Fg("accent", s) },
			func(s string) string { return theme.Fg("muted", s) },
			message, nil)
	}
	loader.AddChild(loader.loaderComponent())
	if isCancellable {
		loader.AddChild(tui.NewSpacer(1))
		loader.AddChild(tui.NewText(KeyHint("tui.select.cancel", "cancel"), 1, 0, nil))
	}
	loader.AddChild(tui.NewSpacer(1))
	loader.AddChild(NewDynamicBorder(borderColor))
	return loader
}

func (b *BorderedLoader) loaderComponent() tui.Component {
	if b.cancellable {
		return b.cancelLoader.Loader
	}
	return b.loader
}

// Done exposes the cancellation channel (upstream's AbortSignal).
func (b *BorderedLoader) Done() <-chan struct{} {
	if b.cancellable {
		return b.cancelLoader.Done()
	}
	return nil
}

// SetOnAbort installs the abort callback.
func (b *BorderedLoader) SetOnAbort(fn func()) {
	b.onAbort = fn
	if b.cancellable {
		b.cancelLoader.OnAbort = fn
	}
}

// HandleInput forwards input to the cancellable loader.
func (b *BorderedLoader) HandleInput(data string) {
	if b.cancellable {
		b.cancelLoader.HandleInput(data)
	}
}

// Dispose stops the loader.
func (b *BorderedLoader) Dispose() {
	if b.cancellable {
		b.cancelLoader.Dispose()
		return
	}
	b.loader.Stop()
}

// MarkdownTransformContext is the transformer context.
type MarkdownTransformContext struct {
	MessageType    string
	IsStreaming    bool
	AvailableWidth int
}

// MarkdownTransformer transforms markdown before parsing (extension surface).
type MarkdownTransformer func(markdown string, context MarkdownTransformContext) (string, bool)

// CreateMarkdownTransform builds the markdown transform for a message type.
func CreateMarkdownTransform(messageType string, isStreaming bool, transformers []MarkdownTransformer) func(markdown string, availableWidth int) string {
	return func(markdown string, availableWidth int) string {
		context := MarkdownTransformContext{
			MessageType:    messageType,
			IsStreaming:    isStreaming,
			AvailableWidth: availableWidth,
		}
		transformed := markdown
		for _, transformer := range transformers {
			func() {
				defer func() { _ = recover() }()
				value, ok := transformer(transformed, context)
				if ok {
					transformed = value
				}
			}()
		}
		return transformed
	}
}
