package interactive

import (
	"sync/atomic"
	"time"

	"github.com/dat267/pier/internal/offloop"
)

// Port of src/modes/interactive/theme/theme-controller.ts: the settings-driven
// theme controller with terminal auto-sync.

// ThemeControllerUI is the TUI surface the controller needs.
type ThemeControllerUI interface {
	Invalidate()
	RequestRender()
	SetTerminalColorSchemeNotifications(enabled bool)
	OnTerminalColorSchemeChange(listener func(theme TerminalTheme)) (unsubscribe func())
	// OnTerminalBackgroundColorChange subscribes to OSC 11 background replies,
	// the fallback for terminals that do not report a color scheme.
	OnTerminalBackgroundColorChange(listener func(rgb RgbColor)) (unsubscribe func())
	// RequestTerminalColorScheme/BackgroundColor send the queries without
	// blocking; the replies arrive through the listeners on the owner goroutine.
	RequestTerminalColorScheme()
	RequestTerminalBackgroundColor()
}

// ThemeSettings is the settings surface the controller needs.
type ThemeSettings interface {
	GetThemeSetting() *string
	SetTheme(theme string)
	Flush()
}

// ThemeControllerOptions configure the controller.
type ThemeControllerOptions struct {
	UI                  ThemeControllerUI
	GetSettingsManager  func() ThemeSettings
	ShowError           func(message string)
	OnChanged           func()
	InitialThemeSetting *string
	// Marshal runs a function on the UI loop (the wiring's UI.Post); nil runs
	// it inline (tests, headless). The async apply paths use it to bring their
	// controller-state updates back onto the loop.
	Marshal func(func())
	// ThemeQueue, when non-nil, loads and applies named themes off the calling
	// goroutine (the internal/offloop uniform mechanism): loadTheme reads the
	// theme file from disk, which the UI loop must never do. nil (the default)
	// keeps theme application synchronous.
	ThemeQueue *offloop.Queue
	// Query detections (the terminal queries behind the auto theme).
	Detector  TerminalAutoThemeDetector
	TimeoutMS int
	Env       func(string) string
}

// ThemeResult is a theme application outcome.
type ThemeResult struct {
	Success bool
	Error   string
}

// InteractiveThemeController keeps the global theme in sync with settings and
// the terminal's color scheme.
type InteractiveThemeController struct {
	ui          ThemeControllerUI
	getSettings func() ThemeSettings
	showError   func(string)
	onChanged   func()
	detector    TerminalAutoThemeDetector
	timeoutMS   int
	env         func(string) string

	currentThemeSetting *string
	terminalTheme       TerminalTheme
	activeThemeName     string
	autoSyncEnabled     bool
	unsubscribe         func()
	backgroundUnsub     func()
	marshal             func(func())
	themeQueue          *offloop.Queue

	// schemeReportSeen records that the terminal answered a color-scheme query;
	// it wins over the OSC 11 background fallback and stops the background poll.
	schemeReportSeen atomic.Bool
	pollStop         chan struct{}

	// terminalStarted gates every terminal write. Queries written before the
	// renderer puts the pty in raw mode are echoed/buffered by the line
	// discipline (and can interleave with the Kitty negotiation); over SSH this
	// broke launching the TUI entirely. MarkTerminalStarted flushes them once
	// the terminal is up.
	terminalStarted bool
}

// NewInteractiveThemeController creates and initializes the controller.
func NewInteractiveThemeController(options ThemeControllerOptions) *InteractiveThemeController {
	controller := &InteractiveThemeController{
		ui:                  options.UI,
		getSettings:         options.GetSettingsManager,
		showError:           options.ShowError,
		onChanged:           options.OnChanged,
		detector:            options.Detector,
		timeoutMS:           options.TimeoutMS,
		env:                 options.Env,
		currentThemeSetting: options.InitialThemeSetting,
		marshal:             options.Marshal,
		themeQueue:          options.ThemeQueue,
	}
	if controller.timeoutMS == 0 {
		controller.timeoutMS = 100
	}
	controller.terminalTheme = DetectTerminalBackgroundFromEnv(controller.env).Theme

	setting := controller.currentThemeSetting
	if setting == nil && controller.getSettings != nil {
		setting = controller.getSettings().GetThemeSetting()
	}
	if name, ok := ResolveThemeSetting(setting, controller.terminalTheme); ok {
		controller.activeThemeName = name
	}
	InitTheme(controller.activeThemeName, true)
	controller.bindTerminalListeners()
	return controller
}

