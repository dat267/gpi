package interactive

import (
	"math/rand"
	"strconv"
	"strings"

	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/{skill-invocation-message,
// first-time-setup,earendil-announcement,daxnuts,armin}.ts.
//
// Divergences: the XBM/RGB easter eggs use an injectable random source because
// the upstream effects call Math.random (D95); the bundled announcement image
// asset is not shipped, so only its text renders (D94).

// SkillInvocationMessageComponent renders a skill block with the
// collapsed/expanded state.
type SkillInvocationMessageComponent struct {
	*tui.Box

	name          string
	content       string
	markdownTheme tui.MarkdownTheme
	expanded      bool
}

// NewSkillInvocationMessageComponent creates the component.
func NewSkillInvocationMessageComponent(name string, content string, markdownTheme *tui.MarkdownTheme) *SkillInvocationMessageComponent {
	theme := ActiveTheme()
	component := &SkillInvocationMessageComponent{
		Box:           tui.NewBox(1, 1, func(text string) string { return theme.Bg("customMessageBg", text) }),
		name:          name,
		content:       content,
		markdownTheme: markdownThemeValue(markdownTheme),
	}
	component.updateDisplay()
	return component
}

// SetExpanded toggles the expanded state.
func (c *SkillInvocationMessageComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.updateDisplay()
}

// Invalidate rebuilds the content.
func (c *SkillInvocationMessageComponent) Invalidate() {
	c.Box.Invalidate()
	c.updateDisplay()
}

func (c *SkillInvocationMessageComponent) updateDisplay() {
	theme := ActiveTheme()
	c.Box.Clear()
	content := &tui.Container{}

	if c.expanded {
		label := theme.Fg("customMessageLabel", "\x1b[1m[skill]\x1b[22m")
		content.AddChild(tui.NewText(label, 0, 0, nil))
		header := "**" + c.name + "**\n\n"
		content.AddChild(tui.NewMarkdown(header+c.content, 0, 0, c.markdownTheme,
			&tui.DefaultTextStyle{Color: func(text string) string { return theme.Fg("customMessageText", text) }},
			tui.MarkdownOptions{}))
	} else {
		line := theme.Fg("customMessageLabel", "\x1b[1m[skill]\x1b[22m ") +
			theme.Fg("customMessageText", c.name) +
			theme.Fg("dim", " ("+KeyText("app.tools.expand")+" to expand)")
		content.AddChild(tui.NewText(line, 0, 0, nil))
	}

	c.Box.AddChild(tui.NewMouseRegion(content, func(event tui.TuiMouseEvent) *tui.TuiMouseDispatchResult {
		if event.Type != tui.MouseClick || event.Button != tui.MouseButtonLeft {
			return nil
		}
		c.SetExpanded(!c.expanded)
		return &tui.TuiMouseDispatchResult{TuiMouseEventResult: tui.TuiMouseEventResult{Handled: true}}
	}))
}

// FirstTimeSetupResult is the setup dialog outcome.
type FirstTimeSetupResult struct {
	Theme          TerminalTheme
	ShareAnalytics bool
}

// FirstTimeSetupOptions configure the dialog.
type FirstTimeSetupOptions struct {
	DetectedTheme  TerminalTheme
	OnThemePreview func(themeName string)
	OnSubmit       func(result FirstTimeSetupResult)
	OnCancel       func()
	// AppName overrides the product name in the welcome line.
	AppName string
}

var setupLogoLines = []string{"██████", "██  ██", "████  ██", "██    ██"}

// FirstTimeSetupComponent is the first-run dialog (theme + analytics).
type FirstTimeSetupComponent struct {
	*tui.Container

	step           string // "theme" | "analytics"
	themeIndex     int
	analyticsIndex int
	options        FirstTimeSetupOptions
}

// NewFirstTimeSetupComponent creates the dialog.
func NewFirstTimeSetupComponent(options FirstTimeSetupOptions) *FirstTimeSetupComponent {
	if options.AppName == "" {
		options.AppName = "pi"
	}
	component := &FirstTimeSetupComponent{Container: &tui.Container{}, options: options, step: "theme"}
	component.themeIndex = 0
	if options.DetectedTheme == TerminalThemeLight {
		component.themeIndex = 1
	}
	component.update()
	return component
}

func (c *FirstTimeSetupComponent) update() {
	theme := ActiveTheme()
	c.Container.Clear()
	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tui.NewSpacer(1))
	c.AddChild(tui.NewText(theme.Fg("accent", strings.Join(setupLogoLines, "\n")), 1, 0, nil))
	c.AddChild(tui.NewSpacer(1))
	c.AddChild(tui.NewText(theme.Fg("accent", theme.Bold("Welcome to "+c.options.AppName+", the minimal coding agent.")), 1, 0, nil))
	c.AddChild(tui.NewSpacer(1))

	if c.step == "theme" {
		c.AddChild(tui.NewText(theme.Fg("text", "Pick a theme."), 1, 0, nil))
		c.AddChild(tui.NewText(theme.Fg("muted", "Detected system appearance: "+string(c.options.DetectedTheme)), 1, 0, nil))
		c.AddChild(tui.NewSpacer(1))
		c.addOptionList([]string{"Dark", "Light"}, c.themeIndex)
	} else {
		c.AddChild(tui.NewText(theme.Fg("text", "Opt-in to anonymous usage data sharing?"), 1, 0, nil))
		c.AddChild(tui.NewText(theme.Fg("muted",
			"Opting in stores a tracking identifier in settings.json and enables anonymous\n"+
				"usage analytics. This helps us to better debug, reproduce, and resolve issues\n"+
				"and bugs within Pi. You can observe what is shared using /privacy and make\n"+
				"changes anytime in settings.json."), 1, 0, nil))
		c.AddChild(tui.NewSpacer(1))
		c.addOptionList([]string{"Share anonymous usage data", "Don't share"}, c.analyticsIndex)
	}

	c.AddChild(tui.NewSpacer(1))
	continueHint := "continue"
	if c.step != "theme" {
		continueHint = "finish"
	}
	c.AddChild(tui.NewText(RawKeyHint("↑↓", "navigate")+"  "+
		KeyHint("tui.select.confirm", continueHint)+"  "+
		KeyHint("tui.select.cancel", "skip setup"), 1, 0, nil))
	c.AddChild(tui.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))
}

