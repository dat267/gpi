package interactive

import (
	"github.com/dat267/pier/internal/offloop"
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/theme/theme-controller.ts: the settings-driven
// theme controller with terminal auto-sync.

// ThemeControllerUI is the TUI surface the controller needs.
type ThemeControllerUI interface {
	Invalidate()
	RequestRender()
	SetTerminalColorSchemeNotifications(enabled bool)
	OnTerminalColorSchemeChange(listener func(theme TerminalTheme)) (unsubscribe func())
	OnTerminalBackgroundColorChange(listener func(color tui.RgbColor)) (unsubscribe func())
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
	unsubscribeBg       func()
	probeStarted        bool
	marshal             func(func())
	themeQueue          *offloop.Queue
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
	controller.bindTerminalColorSchemeListener()
	controller.bindTerminalBackgroundListener()
	return controller
}

// RebindTUI rebinds the terminal color-scheme listener after a renderer swap.
func (c *InteractiveThemeController) RebindTUI() {
	if c.unsubscribe != nil {
		c.unsubscribe()
		c.unsubscribe = nil
	}
	if c.unsubscribeBg != nil {
		c.unsubscribeBg()
		c.unsubscribeBg = nil
	}
	c.probeStarted = false
	c.bindTerminalColorSchemeListener()
	c.bindTerminalBackgroundListener()
	if c.ui != nil {
		c.ui.SetTerminalColorSchemeNotifications(c.autoSyncEnabled)
	}
	// The new renderer never received the OSC 11 probe; re-request it (the
	// reply listener is rebound above).
	c.ProbeTerminalBackground()
}

// ProbeTerminalBackground writes one OSC 11 query and returns. The reply is
// applied by the background listener on the UI loop; a terminal that does not
// answer simply leaves the environment fallback in place. Callers must invoke
// it only after the terminal is in raw mode and the input reader is live: the
// reply is delivered through the ordinary input path, and a query written before
// raw mode is echoed back into the input stream.
func (c *InteractiveThemeController) ProbeTerminalBackground() {
	if c.ui == nil || c.probeStarted || !c.autoSyncEnabled {
		return
	}
	c.probeStarted = true
	c.ui.RequestTerminalBackgroundColor()
}

// ApplyFromSettings applies the theme from the current settings.
func (c *InteractiveThemeController) ApplyFromSettings() {
	themeSetting := c.currentThemeSetting
	if themeSetting == nil && c.getSettings != nil {
		themeSetting = c.getSettings().GetThemeSetting()
	}

	if light, dark, ok := ParseAutoThemeSetting(themeSetting); ok {
		c.terminalTheme = DetectTerminalThemeForAuto(c.detector, c.timeoutMS, c.env)
		c.setAutoSync(true)
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

	detection := DetectTerminalBackgroundTheme(c.detector, c.timeoutMS, c.env)
	c.terminalTheme = detection.Theme
	if !c.applyThemeName(string(detection.Theme), false).Success {
		return
	}
	if detection.Confidence == "high" && c.getSettings != nil {
		settings := c.getSettings()
		settings.SetTheme(string(detection.Theme))
		settings.Flush()
	}
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
	if c.unsubscribeBg != nil {
		c.unsubscribeBg()
		c.unsubscribeBg = nil
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
	if c.ui != nil {
		c.ui.SetTerminalColorSchemeNotifications(enabled)
	}
}

func (c *InteractiveThemeController) bindTerminalColorSchemeListener() {
	if c.ui == nil {
		return
	}
	c.unsubscribe = c.ui.OnTerminalColorSchemeChange(func(terminalTheme TerminalTheme) {
		c.applyTerminalTheme(terminalTheme)
	})
}

// bindTerminalBackgroundListener applies an OSC 11 reply on the UI loop. The
// reply is converted to a light/dark preference by luminance.
func (c *InteractiveThemeController) bindTerminalBackgroundListener() {
	if c.ui == nil {
		return
	}
	c.unsubscribeBg = c.ui.OnTerminalBackgroundColorChange(func(color tui.RgbColor) {
		c.applyTerminalTheme(GetThemeForRgbColor(RgbColor{R: color.R, G: color.G, B: color.B}))
	})
}

func (c *InteractiveThemeController) applyTerminalTheme(terminalTheme TerminalTheme) {
	if !c.autoSyncEnabled {
		return
	}
	c.terminalTheme = terminalTheme
	setting := c.currentThemeSetting
	if setting == nil && c.getSettings != nil {
		setting = c.getSettings().GetThemeSetting()
	}
	light, dark, ok := ParseAutoThemeSetting(setting)
	if !ok {
		c.setAutoSync(false)
		return
	}
	themeName := dark
	if terminalTheme == TerminalThemeLight {
		name := light
		themeName = name
	}
	if themeName != c.activeThemeName {
		c.applyThemeName(themeName, false)
	}
}

// ThemeQueueFlushForTest drains the theme queue; exported for tests because
// the queue is wired by the app, not the caller.
func (c *InteractiveThemeController) ThemeQueueFlushForTest() {
	if c.themeQueue != nil {
		c.themeQueue.Flush()
	}
}