// RebindTUI rebinds the terminal color-scheme listener after a renderer swap.
func (c *InteractiveThemeController) RebindTUI() {
	if c.unsubscribe != nil {
		c.unsubscribe()
		c.unsubscribe = nil
	}
	if c.backgroundUnsub != nil {
		c.backgroundUnsub()
		c.backgroundUnsub = nil
	}
	c.bindTerminalListeners()
	if c.ui != nil && c.terminalStarted {
		c.ui.SetTerminalColorSchemeNotifications(c.autoSyncEnabled)
	}
	if c.autoSyncEnabled {
		c.requestTerminalTheme()
	}
}

// ApplyFromSettings applies the theme from the current settings. In the
// interactive wiring it never blocks on a terminal query: the terminal is
// asked asynchronously and its reply arrives through the color-scheme/background
// listeners (a synchronous query cannot complete on the UI loop, which is what
// dispatches the reply). The env/COLORFGBG result is applied immediately so the
// first paint is themed. Headless callers (no Marshal seam, so no loop to
// dispatch a reply) use the synchronous detector instead.
func (c *InteractiveThemeController) ApplyFromSettings() {
	themeSetting := c.currentThemeSetting
	if themeSetting == nil && c.getSettings != nil {
		themeSetting = c.getSettings().GetThemeSetting()
	}

	if light, dark, ok := ParseAutoThemeSetting(themeSetting); ok {
		c.setAutoSync(true)
		if c.marshal == nil {
			c.terminalTheme = DetectTerminalThemeForAuto(c.detector, c.timeoutMS, c.env)
		} else {
			c.terminalTheme = DetectTerminalBackgroundFromEnv(c.env).Theme
		}
		name := dark
		if c.terminalTheme == TerminalThemeLight {
			name = light
		}
		c.applyThemeName(name, true)
		return
	}

	c.setAutoSync(false)
	if themeSetting != nil {
		c.applyThemeName(*themeSetting, true)
		return
	}

	detection := DetectTerminalBackgroundFromEnv(c.env)
	if c.marshal == nil {
		detection = DetectTerminalBackgroundTheme(c.detector, c.timeoutMS, c.env)
	}
	c.terminalTheme = detection.Theme
	if !c.applyThemeName(string(detection.Theme), false).Success {
		return
	}
	if detection.Confidence == "high" && c.getSettings != nil {
		settings := c.getSettings()
		settings.SetTheme(string(detection.Theme))
		settings.Flush()
	}
	// Even with a fixed env result, ask the terminal to refine the choice.
	c.requestTerminalTheme()
}

// GetThemeSelection returns the active theme selection.
func (c *InteractiveThemeController) GetThemeSelection() string {
	if c.currentThemeSetting != nil {
		return *c.currentThemeSetting
	}
	if c.getSettings != nil {
		if setting := c.getSettings().GetThemeSetting(); setting != nil {
			return *setting
		}
	}
	return c.activeThemeName
}

// SetThemeName applies a theme name.
func (c *InteractiveThemeController) SetThemeName(themeName string, showError bool) ThemeResult {
	c.setAutoSync(false)
	result := c.applyThemeName(themeName, showError)
	if result.Success {
		c.currentThemeSetting = &themeName
	}
	return result
}

// SetThemeSetting applies an auto/plain theme setting. The auto path stays
// synchronous (terminal detection is interactive by design); a named theme
// loads and applies on the theme queue because loadTheme reads the theme file
// from disk, and the selector callbacks that reach here run on the UI loop.
func (c *InteractiveThemeController) SetThemeSetting(themeSetting string) {
	c.currentThemeSetting = &themeSetting
	if _, _, ok := ParseAutoThemeSetting(&themeSetting); ok {
		c.ApplyFromSettings()
		return
	}
	c.applyThemeNameAsync(themeSetting)
}

// applyThemeNameAsync is applyThemeName off the loop: SetTheme loads the theme
// file and swaps the global atomics on the worker (its notifyThemeChange
// callback already marshals the UI invalidation onto the loop), and the
// controller's own state update marshals back through the wiring's Marshal
// seam. Ordered submissions make the last switch win.
func (c *InteractiveThemeController) applyThemeNameAsync(themeName string) {
	if c.themeQueue == nil {
		c.applyThemeNameSync(themeName)
		return
	}
	c.themeQueue.Go(func() {
		success, message := SetTheme(themeName, true)
		c.onUI(func() {
			if success {
				c.activeThemeName = themeName
			} else {
				c.activeThemeName = "dark"
			}
			c.notifyChanged()
			if !success && c.showError != nil {
				c.showError("Failed to load theme \"" + themeName + "\": " + message + "\nFell back to dark theme.")
			}
		})
	})
}

