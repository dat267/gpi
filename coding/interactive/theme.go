package interactive

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dat267/gpi/tui"
)

// Port of src/modes/interactive/theme/theme.ts: the color palettes, the Theme
// type and its styling helpers, theme loading (built-in + custom), the
// terminal light/dark detection helpers, and the global theme registry.
//
// Divergences: chalk's style helpers become the equivalent ANSI sequences
// (verified against chalk output); the syntax highlighter (highlight.js) is
// out of scope, so HighlightCode stays unset and the markdown renderer uses
// its per-line fallback (D74); the file watcher polls instead of using
// fs.watch (D75).

//go:embed themes/*.json
var builtinThemeFS embed.FS

// ColorMode is the terminal color capability.
type ColorMode string

const (
	ColorModeTruecolor ColorMode = "truecolor"
	ColorMode256       ColorMode = "256color"
)

// ThemeColor names a foreground color token.
type ThemeColor = string

// ThemeBg names a background color token.
type ThemeBg = string

// Theme is a resolved color palette.
type Theme struct {
	Name       string
	SourcePath string
	SourceInfo any

	mode     ColorMode
	fgColors map[string]string
	bgColors map[string]string
}

// NewTheme resolves the palettes into ANSI sequences.
func NewTheme(fgColors map[string]ColorValue, bgColors map[string]ColorValue, mode ColorMode, name string, sourcePath string) *Theme {
	theme := &Theme{
		Name:       name,
		SourcePath: sourcePath,
		mode:       mode,
		fgColors:   map[string]string{},
		bgColors:   map[string]string{},
	}

	colors := withThemeColorFallbacks(fgColors)
	for key, value := range colors {
		theme.fgColors[key] = fgAnsi(value, mode)
	}
	backgrounds := withBackgroundFallbacks(bgColors)
	for key, value := range backgrounds {
		theme.bgColors[key] = bgAnsi(value, mode)
	}
	return theme
}

// Fg styles text with a foreground color.
func (t *Theme) Fg(color ThemeColor, text string) string {
	ansi, ok := t.fgColors[color]
	if !ok {
		panic("Unknown theme color: " + color)
	}
	return ansi + text + "\x1b[39m"
}

// Bg styles text with a background color.
func (t *Theme) Bg(color ThemeBg, text string) string {
	ansi, ok := t.bgColors[color]
	if !ok {
		panic("Unknown theme background color: " + color)
	}
	return ansi + text + "\x1b[49m"
}

// styleColorsEnabled mirrors chalk's color-support detection: when disabled
// the style helpers return plain text (divergence D86: a package switch
// instead of chalk's NO_COLOR/FORCE_COLOR environment handling).
var styleColorsState struct {
	mu      sync.Mutex
	enabled bool
	set     bool
}

// SetStyleColorsEnabled toggles the chalk-equivalent style helpers.
func SetStyleColorsEnabled(enabled bool) {
	styleColorsState.mu.Lock()
	defer styleColorsState.mu.Unlock()
	styleColorsState.enabled = enabled
	styleColorsState.set = true
}

func styleColorsEnabled() bool {
	styleColorsState.mu.Lock()
	defer styleColorsState.mu.Unlock()
	if !styleColorsState.set {
		return true
	}
	return styleColorsState.enabled
}

// Bold applies bold (chalk.bold).
func (t *Theme) Bold(text string) string {
	if !styleColorsEnabled() {
		return text
	}
	return "\x1b[1m" + text + "\x1b[22m"
}

// Italic applies italic (chalk.italic).
func (t *Theme) Italic(text string) string {
	if !styleColorsEnabled() {
		return text
	}
	return "\x1b[3m" + text + "\x1b[23m"
}

// Underline applies underline (chalk.underline).
func (t *Theme) Underline(text string) string {
	if !styleColorsEnabled() {
		return text
	}
	return "\x1b[4m" + text + "\x1b[24m"
}

// Inverse applies inverse video (chalk.inverse).
func (t *Theme) Inverse(text string) string {
	if !styleColorsEnabled() {
		return text
	}
	return "\x1b[7m" + text + "\x1b[27m"
}

