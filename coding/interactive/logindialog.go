package interactive

import (
	"runtime"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/tui"
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
	post             func(func())
	onComplete       func(success bool, message string)

	aborted chan struct{}
	pending chan LoginInputResult
	// selectList is the active select prompt's list; the dialog owns input
	// routing, so a select needs no focus switch.
	selectList *tui.SelectList
	focused    bool
}

// NewLoginDialogComponent creates the dialog. post marshals mutations from the
// login flow's goroutine onto the UI loop; a nil post applies them inline, for
// callers that drive the dialog from the loop themselves.
func NewLoginDialogComponent(host tui.RenderRequester, post func(func()), providerID string, onComplete func(success bool, message string), providerNameOverride string, titleOverride string) *LoginDialogComponent {
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
		post:       post,
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

// runOnUI marshals a mutation onto the UI loop. The login flow runs on its own
// goroutine (it waits on the dialog for input, so running it on the loop would
// deadlock), which means every touch of the dialog tree — and of the fields the
// input handlers read, like pending and selectList — has to happen on the loop
// that renders the dialog and routes its keys.
func (c *LoginDialogComponent) runOnUI(fn func()) {
	if c.post != nil {
		c.post(fn)
		return
	}
	fn()
}

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
	// Opening the browser stays on the flow's goroutine: exec can block, and the
	// loop has to stay free to deliver input.
	browserOpener(url)
	c.runOnUI(func() {
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
		c.requestRender()
	})
}

// ShowDeviceCode shows the device-code verification URL and user code.
func (c *LoginDialogComponent) ShowDeviceCode(verificationURI string, userCode string) {
	c.runOnUI(func() {
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
	})
}

// ShowManualInput shows the manual code/URL entry prompt.
func (c *LoginDialogComponent) ShowManualInput(prompt string) <-chan LoginInputResult {
	results := make(chan LoginInputResult, 1)
	c.runOnUI(func() {
		theme := ActiveTheme()
		c.input.SetValue("")
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.Fg("dim", prompt), 1, 0, nil))
		c.contentContainer.AddChild(c.input)
		c.contentContainer.AddChild(tui.NewText("("+KeyHint("tui.select.cancel", "to cancel")+")", 1, 0, nil))
		c.requestRender()
		c.pending = results
	})
	return results
}

// ShowPrompt shows a prompt and waits for input (content is appended, not
// cleared, so the authorization URL stays visible).
func (c *LoginDialogComponent) ShowPrompt(message string, placeholder string) <-chan LoginInputResult {
	results := make(chan LoginInputResult, 1)
	c.runOnUI(func() {
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
		c.pending = results
	})
	return results
}

// ShowSelect shows the prompt's options as a list and returns the chosen
// option's id (upstream's ui.select step: the Bedrock auth-method and profile
// prompts go through this), or an error when the login is cancelled.
func (c *LoginDialogComponent) ShowSelect(message string, options []ai.AuthSelectOption) <-chan LoginInputResult {
	items := make([]tui.SelectItem, 0, len(options))
	for _, option := range options {
		items = append(items, tui.SelectItem{
			Value: option.ID, Label: option.Label, Description: option.Description,
		})
	}
	visible := len(items)
	if visible > loginSelectVisibleRows {
		visible = loginSelectVisibleRows
	}
	if visible < 1 {
		visible = 1
	}
	results := make(chan LoginInputResult, 1)
	c.runOnUI(func() {
		theme := ActiveTheme()
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.Fg("text", message), 1, 0, nil))

		list := tui.NewSelectList(items, visible, GetSelectListTheme(), loginSelectLayout)
		list.OnSelect = func(item tui.SelectItem) {
			c.selectList = nil
			select {
			case results <- LoginInputResult{Value: item.Value}:
			default:
			}
		}
		c.selectList = list
		c.contentContainer.AddChild(list)
		c.contentContainer.AddChild(tui.NewText(
			"("+KeyHint("tui.select.cancel", "to cancel,")+" "+KeyHint("tui.select.confirm", "to select")+")", 1, 0, nil))
		c.requestRender()
	})
	return results
}

// loginSelectLayout bounds the option column so a long description cannot push
// the labels off screen.
var loginSelectLayout = tui.SelectListLayoutOptions{
	MinPrimaryColumnWidth: 20,
	HasMin:                true,
	MaxPrimaryColumnWidth: 48,
	HasMax:                true,
}

// loginSelectVisibleRows is how many options the login list shows at once.
const loginSelectVisibleRows = 8

// ShowDetails replaces the content with informational lines.
func (c *LoginDialogComponent) ShowDetails(lines []string) {
	c.runOnUI(func() {
		c.contentContainer.Clear()
		c.contentContainer.AddChild(tui.NewSpacer(1))
		for _, line := range lines {
			c.contentContainer.AddChild(tui.NewText(line, 1, 0, nil))
		}
		c.requestRender()
	})
}

// ShowInfo appends provider information and links.
func (c *LoginDialogComponent) ShowInfo(message string, links []ai.AuthInfoLink, showCloseHint bool) {
	c.runOnUI(func() {
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
	})
}

// ShowWaiting appends a waiting message.
func (c *LoginDialogComponent) ShowWaiting(message string) {
	c.runOnUI(func() {
		theme := ActiveTheme()
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.Fg("dim", message), 1, 0, nil))
		c.contentContainer.AddChild(tui.NewText("("+KeyHint("tui.select.cancel", "to cancel")+")", 1, 0, nil))
		c.requestRender()
	})
}

// ShowProgress appends a progress message.
func (c *LoginDialogComponent) ShowProgress(message string) {
	c.runOnUI(func() {
		c.contentContainer.AddChild(tui.NewText(ActiveTheme().Fg("dim", message), 1, 0, nil))
		c.requestRender()
	})
}

// HandleInput processes input.
func (c *LoginDialogComponent) HandleInput(data string) {
	kb := tui.GetKeybindings()
	if kb.Matches(data, "tui.select.cancel") {
		c.cancel()
		return
	}
	if c.selectList != nil {
		c.selectList.HandleInput(data)
		c.requestRender()
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