// onUI runs fn on the UI loop when a Marshal seam is wired, inline otherwise.
func (c *InteractiveThemeController) onUI(fn func()) {
	if c.marshal != nil {
		c.marshal(fn)
		return
	}
	fn()
}

// SetThemeInstance installs an in-memory theme.
func (c *InteractiveThemeController) SetThemeInstance(themeInstance *Theme) ThemeResult {
	c.setAutoSync(false)
	SetThemeInstance(themeInstance)
	c.activeThemeName = "<in-memory>"
	c.notifyChanged()
	return ThemeResult{Success: true}
}

// Preview applies a theme without persisting the setting.
func (c *InteractiveThemeController) Preview(themeSettingOrName string) {
	themeName, ok := ResolveThemeSetting(&themeSettingOrName, c.terminalTheme)
	if !ok {
		themeName = c.activeThemeName
	}
	if themeName == "" {
		return
	}
	if c.themeQueue == nil {
		if success, _ := SetTheme(themeName, true); success {
			if c.ui != nil {
				c.ui.Invalidate()
				c.ui.RequestRender()
			}
		}
		return
	}
	// Preview loads run on the theme queue like real switches; ordered
	// submissions make the last previewed theme win.
	c.themeQueue.Go(func() {
		success, _ := SetTheme(themeName, true)
		if success {
			c.onUI(func() {
				if c.ui != nil {
					c.ui.Invalidate()
					c.ui.RequestRender()
				}
			})
		}
	})
}

// DisableAutoSync turns off terminal color-scheme syncing.
func (c *InteractiveThemeController) DisableAutoSync() { c.setAutoSync(false) }

// Dispose tears the controller down.
func (c *InteractiveThemeController) Dispose() {
	c.setAutoSync(false)
	if c.unsubscribe != nil {
		c.unsubscribe()
		c.unsubscribe = nil
	}
	if c.backgroundUnsub != nil {
		c.backgroundUnsub()
		c.backgroundUnsub = nil
	}
}

// GetTerminalTheme returns the detected terminal theme.
func (c *InteractiveThemeController) GetTerminalTheme() TerminalTheme { return c.terminalTheme }

// ActiveThemeName returns the active theme name.
func (c *InteractiveThemeController) ActiveThemeName() string { return c.activeThemeName }

func (c *InteractiveThemeController) applyThemeName(themeName string, showError bool) ThemeResult {
	success, message := SetTheme(themeName, true)
	return c.recordThemeApply(themeName, success, message, showError)
}

// applyThemeNameSync is applyThemeName for the synchronous (nil-queue) mode;
// kept separate so the async path cannot accidentally call back into it.
func (c *InteractiveThemeController) applyThemeNameSync(themeName string) ThemeResult {
	success, message := SetTheme(themeName, true)
	return c.recordThemeApply(themeName, success, message, true)
}

// recordThemeApply updates the controller state after a theme application and
// reports the outcome; the caller arranges the goroutine (loop or worker).
func (c *InteractiveThemeController) recordThemeApply(themeName string, success bool, message string, showError bool) ThemeResult {
	if success {
		c.activeThemeName = themeName
	} else {
		c.activeThemeName = "dark"
	}
	c.notifyChanged()
	if !success && showError && c.showError != nil {
		c.showError("Failed to load theme \"" + themeName + "\": " + message + "\nFell back to dark theme.")
	}
	return ThemeResult{Success: success, Error: message}
}

func (c *InteractiveThemeController) notifyChanged() {
	if c.ui != nil {
		c.ui.Invalidate()
	}
	if c.onChanged != nil {
		c.onChanged()
	}
}

func (c *InteractiveThemeController) setAutoSync(enabled bool) {
	if c.autoSyncEnabled == enabled {
		return
	}
	c.autoSyncEnabled = enabled
	if !c.terminalStarted {
		return
	}
	if c.ui != nil {
		c.ui.SetTerminalColorSchemeNotifications(enabled)
	}
	if enabled {
		c.requestTerminalTheme()
		c.startBackgroundPoll()
		return
	}
	c.stopBackgroundPoll()
}