// Strikethrough applies strikethrough (chalk.strikethrough).
func (t *Theme) Strikethrough(text string) string {
	if !styleColorsEnabled() {
		return text
	}
	return "\x1b[9m" + text + "\x1b[29m"
}

// GetFgAnsi returns the raw foreground sequence.
func (t *Theme) GetFgAnsi(color ThemeColor) string {
	ansi, ok := t.fgColors[color]
	if !ok {
		panic("Unknown theme color: " + color)
	}
	return ansi
}

// GetBgAnsi returns the raw background sequence.
func (t *Theme) GetBgAnsi(color ThemeBg) string {
	ansi, ok := t.bgColors[color]
	if !ok {
		panic("Unknown theme background color: " + color)
	}
	return ansi
}

// ColorMode returns the palette mode.
func (t *Theme) ColorMode() ColorMode { return t.mode }

// GetThinkingBorderColor maps a thinking level to a border style.
func (t *Theme) GetThinkingBorderColor(level string) func(string) string {
	color := "thinkingOff"
	switch level {
	case "off":
		color = "thinkingOff"
	case "minimal":
		color = "thinkingMinimal"
	case "low":
		color = "thinkingLow"
	case "medium":
		color = "thinkingMedium"
	case "high":
		color = "thinkingHigh"
	case "xhigh":
		color = "thinkingXhigh"
	case "max":
		color = "thinkingMax"
	}
	return func(str string) string { return t.Fg(color, str) }
}

// GetBashModeBorderColor returns the bash-mode border style.
func (t *Theme) GetBashModeBorderColor() func(string) string {
	return func(str string) string { return t.Fg("bashMode", str) }
}

// ---- Color utilities ----

type rgbColor struct {
	R int
	G int
	B int
}

// RgbColor is the exported RGB triple (upstream RgbColor).
type RgbColor struct {
	R int
	G int
	B int
}

func hexToRgb(hex string) (rgbColor, error) {
	cleaned := strings.Replace(hex, "#", "", 1)
	if len(cleaned) != 6 {
		return rgbColor{}, fmt.Errorf("Invalid hex color: %s", hex)
	}
	r, errR := strconv.ParseInt(cleaned[0:2], 16, 32)
	g, errG := strconv.ParseInt(cleaned[2:4], 16, 32)
	b, errB := strconv.ParseInt(cleaned[4:6], 16, 32)
	if errR != nil || errG != nil || errB != nil {
		return rgbColor{}, fmt.Errorf("Invalid hex color: %s", hex)
	}
	return rgbColor{R: int(r), G: int(g), B: int(b)}, nil
}

var cubeValues = []int{0, 95, 135, 175, 215, 255}

func grayValues() []int {
	values := make([]int, 24)
	for i := range values {
		values[i] = 8 + i*10
	}
	return values
}

func findClosestCubeIndex(value int) int {
	minDist := 1 << 30
	minIdx := 0
	for index, candidate := range cubeValues {
		dist := value - candidate
		if dist < 0 {
			dist = -dist
		}
		if dist < minDist {
			minDist = dist
			minIdx = index
		}
	}
	return minIdx
}

func findClosestGrayIndex(gray int) int {
	values := grayValues()
	minDist := 1 << 30
	minIdx := 0
	for index, candidate := range values {
		dist := gray - candidate
		if dist < 0 {
			dist = -dist
		}
		if dist < minDist {
			minDist = dist
			minIdx = index
		}
	}
	return minIdx
}

func colorDistance(r1 int, g1 int, b1 int, r2 int, g2 int, b2 int) float64 {
	dr := float64(r1 - r2)
	dg := float64(g1 - g2)
	db := float64(b1 - b2)
	return dr*dr*0.299 + dg*dg*0.587 + db*db*0.114
}

