package interactive

import (
	"runtime"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/tui"
)

// Port of src/modes/interactive/components/login-dialog.ts: the login dialog
// that replaces the editor during the OAuth login flow.
//
// Divergences: the promise-returning prompts become result channels (D91) and
// the browser opener is injected because the native helper is out of scope
// (D92).

// OpenBrowserFn opens a URL in the default browser.
type OpenBrowserFn func(url string)

var browserOpener OpenBrowserFn = func(string) {}

// SetBrowserOpener installs the browser opener (D92).
func SetBrowserOpener(opener OpenBrowserFn) {
	if opener == nil {
		opener = func(string) {}
	}
	browserOpener = opener
}

// LoginInputResult is a login prompt outcome.
type LoginInputResult struct {
	Value string
	Err   error
}

// LoginDialogComponent is the login dialog.
type LoginDialogComponent struct {
	*tui.Container

	contentContainer *tui.Container
	input            *tui.Input
	host             tui.RenderRequester
	onComplete       func(success bool, message string)

	aborted chan struct{}
	pending chan LoginInputResult
	focused bool
}

// NewLoginDialogComponent creates the dialog.
func NewLoginDialogComponent(host tui.RenderRequester, providerID string, onComplete func(success bool, message string), providerNameOverride string, titleOverride string) *LoginDialogComponent {
	theme := ActiveTheme()
	providerName := providerID
	if providerNameOverride != "" {
		providerName = providerNameOverride
	}
	title := "Login to " + providerName
	if titleOverride != "" {
		title = titleOverride
	}

	component := &LoginDialogComponent{
		Container:  &tui.Container{},
		host:       host,
		onComplete: onComplete,
		aborted:    make(chan struct{}),
	}
	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewText(theme.Fg("accent", theme.Bold(title)), 1, 0, nil))
	component.contentContainer = &tui.Container{}
	component.AddChild(component.contentContainer)

	component.input = tui.NewInput(tui.InputOptions{})
	component.input.OnSubmit = func(value string) {
		if component.pending != nil {
			component.replaceInputWithSubmittedText(value)
			component.pending <- LoginInputResult{Value: value}
			component.pending = nil
		}
	}
	component.input.OnEscape = func() { component.cancel() }
	component.AddChild(NewDynamicBorder(nil))
	return component
}

// Aborted is closed when the login is cancelled (upstream's AbortSignal).
func (c *LoginDialogComponent) Aborted() <-chan struct{} { return c.aborted }

func (c *LoginDialogComponent) replaceInputWithSubmittedText(value string) {
	replaced := make([]tui.Component, 0, len(c.contentContainer.Children))
	for _, child := range c.contentContainer.Children {
		if child == tui.Component(c.input) {
			replaced = append(replaced, tui.NewText("> "+value, 0, 0, nil))
			continue
		}
		replaced = append(replaced, child)
	}
	c.contentContainer.Children = replaced
}

func (c *LoginDialogComponent) cancel() {
	select {
	case <-c.aborted:
	default:
		close(c.aborted)
	}
	if c.pending != nil {
		c.pending <- LoginInputResult{Err: errLoginCancelled{}}
		c.pending = nil
	}
	if c.onComplete != nil {
		c.onComplete(false, "Login cancelled")
	}
}

type errLoginCancelled struct{}

func (errLoginCancelled) Error() string { return "Login cancelled" }

// ShowAuth shows the authorization URL and instructions.
func (c *LoginDialogComponent) ShowAuth(url string, instructions string) {
	theme := ActiveTheme()
	c.contentContainer.Clear()
	c.contentContainer.AddChild(tui.NewSpacer(1))
	linkedURL := "\x1b]8;;" + url + "\x07" + url + "\x1b]8;;\x07"
	c.contentContainer.AddChild(tui.NewText(theme.Fg("accent", linkedURL), 1, 0, nil))

	clickHint := "Ctrl+click to open"
	if runtime.GOOS == "darwin" {
		clickHint = "Cmd+click to open"
	}
	hyperlink := "\x1b]8;;" + url + "\x07" + clickHint + "\x1b]8;;\x07"
	c.contentContainer.AddChild(tui.NewText(theme.Fg("dim", hyperlink), 1, 0, nil))

	if instructions != "" {
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.Fg("warning", instructions), 1, 0, nil))
	}
	browserOpener(url)
	c.requestRender()
}

// ShowDeviceCode shows the device-code verification URL and user code.
func (c *LoginDialogComponent) ShowDeviceCode(verificationURI string, userCode string) {
	theme := ActiveTheme()
	c.contentContainer.Clear()
	c.contentContainer.AddChild(tui.NewSpacer(1))
	linkedURL := "\x1b]8;;" + verificationURI + "\x07" + verificationURI + "\x1b]8;;\x07"
	c.contentContainer.AddChild(tui.NewText(theme.Fg("accent", linkedURL), 1, 0, nil))

	clickHint := "Ctrl+click to open"
	if runtime.GOOS == "darwin" {
		clickHint = "Cmd+click to open"
	}
	hyperlink := "\x1b]8;;" + verificationURI + "\x07" + clickHint + "\x1b]8;;\x07"
	c.contentContainer.AddChild(tui.NewText(theme.Fg("dim", hyperlink), 1, 0, nil))
	c.contentContainer.AddChild(tui.NewSpacer(1))
	c.contentContainer.AddChild(tui.NewText(theme.Fg("warning", "Enter code: "+userCode), 1, 0, nil))
	c.requestRender()
}

