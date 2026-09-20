package interactive

// Port of src/modes/interactive/theme/theme-controller.ts: the settings-driven
// theme controller with terminal auto-sync.

// ThemeControllerUI is the TUI surface the controller needs.
type ThemeControllerUI interface {
	Invalidate()
	RequestRender()
	SetTerminalColorSchemeNotifications(enabled bool)
	OnTerminalColorSchemeChange(listener func(theme TerminalTheme)) (unsubscribe func())
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
	return controller
}

// RebindTUI rebinds the terminal color-scheme listener after a renderer swap.
func (c *InteractiveThemeController) RebindTUI() {
	if c.unsubscribe != nil {
		c.unsubscribe()
		c.unsubscribe = nil
	}
	c.bindTerminalColorSchemeListener()
	if c.ui != nil {
		c.ui.SetTerminalColorSchemeNotifications(c.autoSyncEnabled)
	}
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

// SetThemeSetting applies an auto/plain theme setting.
func (c *InteractiveThemeController) SetThemeSetting(themeSetting string) {
	c.currentThemeSetting = &themeSetting
	c.ApplyFromSettings()
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
	if success, _ := SetTheme(themeName, true); success {
		if c.ui != nil {
			c.ui.Invalidate()
			c.ui.RequestRender()
		}
	}
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
}

// GetTerminalTheme returns the detected terminal theme.
func (c *InteractiveThemeController) GetTerminalTheme() TerminalTheme { return c.terminalTheme }

// ActiveThemeName returns the active theme name.
func (c *InteractiveThemeController) ActiveThemeName() string { return c.activeThemeName }

func (c *InteractiveThemeController) applyThemeName(themeName string, showError bool) ThemeResult {
	success, message := SetTheme(themeName, true)
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