func rgbTo256(r int, g int, b int) int {
	rIdx := findClosestCubeIndex(r)
	gIdx := findClosestCubeIndex(g)
	bIdx := findClosestCubeIndex(b)
	cubeR := cubeValues[rIdx]
	cubeG := cubeValues[gIdx]
	cubeB := cubeValues[bIdx]
	cubeIndex := 16 + 36*rIdx + 6*gIdx + bIdx
	cubeDist := colorDistance(r, g, b, cubeR, cubeG, cubeB)

	gray := int(0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b) + 0.5)
	grayIdx := findClosestGrayIndex(gray)
	grayValue := grayValues()[grayIdx]
	grayIndex := 232 + grayIdx
	grayDist := colorDistance(r, g, b, grayValue, grayValue, grayValue)

	maxC := maxInt(r, maxInt(g, b))
	minC := minInt(r, minInt(g, b))
	spread := maxC - minC

	if spread < 10 && grayDist < cubeDist {
		return grayIndex
	}
	return cubeIndex
}

func hexTo256(hex string) (int, error) {
	rgb, err := hexToRgb(hex)
	if err != nil {
		return 0, err
	}
	return rgbTo256(rgb.R, rgb.G, rgb.B), nil
}

func fgAnsi(color ColorValue, mode ColorMode) string {
	if color.IsIndex {
		return "\x1b[38;5;" + strconv.Itoa(color.Index) + "m"
	}
	if color.Value == "" {
		return "\x1b[39m"
	}
	if strings.HasPrefix(color.Value, "#") {
		if mode == ColorModeTruecolor {
			rgb, err := hexToRgb(color.Value)
			if err != nil {
				panic(err.Error())
			}
			return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", rgb.R, rgb.G, rgb.B)
		}
		index, err := hexTo256(color.Value)
		if err != nil {
			panic(err.Error())
		}
		return "\x1b[38;5;" + strconv.Itoa(index) + "m"
	}
	panic("Invalid color value: " + color.Value)
}

func bgAnsi(color ColorValue, mode ColorMode) string {
	if color.IsIndex {
		return "\x1b[48;5;" + strconv.Itoa(color.Index) + "m"
	}
	if color.Value == "" {
		return "\x1b[49m"
	}
	if strings.HasPrefix(color.Value, "#") {
		if mode == ColorModeTruecolor {
			rgb, err := hexToRgb(color.Value)
			if err != nil {
				panic(err.Error())
			}
			return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", rgb.R, rgb.G, rgb.B)
		}
		index, err := hexTo256(color.Value)
		if err != nil {
			panic(err.Error())
		}
		return "\x1b[48;5;" + strconv.Itoa(index) + "m"
	}
	panic("Invalid color value: " + color.Value)
}

func resolveVarRefs(value ColorValue, vars map[string]ColorValue, visited map[string]bool) ColorValue {
	if value.IsIndex || value.Value == "" || strings.HasPrefix(value.Value, "#") {
		return value
	}
	if visited[value.Value] {
		panic("Circular variable reference detected: " + value.Value)
	}
	next, ok := vars[value.Value]
	if !ok {
		panic("Variable reference not found: " + value.Value)
	}
	visited[value.Value] = true
	return resolveVarRefs(next, vars, visited)
}

// ResolveThemeColors resolves variable references in a color map.
func ResolveThemeColors(colors map[string]ColorValue, vars map[string]ColorValue) map[string]ColorValue {
	resolved := make(map[string]ColorValue, len(colors))
	keys := make([]string, 0, len(colors))
	for key := range colors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		resolved[key] = resolveVarRefs(colors[key], vars, map[string]bool{})
	}
	return resolved
}

func withThemeColorFallbacks(colors map[string]ColorValue) map[string]ColorValue {
	result := make(map[string]ColorValue, len(colors)+5)
	for key, value := range colors {
		result[key] = value
	}
	if _, ok := result["scrollbarTrack"]; !ok {
		result["scrollbarTrack"] = colors["muted"]
	}
	if _, ok := result["scrollbarThumb"]; !ok {
		result["scrollbarThumb"] = colors["text"]
	}
	if _, ok := result["thinkingMax"]; !ok {
		result["thinkingMax"] = colors["thinkingXhigh"]
	}
	if _, ok := result["searchMatchText"]; !ok {
		result["searchMatchText"] = colors["text"]
	}
	return result
}