func (c *FirstTimeSetupComponent) addOptionList(labels []string, selectedIndex int) {
	theme := ActiveTheme()
	for index, label := range labels {
		prefix := "  "
		text := theme.Fg("text", label)
		if index == selectedIndex {
			prefix = theme.Fg("accent", "→ ")
			text = theme.Fg("accent", label)
		}
		c.AddChild(tui.NewText(prefix+text, 1, 0, nil))
	}
}

func (c *FirstTimeSetupComponent) moveSelection(delta int) {
	if c.step == "theme" {
		next := maxIntLocal(0, minIntLocal(1, c.themeIndex+delta))
		if next != c.themeIndex {
			c.themeIndex = next
			if c.options.OnThemePreview != nil {
				name := string(TerminalThemeDark)
				if c.themeIndex == 1 {
					name = string(TerminalThemeLight)
				}
				c.options.OnThemePreview(name)
			}
		}
	} else {
		c.analyticsIndex = maxIntLocal(0, minIntLocal(1, c.analyticsIndex+delta))
	}
	c.update()
}

// HandleInput processes input.
func (c *FirstTimeSetupComponent) HandleInput(data string) {
	kb := tui.GetKeybindings()
	switch {
	case kb.Matches(data, "tui.select.up") || data == "k":
		c.moveSelection(-1)
	case kb.Matches(data, "tui.select.down") || data == "j":
		c.moveSelection(1)
	case kb.Matches(data, "tui.select.confirm") || data == "\n":
		if c.step == "theme" {
			c.step = "analytics"
			c.update()
			return
		}
		if c.options.OnSubmit != nil {
			theme := TerminalThemeDark
			if c.themeIndex == 1 {
				theme = TerminalThemeLight
			}
			c.options.OnSubmit(FirstTimeSetupResult{Theme: theme, ShareAnalytics: c.analyticsIndex == 0})
		}
	case kb.Matches(data, "tui.select.cancel"):
		if c.options.OnCancel != nil {
			c.options.OnCancel()
		}
	}
}