// ShowManualInput shows the manual code/URL entry prompt.
func (c *LoginDialogComponent) ShowManualInput(prompt string) <-chan LoginInputResult {
	theme := ActiveTheme()
	c.input.SetValue("")
	c.contentContainer.AddChild(tui.NewSpacer(1))
	c.contentContainer.AddChild(tui.NewText(theme.Fg("dim", prompt), 1, 0, nil))
	c.contentContainer.AddChild(c.input)
	c.contentContainer.AddChild(tui.NewText("("+KeyHint("tui.select.cancel", "to cancel")+")", 1, 0, nil))
	c.requestRender()

	results := make(chan LoginInputResult, 1)
	c.pending = results
	return results
}

// ShowPrompt shows a prompt and waits for input (content is appended, not
// cleared, so the authorization URL stays visible).
func (c *LoginDialogComponent) ShowPrompt(message string, placeholder string) <-chan LoginInputResult {
	theme := ActiveTheme()
	c.contentContainer.AddChild(tui.NewSpacer(1))
	c.contentContainer.AddChild(tui.NewText(theme.Fg("text", message), 1, 0, nil))
	if placeholder != "" {
		c.contentContainer.AddChild(tui.NewText(theme.Fg("dim", "e.g., "+placeholder), 1, 0, nil))
	}
	c.contentContainer.AddChild(c.input)
	c.contentContainer.AddChild(tui.NewText(
		"("+KeyHint("tui.select.cancel", "to cancel,")+" "+KeyHint("tui.select.confirm", "to submit")+")", 1, 0, nil))
	c.input.SetValue("")
	c.requestRender()

	results := make(chan LoginInputResult, 1)
	c.pending = results
	return results
}

// ShowDetails replaces the content with informational lines.
func (c *LoginDialogComponent) ShowDetails(lines []string) {
	c.contentContainer.Clear()
	c.contentContainer.AddChild(tui.NewSpacer(1))
	for _, line := range lines {
		c.contentContainer.AddChild(tui.NewText(line, 1, 0, nil))
	}
	c.requestRender()
}

// ShowInfo appends provider information and links.
func (c *LoginDialogComponent) ShowInfo(message string, links []ai.AuthInfoLink, showCloseHint bool) {
	theme := ActiveTheme()
	c.contentContainer.AddChild(tui.NewSpacer(1))
	c.contentContainer.AddChild(tui.NewText(theme.Fg("text", message), 1, 0, nil))
	for _, link := range links {
		text := link.URL
		if link.Label != "" {
			text = link.Label + ": " + link.URL
		}
		hyperlink := "\x1b]8;;" + link.URL + "\x07" + text + "\x1b]8;;\x07"
		c.contentContainer.AddChild(tui.NewText(theme.Fg("accent", hyperlink), 1, 0, nil))
	}
	if showCloseHint {
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText("("+KeyHint("tui.select.cancel", "to close")+")", 1, 0, nil))
	}
	c.requestRender()
}

// ShowWaiting appends a waiting message.
func (c *LoginDialogComponent) ShowWaiting(message string) {
	theme := ActiveTheme()
	c.contentContainer.AddChild(tui.NewSpacer(1))
	c.contentContainer.AddChild(tui.NewText(theme.Fg("dim", message), 1, 0, nil))
	c.contentContainer.AddChild(tui.NewText("("+KeyHint("tui.select.cancel", "to cancel")+")", 1, 0, nil))
	c.requestRender()
}

// ShowProgress appends a progress message.
func (c *LoginDialogComponent) ShowProgress(message string) {
	c.contentContainer.AddChild(tui.NewText(ActiveTheme().Fg("dim", message), 1, 0, nil))
	c.requestRender()
}

// HandleInput processes input.
func (c *LoginDialogComponent) HandleInput(data string) {
	kb := tui.GetKeybindings()
	if kb.Matches(data, "tui.select.cancel") {
		c.cancel()
		return
	}
	c.input.HandleInput(data)
}

// Complete reports the login outcome.
func (c *LoginDialogComponent) Complete(success bool, message string) {
	if c.onComplete != nil {
		c.onComplete(success, message)
	}
}

// SetFocused implements Focusable.
func (c *LoginDialogComponent) SetFocused(focused bool) {
	c.focused = focused
	c.input.SetFocused(focused)
}

// IsFocused implements Focusable.
func (c *LoginDialogComponent) IsFocused() bool { return c.focused }

func (c *LoginDialogComponent) requestRender() {
	if c.host != nil {
		c.host.RequestRender(false)
	}
}

// ContentLines renders the content container (test helper).
func (c *LoginDialogComponent) ContentLines(width int) []string {
	return c.contentContainer.Render(width)
}

var (
	_ tui.Component = (*LoginDialogComponent)(nil)
	_ tui.Focusable = (*LoginDialogComponent)(nil)
)