func withBackgroundFallbacks(colors map[string]ColorValue) map[string]ColorValue {
	result := make(map[string]ColorValue, len(colors)+1)
	for key, value := range colors {
		result[key] = value
	}
	if _, ok := result["searchMatchBg"]; !ok {
		result["searchMatchBg"] = colors["selectedBg"]
	}
	return result
}

// ---- Theme loading ----

var backgroundColorKeys = map[string]bool{
	"selectedBg": true, "searchMatchBg": true, "userMessageBg": true, "customMessageBg": true,
	"toolPendingBg": true, "toolSuccessBg": true, "toolErrorBg": true,
}

var (
	builtinThemesOnce sync.Once
	builtinThemes     map[string]*ThemeJSON
)

func getBuiltinThemes() map[string]*ThemeJSON {
	builtinThemesOnce.Do(func() {
		builtinThemes = map[string]*ThemeJSON{}
		for _, name := range []string{"dark", "light"} {
			data, err := builtinThemeFS.ReadFile("themes/" + name + ".json")
			if err != nil {
				panic(err)
			}
			theme, err := ParseThemeJSON(name, data, false)
			if err != nil {
				panic(err)
			}
			builtinThemes[name] = theme
		}
	})
	return builtinThemes
}

// ThemeInfo pairs a theme name with its file path.
type ThemeInfo struct {
	Name string
	Path string
}

// ThemesDir returns the built-in themes directory.
func ThemesDir() string {
	return "themes"
}

// AvailableThemesWithPaths lists built-in, custom, and registered themes.
func AvailableThemesWithPaths() []ThemeInfo {
	var result []ThemeInfo
	seen := map[string]bool{}
	add := func(info ThemeInfo) {
		if seen[info.Name] {
			return
		}
		seen[info.Name] = true
		result = append(result, info)
	}
	for name := range getBuiltinThemes() {
		add(ThemeInfo{Name: name, Path: filepath.Join(ThemesDir(), name+".json")})
	}
	for _, info := range customThemeInfos() {
		add(info)
	}
	for name, theme := range registeredThemesSnapshot() {
		add(ThemeInfo{Name: name, Path: theme.SourcePath})
	}
	sort.SliceStable(result, func(a, b int) bool {
		return localeCompareTheme(result[a].Name, result[b].Name) < 0
	})
	return result
}

// AvailableThemes lists the theme names.
func AvailableThemes() []string {
	infos := AvailableThemesWithPaths()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names
}

func maxInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}

func minInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func localeCompareTheme(a string, b string) int {
	lowerA := strings.ToLower(a)
	lowerB := strings.ToLower(b)
	if lowerA != lowerB {
		if lowerA < lowerB {
			return -1
		}
		return 1
	}
	if a == b {
		return 0
	}
	if a < b {
		return -1
	}
	return 1
}

func customThemeInfos() []ThemeInfo {
	customThemesDir := CustomThemesDir()
	entries, err := os.ReadDir(customThemesDir)
	if err != nil {
		return nil
	}
	var result []ThemeInfo
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		themePath := filepath.Join(customThemesDir, entry.Name())
		theme, err := LoadThemeFromPath(themePath, "")
		if err == nil && theme.Name != "" {
			result = append(result, ThemeInfo{Name: theme.Name, Path: themePath})
		}
	}
	return result
}

// LoadThemeFromPath reads and resolves a theme file.
func LoadThemeFromPath(themePath string, mode ColorMode) (*Theme, error) {
	data, err := os.ReadFile(themePath)
	if err != nil {
		return nil, err
	}
	themeJSON, err := ParseThemeJSON(themePath, stripBOM(data), true)
	if err != nil {
		return nil, err
	}
	return CreateTheme(themeJSON, mode, themePath), nil
}