// Step returns the current step (test helper).
func (c *FirstTimeSetupComponent) Step() string { return c.step }

// EarendilAnnouncementComponent is the announcement card.
type EarendilAnnouncementComponent struct {
	*tui.Container
}

const earendilBlogURL = "https://mariozechner.at/posts/2026-04-08-ive-sold-out/"

// NewEarendilAnnouncementComponent creates the announcement. The bundled
// image asset is not shipped, so the image branch is omitted (D94).
func NewEarendilAnnouncementComponent() *EarendilAnnouncementComponent {
	theme := ActiveTheme()
	component := &EarendilAnnouncementComponent{Container: &tui.Container{}}
	component.AddChild(NewDynamicBorder(func(text string) string { return theme.Fg("accent", text) }))
	component.AddChild(tui.NewText(theme.Bold(theme.Fg("accent", "pi has joined Earendil")), 1, 0, nil))
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(tui.NewText(theme.Fg("muted", "Read the blog post:"), 1, 0, nil))
	component.AddChild(tui.NewText(theme.Fg("mdLink", earendilBlogURL), 1, 0, nil))
	component.AddChild(tui.NewSpacer(1))
	// The bundled image asset is not shipped (D94); upstream's Image component
	// renders no lines without image capabilities, so only the surrounding
	// spacer remains, keeping the rendered shape identical.
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(func(text string) string { return theme.Fg("accent", text) }))
	return component
}

// ---- Daxnuts easter egg ----

const (
	daxnutsWidth  = 32
	daxnutsHeight = 32
)

// DaxnutsComponent renders the daxnuts easter egg with a scanline reveal.
type DaxnutsComponent struct {
	image    []string
	tick     int
	maxTicks int
	host     tui.RenderRequester

	cachedLines []string
	cachedWidth int
	cachedTick  int
}

// NewDaxnutsComponent creates the component.
func NewDaxnutsComponent(host tui.RenderRequester) *DaxnutsComponent {
	return &DaxnutsComponent{
		image:      buildDaxnutsImage(),
		maxTicks:   25,
		host:       host,
		cachedTick: -1,
	}
}

func buildDaxnutsImage() []string {
	pixels := parseDaxnutsPixels()
	var lines []string
	for row := 0; row < daxnutsHeight; row += 2 {
		var builder strings.Builder
		for x := 0; x < daxnutsWidth; x++ {
			top := pixels[row][x]
			bottom := top
			if row+1 < daxnutsHeight {
				bottom = pixels[row+1][x]
			}
			builder.WriteString(rgbANSI(bottom[0], bottom[1], bottom[2], false))
			builder.WriteString(rgbANSI(top[0], top[1], top[2], true))
			builder.WriteString("▄")
		}
		builder.WriteString("\x1b[0m")
		lines = append(lines, builder.String())
	}
	return lines
}

func parseDaxnutsPixels() [][][3]int {
	pixels := make([][][3]int, 0, daxnutsHeight)
	for y := 0; y < daxnutsHeight; y++ {
		row := make([][3]int, 0, daxnutsWidth)
		for x := 0; x < daxnutsWidth; x++ {
			index := (y*daxnutsWidth + x) * 6
			r := parseHexByte(daxnutsRGBHexData[index : index+2])
			g := parseHexByte(daxnutsRGBHexData[index+2 : index+4])
			b := parseHexByte(daxnutsRGBHexData[index+4 : index+6])
			row = append(row, [3]int{r, g, b})
		}
		pixels = append(pixels, row)
	}
	return pixels
}

func parseHexByte(value string) int {
	parsed, err := strconv.ParseInt(value, 16, 32)
	if err != nil {
		return 0
	}
	return int(parsed)
}

func rgbANSI(r int, g int, b int, background bool) string {
	prefix := "38"
	if background {
		prefix = "48"
	}
	return "\x1b[" + prefix + ";2;" + itoa(r) + ";" + itoa(g) + ";" + itoa(b) + "m"
}