// MarkTerminalStarted is called by the owner once the renderer's terminal has
// entered raw mode. It flushes the auto-sync state and sends the initial
// detection requests, which must not be written before the pty is raw.
func (c *InteractiveThemeController) MarkTerminalStarted() {
	if c.terminalStarted {
		return
	}
	c.terminalStarted = true
	start := func() {
		if c.ui != nil {
			c.ui.SetTerminalColorSchemeNotifications(c.autoSyncEnabled)
		}
		c.requestTerminalTheme()
		if c.autoSyncEnabled {
			c.startBackgroundPoll()
		}
	}
	// Let the first frame paint before the terminal is asked anything: a theme
	// change can rebuild the transcript, and that must never race the initial
	// render (writing the queries earlier broke launching the TUI).
	if c.marshal != nil {
		time.AfterFunc(150*time.Millisecond, func() { c.onUI(start) })
		return
	}
	start()
}

// requestTerminalTheme asks the terminal for its scheme and background without
// blocking; the replies reach the listeners on the owner goroutine. D165: the
// port cannot query synchronously on the UI loop, which is what dispatches the
// reply, so it requests and listens instead of awaiting a result.
func (c *InteractiveThemeController) requestTerminalTheme() {
	if c.ui == nil || !c.terminalStarted {
		return
	}
	// A fresh request cycle: until a scheme reply arrives, the background
	// fallback (and its poll) is authoritative.
	c.schemeReportSeen.Store(false)
	c.ui.RequestTerminalColorScheme()
	c.ui.RequestTerminalBackgroundColor()
}

// startBackgroundPoll re-requests the background while the terminal never
// answered a color-scheme query, so a profile switch still gets noticed on
// terminals without OSC 2031. It runs off the loop and marshals the request.
func (c *InteractiveThemeController) startBackgroundPoll() {
	if c.ui == nil || c.marshal == nil || c.pollStop != nil {
		return
	}
	stop := make(chan struct{})
	c.pollStop = stop
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if c.schemeReportSeen.Load() {
					return
				}
				c.onUI(func() {
					if c.ui != nil {
						c.ui.RequestTerminalBackgroundColor()
					}
				})
			}
		}
	}()
}

func (c *InteractiveThemeController) stopBackgroundPoll() {
	if c.pollStop != nil {
		close(c.pollStop)
		c.pollStop = nil
	}
}

func (c *InteractiveThemeController) bindTerminalListeners() {
	if c.ui == nil {
		return
	}
	c.unsubscribe = c.ui.OnTerminalColorSchemeChange(func(terminalTheme TerminalTheme) {
		c.schemeReportSeen.Store(true)
		c.safeThemeChange(func() { c.applyTerminalTheme(terminalTheme) })
	})
	c.backgroundUnsub = c.ui.OnTerminalBackgroundColorChange(func(rgb RgbColor) {
		if c.schemeReportSeen.Load() {
			return
		}
		c.safeThemeChange(func() { c.applyTerminalTheme(GetThemeForRgbColor(rgb)) })
	})
}

// safeThemeChange applies a theme change defensively: a failure here must never
// take the TUI down, so a panic disables auto-sync and leaves the running theme.
func (c *InteractiveThemeController) safeThemeChange(fn func()) {
	defer func() {
		if recover() != nil {
			c.setAutoSync(false)
		}
	}()
	fn()
}

// applyTerminalTheme applies a detected light/dark classification. With an
// auto setting it switches the pair; with no setting it adopts and persists the
// detected theme; with a fixed setting it is ignored.
func (c *InteractiveThemeController) applyTerminalTheme(terminalTheme TerminalTheme) {
	setting := c.currentThemeSetting
	if setting == nil && c.getSettings != nil {
		setting = c.getSettings().GetThemeSetting()
	}
	if light, dark, ok := ParseAutoThemeSetting(setting); ok {
		if !c.autoSyncEnabled {
			return
		}
		c.terminalTheme = terminalTheme
		themeName := dark
		if terminalTheme == TerminalThemeLight {
			themeName = light
		}
		if themeName != c.activeThemeName {
			c.applyThemeName(themeName, false)
		}
		return
	}
	if setting != nil {
		return
	}
	if c.activeThemeName == string(terminalTheme) && c.terminalTheme == terminalTheme {
		return
	}
	c.terminalTheme = terminalTheme
	c.applyThemeName(string(terminalTheme), false)
	if c.getSettings != nil {
		settings := c.getSettings()
		settings.SetTheme(string(terminalTheme))
		settings.Flush()
	}
}

// ThemeQueueFlushForTest drains the theme queue; exported for tests because
// the queue is wired by the app, not the caller.
func (c *InteractiveThemeController) ThemeQueueFlushForTest() {
	if c.themeQueue != nil {
		c.themeQueue.Flush()
	}
}