// CreateTheme builds a Theme from a validated document.
func CreateTheme(themeJSON *ThemeJSON, mode ColorMode, sourcePath string) *Theme {
	colorMode := mode
	if colorMode == "" {
		if terminalCapabilitiesTrueColor() {
			colorMode = ColorModeTruecolor
		} else {
			colorMode = ColorMode256
		}
	}
	resolved := ResolveThemeColors(themeJSON.Colors, themeJSON.Vars)
	fgColors := map[string]ColorValue{}
	bgColors := map[string]ColorValue{}
	for key, value := range resolved {
		if backgroundColorKeys[key] {
			bgColors[key] = value
		} else {
			fgColors[key] = value
		}
	}
	return NewTheme(fgColors, bgColors, colorMode, themeJSON.Name, sourcePath)
}

func loadThemeJSON(name string) (*ThemeJSON, error) {
	if theme, ok := getBuiltinThemes()[name]; ok {
		return theme, nil
	}
	if registered, ok := registeredThemesGet(name); ok {
		if registered.SourcePath != "" {
			data, err := os.ReadFile(registered.SourcePath)
			if err != nil {
				return nil, err
			}
			return ParseThemeJSON(registered.SourcePath, stripBOM(data), true)
		}
		return nil, fmt.Errorf("Theme %q does not have a source path for export", name)
	}
	themePath := filepath.Join(CustomThemesDir(), name+".json")
	if _, err := os.Stat(themePath); err != nil {
		return nil, fmt.Errorf("Theme not found: %s", name)
	}
	data, err := os.ReadFile(themePath)
	if err != nil {
		return nil, err
	}
	return ParseThemeJSON(name, stripBOM(data), true)
}

func loadTheme(name string, mode ColorMode) (*Theme, error) {
	if registered, ok := registeredThemesGet(name); ok {
		return registered, nil
	}
	themeJSON, err := loadThemeJSON(name)
	if err != nil {
		return nil, err
	}
	return CreateTheme(themeJSON, mode, ""), nil
}

// GetThemeByName loads a theme by name, or nil if it fails.
func GetThemeByName(name string) *Theme {
	theme, err := loadTheme(name, "")
	if err != nil {
		return nil
	}
	return theme
}

func stripBOM(data []byte) []byte {
	return []byte(strings.TrimPrefix(string(data), "\uFEFF"))
}

// ---- Terminal theme detection ----

// TerminalTheme is the terminal's light/dark classification.
// TerminalTheme is the terminal's light/dark preference (alias so the renderer
// query surface satisfies the theme interfaces).
type TerminalTheme = tui.TerminalColorScheme

const (
	TerminalThemeDark  TerminalTheme = tui.TerminalColorSchemeDark
	TerminalThemeLight TerminalTheme = tui.TerminalColorSchemeLight
)

// ParseAutoThemeSetting parses a "light/dark" auto theme setting.
func ParseAutoThemeSetting(themeSetting *string) (lightTheme string, darkTheme string, ok bool) {
	if themeSetting == nil || *themeSetting == "" {
		return "", "", false
	}
	value := *themeSetting
	slashIndex := strings.Index(value, "/")
	if slashIndex == -1 || strings.Contains(value[slashIndex+1:], "/") {
		return "", "", false
	}
	lightTheme = strings.TrimSpace(value[:slashIndex])
	darkTheme = strings.TrimSpace(value[slashIndex+1:])
	if lightTheme == "" || darkTheme == "" {
		return "", "", false
	}
	return lightTheme, darkTheme, true
}

// ResolveThemeSetting resolves an auto theme setting for a terminal theme.
func ResolveThemeSetting(themeSetting *string, terminalTheme TerminalTheme) (string, bool) {
	if light, dark, ok := ParseAutoThemeSetting(themeSetting); ok {
		if terminalTheme == TerminalThemeLight {
			return light, true
		}
		return dark, true
	}
	if themeSetting == nil {
		return "", false
	}
	if strings.Contains(*themeSetting, "/") {
		return "", false
	}
	return *themeSetting, true
}

// TerminalThemeDetection is a detection result.
type TerminalThemeDetection struct {
	Theme      TerminalTheme
	Source     string // "terminal background" | "COLORFGBG" | "fallback"
	Detail     string
	Confidence string // "high" | "low"
}