// SetTick sets the animation tick (test seam for the timer).
func (c *DaxnutsComponent) SetTick(tick int) {
	c.tick = tick
}

// Tick returns the current tick.
func (c *DaxnutsComponent) Tick() int { return c.tick }

// Advance increments the tick.
func (c *DaxnutsComponent) Advance() {
	c.tick++
	if c.tick >= c.maxTicks {
		c.tick = c.maxTicks
	}
	c.cachedWidth = 0
	if c.host != nil {
		c.host.RequestRender(false)
	}
}

// Invalidate drops the cache.
func (c *DaxnutsComponent) Invalidate() { c.cachedWidth = 0 }

// Render renders the scanline reveal.
func (c *DaxnutsComponent) Render(width int) []string {
	if width == c.cachedWidth && c.cachedTick == c.tick {
		return c.cachedLines
	}
	theme := ActiveTheme()
	var lines []string

	center := func(value string) string {
		visible := tui.VisibleWidth(value)
		left := maxIntLocal(0, (width-visible)/2)
		return strings.Repeat(" ", left) + value
	}

	lines = append(lines, "")

	revealedRows := minIntLocal(len(c.image), c.tick*(len(c.image)+3)/c.maxTicks)
	for i := 0; i < len(c.image); i++ {
		switch {
		case i < revealedRows:
			lines = append(lines, center(c.image[i]))
		case i == revealedRows:
			scanline := strings.Repeat("▓", daxnutsWidth)
			lines = append(lines, center(rgbANSI(100, 200, 255, false)+scanline+"\x1b[0m"))
		default:
			lines = append(lines, center(strings.Repeat(" ", daxnutsWidth)))
		}
	}

	lines = append(lines, "")

	textPhase := c.tick - c.maxTicks*6/10
	if textPhase > 0 || c.tick >= c.maxTicks {
		lines = append(lines, center(theme.Fg("accent", "Free Kimi K2.5 via OpenCode Zen")))
		lines = append(lines, center(theme.Fg("success", "\"Powered by daxnuts\"")))
		lines = append(lines, center(theme.Fg("muted", "— @thdxr")))
	} else {
		lines = append(lines, "", "", "")
	}

	lines = append(lines, "")
	if textPhase > 2 || c.tick >= c.maxTicks {
		lines = append(lines, center(theme.Fg("dim", "Try OpenCode")))
		lines = append(lines, center(theme.Fg("mdLink", "https://mistral.ai/news/mistral-vibe-2-0")))
	} else {
		lines = append(lines, "", "")
	}
	lines = append(lines, "")

	c.cachedLines = lines
	c.cachedWidth = width
	c.cachedTick = c.tick
	return lines
}

// ---- Armin easter egg ----

const (
	arminWidth    = 31
	arminHeight   = 36
	arminRowBytes = 4 // ceil(31/8)
)

// ArminEffects are the animation effect names.
var ArminEffects = []string{"typewriter", "scanline", "rain", "fade", "crt", "glitch", "dissolve"}

func arminPixel(x int, y int) bool {
	if y >= arminHeight {
		return false
	}
	byteIndex := y*arminRowBytes + x/8
	bitIndex := uint(x % 8)
	return (arminXBM[byteIndex]>>bitIndex)&1 == 0
}

func arminChar(x int, row int) string {
	upper := arminPixel(x, row*2)
	lower := arminPixel(x, row*2+1)
	switch {
	case upper && lower:
		return "█"
	case upper:
		return "▀"
	case lower:
		return "▄"
	default:
		return " "
	}
}

// ArminFinalGrid builds the full image grid (deterministic).
func ArminFinalGrid() [][]string {
	displayHeight := (arminHeight + 1) / 2
	grid := make([][]string, 0, displayHeight)
	for row := 0; row < displayHeight; row++ {
		line := make([]string, 0, arminWidth)
		for x := 0; x < arminWidth; x++ {
			line = append(line, arminChar(x, row))
		}
		grid = append(grid, line)
	}
	return grid
}