func getColorFgBgBackgroundIndex(colorfgbg string) (int, bool) {
	parts := strings.Split(colorfgbg, ";")
	for index := len(parts) - 1; index >= 0; index-- {
		bg, err := strconv.Atoi(strings.TrimSpace(parts[index]))
		if err == nil && bg >= 0 && bg <= 255 {
			return bg, true
		}
	}
	return 0, false
}

func getRgbColorLuminance(rgb rgbColor) float64 {
	toLinear := func(channel int) float64 {
		value := float64(channel) / 255
		if value <= 0.03928 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*toLinear(rgb.R) + 0.7152*toLinear(rgb.G) + 0.0722*toLinear(rgb.B)
}

func getAnsiColorLuminance(index int) float64 {
	rgb, err := hexToRgb(ansi256ToHex(index))
	if err != nil {
		return 0
	}
	return getRgbColorLuminance(rgb)
}

// GetThemeForRgbColor classifies a background color.
func GetThemeForRgbColor(rgb RgbColor) TerminalTheme {
	if getRgbColorLuminance(rgbColor{R: rgb.R, G: rgb.G, B: rgb.B}) >= 0.5 {
		return TerminalThemeLight
	}
	return TerminalThemeDark
}

// DetectTerminalBackgroundFromEnv detects the theme from COLORFGBG.
func DetectTerminalBackgroundFromEnv(env func(string) string) TerminalThemeDetection {
	colorfgbg := ""
	if env != nil {
		colorfgbg = env("COLORFGBG")
	}
	if bg, ok := getColorFgBgBackgroundIndex(colorfgbg); ok {
		theme := TerminalThemeDark
		if getAnsiColorLuminance(bg) >= 0.5 {
			theme = TerminalThemeLight
		}
		return TerminalThemeDetection{
			Theme:      theme,
			Source:     "COLORFGBG",
			Detail:     "background color index " + strconv.Itoa(bg),
			Confidence: "high",
		}
	}
	return TerminalThemeDetection{
		Theme:      TerminalThemeDark,
		Source:     "fallback",
		Detail:     "no terminal background hint found",
		Confidence: "low",
	}
}

// ansi256ToHex converts a 256-color index to a hex color.
func ansi256ToHex(index int) string {
	basicColors := []string{
		"#000000", "#800000", "#008000", "#808000", "#000080", "#800080", "#008080", "#c0c0c0",
		"#808080", "#ff0000", "#00ff00", "#ffff00", "#0000ff", "#ff00ff", "#00ffff", "#ffffff",
	}
	if index < 16 {
		return basicColors[index]
	}
	if index < 232 {
		cubeIndex := index - 16
		r := cubeIndex / 36
		g := (cubeIndex % 36) / 6
		b := cubeIndex % 6
		return hexFromRgb(cubeValues[r], cubeValues[g], cubeValues[b])
	}
	gray := 8 + (index-232)*10
	return hexFromRgb(gray, gray, gray)
}

func hexFromRgb(r int, g int, b int) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// GetDefaultTheme returns the environment-detected default theme name.
func GetDefaultTheme() string {
	return string(DetectTerminalBackgroundFromEnv(os.Getenv).Theme)
}

// ---- Global theme registry ----

var themeState struct {
	mu             sync.Mutex
	current        *Theme
	currentName    string
	registered     map[string]*Theme
	onChange       func()
	watcherStop    chan struct{}
	watcherWatchFn func(string) // test seam for the watcher (unused by default)
	validator      func(label string, raw json.RawMessage) (*ThemeJSON, error)
}

func registeredThemesSnapshot() map[string]*Theme {
	themeState.mu.Lock()
	defer themeState.mu.Unlock()
	out := map[string]*Theme{}
	for name, theme := range themeState.registered {
		out[name] = theme
	}
	return out
}

func registeredThemesGet(name string) (*Theme, bool) {
	themeState.mu.Lock()
	defer themeState.mu.Unlock()
	theme, ok := themeState.registered[name]
	return theme, ok
}

// SetRegisteredThemes installs the registered (in-memory) themes.
func SetRegisteredThemes(themes []*Theme) {
	themeState.mu.Lock()
	defer themeState.mu.Unlock()
	themeState.registered = map[string]*Theme{}
	for _, theme := range themes {
		if theme == nil || theme.Name == "" {
			continue
		}
		if strings.Contains(theme.Name, "/") {
			panic("Invalid theme name \"" + theme.Name + "\": theme names cannot contain \"/\"")
		}
		themeState.registered[theme.Name] = theme
	}
}

// SetThemeJSONValidator installs the document validator.
func SetThemeJSONValidator(validator func(label string, raw json.RawMessage) (*ThemeJSON, error)) {
	themeState.mu.Lock()
	defer themeState.mu.Unlock()
	themeState.validator = validator
}

// InitTheme loads the given theme (or the detected default) into the global slot.
func InitTheme(themeName string, enableWatcher bool) {
	name := themeName
	if name == "" {
		name = GetDefaultTheme()
	}
	themeState.mu.Lock()
	themeState.currentName = name
	themeState.mu.Unlock()
	theme, err := loadTheme(name, "")
	if err != nil {
		setGlobalThemeLocked("dark", mustLoadTheme("dark"))
		return
	}
	setGlobalThemeLocked(name, theme)
	if enableWatcher {
		startThemeWatcher(name)
	}
}

func mustLoadTheme(name string) *Theme {
	theme, err := loadTheme(name, "")
	if err != nil {
		panic(err)
	}
	return theme
}

func setGlobalThemeLocked(name string, theme *Theme) {
	themeState.mu.Lock()
	defer themeState.mu.Unlock()
	themeState.current = theme
	themeState.currentName = name
}

// SetTheme switches the global theme.
func SetTheme(name string, enableWatcher bool) (bool, string) {
	themeState.mu.Lock()
	themeState.currentName = name
	themeState.mu.Unlock()
	theme, err := loadTheme(name, "")
	if err != nil {
		setGlobalThemeLocked("dark", mustLoadTheme("dark"))
		return false, err.Error()
	}
	setGlobalThemeLocked(name, theme)
	if enableWatcher {
		startThemeWatcher(name)
	}
	themeState.mu.Lock()
	callback := themeState.onChange
	themeState.mu.Unlock()
	if callback != nil {
		callback()
	}
	return true, ""
}

// SetThemeInstance installs an in-memory theme.
func SetThemeInstance(theme *Theme) {
	setGlobalThemeLocked("<in-memory>", theme)
	stopThemeWatcher()
	themeState.mu.Lock()
	callback := themeState.onChange
	themeState.mu.Unlock()
	if callback != nil {
		callback()
	}
}

// OnThemeChange registers the change callback.
func OnThemeChange(callback func()) {
	themeState.mu.Lock()
	defer themeState.mu.Unlock()
	themeState.onChange = callback
}

// CurrentTheme returns the active theme.
func CurrentTheme() *Theme {
	themeState.mu.Lock()
	defer themeState.mu.Unlock()
	return themeState.current
}

// CurrentThemeName returns the active theme name.
func CurrentThemeName() string {
	themeState.mu.Lock()
	defer themeState.mu.Unlock()
	return themeState.currentName
}

// startThemeWatcher polls a custom theme file for changes (D75: no fs.watch in
// the Go port).
func startThemeWatcher(themeName string) {
	stopThemeWatcher()
	if themeName == "" || themeName == "dark" || themeName == "light" {
		return
	}
	themeFile := filepath.Join(CustomThemesDir(), themeName+".json")
	if _, err := os.Stat(themeFile); err != nil {
		return
	}

	stop := make(chan struct{})
	themeState.mu.Lock()
	themeState.watcherStop = stop
	themeState.mu.Unlock()

	go func() {
		var lastMod time.Time
		if info, err := os.Stat(themeFile); err == nil {
			lastMod = info.ModTime()
		}
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if CurrentThemeName() != themeName {
					return
				}
				info, err := os.Stat(themeFile)
				if err != nil {
					continue
				}
				if !info.ModTime().After(lastMod) {
					continue
				}
				lastMod = info.ModTime()
				reloaded, err := LoadThemeFromPath(themeFile, "")
				if err != nil {
					// The file may be mid-write; ignore and retry on the next tick.
					continue
				}
				themeState.mu.Lock()
				if themeState.registered != nil {
					themeState.registered[themeName] = reloaded
				}
				themeState.current = reloaded
				callback := themeState.onChange
				themeState.mu.Unlock()
				if callback != nil {
					callback()
				}
			}
		}
	}()
}