// ArminComponent renders the easter egg. The animation effects use an
// injectable random source (D95).
type ArminComponent struct {
	host      tui.RenderRequester
	Effect    string
	finalGrid [][]string
	grid      [][]string
	rng       *rand.Rand

	effectState map[string]any

	cachedLines []string
	cachedWidth int
}

// NewArminComponent creates the component with the given effect (empty picks
// one at random).
func NewArminComponent(host tui.RenderRequester, effect string, seed int64) *ArminComponent {
	rng := rand.New(rand.NewSource(seed))
	if effect == "" {
		effect = ArminEffects[rng.Intn(len(ArminEffects))]
	}
	component := &ArminComponent{
		host:      host,
		Effect:    effect,
		finalGrid: ArminFinalGrid(),
		rng:       rng,
	}
	component.grid = component.emptyGrid()
	component.initEffect()
	return component
}

// SetGrid replaces the animation grid (test seam).
func (c *ArminComponent) SetGrid(grid [][]string) {
	c.grid = grid
	c.cachedWidth = 0
}

// Grid returns the current animation grid.
func (c *ArminComponent) Grid() [][]string { return c.grid }

func (c *ArminComponent) emptyGrid() [][]string {
	grid := make([][]string, 0, len(c.finalGrid))
	for range c.finalGrid {
		row := make([]string, arminWidth)
		for i := range row {
			row[i] = " "
		}
		grid = append(grid, row)
	}
	return grid
}

func (c *ArminComponent) initEffect() {
	switch c.Effect {
	case "typewriter":
		c.effectState = map[string]any{"pos": 0}
	case "scanline":
		c.effectState = map[string]any{"row": 0}
	case "rain":
		drops := make([]map[string]int, arminWidth)
		for i := range drops {
			drops[i] = map[string]int{"y": -c.rng.Intn(len(c.finalGrid) * 2), "settled": 0}
		}
		c.effectState = map[string]any{"drops": drops}
	case "fade":
		c.effectState = map[string]any{"positions": shuffledPositions(c.rng, len(c.finalGrid), arminWidth), "idx": 0}
	case "crt":
		c.effectState = map[string]any{"expansion": 0}
	case "glitch":
		c.effectState = map[string]any{"phase": 0, "glitchFrames": 8}
	case "dissolve":
		chars := []string{" ", "░", "▒", "▓", "█", "▀", "▄"}
		for row := range c.grid {
			for x := range c.grid[row] {
				c.grid[row][x] = chars[c.rng.Intn(len(chars))]
			}
		}
		c.effectState = map[string]any{"positions": shuffledPositions(c.rng, len(c.finalGrid), arminWidth), "idx": 0}
	}
}

func shuffledPositions(rng *rand.Rand, rows int, columns int) [][2]int {
	positions := make([][2]int, 0, rows*columns)
	for row := 0; row < rows; row++ {
		for x := 0; x < columns; x++ {
			positions = append(positions, [2]int{row, x})
		}
	}
	for i := len(positions) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		positions[i], positions[j] = positions[j], positions[i]
	}
	return positions
}

// Render renders the animation grid with the accent color.
func (c *ArminComponent) Render(width int) []string {
	if width == c.cachedWidth && c.cachedLines != nil {
		return c.cachedLines
	}
	theme := ActiveTheme()
	const padding = 1
	availableWidth := width - padding

	var lines []string
	for _, row := range c.grid {
		clipped := ""
		if len(row) > availableWidth {
			clipped = strings.Join(row[:availableWidth], "")
		} else {
			clipped = strings.Join(row, "")
		}
		padRight := maxIntLocal(0, width-padding-tui.VisibleWidth(clipped))
		lines = append(lines, " "+theme.Fg("accent", clipped)+strings.Repeat(" ", padRight))
	}

	message := "ARMIN SAYS HI"
	msgPadRight := maxIntLocal(0, width-padding-len(message))
	lines = append(lines, " "+theme.Fg("accent", message)+strings.Repeat(" ", msgPadRight))

	c.cachedLines = lines
	c.cachedWidth = width
	return lines
}

// Invalidate drops the render cache.
func (c *ArminComponent) Invalidate() { c.cachedWidth = 0 }

var _ tui.Component = (*ArminComponent)(nil)