// StopThemeWatcher stops the theme file watcher.
func StopThemeWatcher() {
	themeState.mu.Lock()
	stop := themeState.watcherStop
	themeState.watcherStop = nil
	themeState.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

func stopThemeWatcher() { StopThemeWatcher() }

// terminalCapabilitiesTrueColor reports the terminal's truecolor support; the
// host installs the capability flag (D69: injectable capabilities).
var trueColorState struct {
	mu      sync.Mutex
	enabled bool
	known   bool
}

// SetTrueColorSupport installs the terminal truecolor capability.
func SetTrueColorSupport(enabled bool) {
	trueColorState.mu.Lock()
	defer trueColorState.mu.Unlock()
	trueColorState.enabled = enabled
	trueColorState.known = true
}

func terminalCapabilitiesTrueColor() bool {
	trueColorState.mu.Lock()
	defer trueColorState.mu.Unlock()
	return trueColorState.enabled
}

// CustomThemesDir returns the user's custom themes directory. The host
// installs it (upstream reads the agent directory).
var customThemesDirState struct {
	mu  sync.Mutex
	dir string
}

// SetCustomThemesDir installs the custom themes directory.
func SetCustomThemesDir(dir string) {
	customThemesDirState.mu.Lock()
	defer customThemesDirState.mu.Unlock()
	customThemesDirState.dir = dir
}

// CustomThemesDir returns the configured custom themes directory.
func CustomThemesDir() string {
	customThemesDirState.mu.Lock()
	defer customThemesDirState.mu.Unlock()
	return customThemesDirState.dir
}

// ---- Terminal queries (src/modes/interactive/theme/theme.ts) ----

// TerminalBackgroundDetector queries the terminal's background color.
type TerminalBackgroundDetector interface {
	QueryTerminalBackgroundColor(timeoutMs int) (RgbColor, bool)
}

// TerminalAutoThemeDetector additionally queries the terminal color scheme.
type TerminalAutoThemeDetector interface {
	TerminalBackgroundDetector
	QueryTerminalColorScheme(timeoutMs int) (TerminalTheme, bool)
}

// DetectTerminalBackgroundTheme queries the terminal background and falls back
// to environment detection.
//
// Upstream runs the color-scheme and background queries concurrently; the Go
// port queries them in sequence (D76).
func DetectTerminalBackgroundTheme(ui TerminalBackgroundDetector, timeoutMs int, env func(string) string) TerminalThemeDetection {
	if ui != nil {
		if rgb, ok := ui.QueryTerminalBackgroundColor(timeoutMs); ok {
			theme := GetThemeForRgbColor(rgb)
			return TerminalThemeDetection{
				Theme:      theme,
				Source:     "terminal background",
				Detail:     "OSC 11 background rgb(" + itoa(rgb.R) + ", " + itoa(rgb.G) + ", " + itoa(rgb.B) + ")",
				Confidence: "high",
			}
		}
	}
	return DetectTerminalBackgroundFromEnv(env)
}

// DetectTerminalThemeForAuto prefers the terminal color-scheme report and
// falls back to the background detection.
func DetectTerminalThemeForAuto(ui TerminalAutoThemeDetector, timeoutMs int, env func(string) string) TerminalTheme {
	if ui != nil {
		if scheme, ok := ui.QueryTerminalColorScheme(timeoutMs); ok {
			return scheme
		}
	}
	return DetectTerminalBackgroundTheme(ui, timeoutMs, env).Theme
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		digits[index] = '-'
	}
	return string(digits[index:])
}
