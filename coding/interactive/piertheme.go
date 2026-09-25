package interactive

// The port's own palette (D154). Two themes — one for a dark terminal
// background, one for a light one — chosen by the same settings/terminal
// detection as upstream.
//
// Three deliberate differences from upstream's dark.json/light.json:
//
//   - **The decorative backgrounds are the terminal's.** The fills that only
//     group content — the selected list row, search matches, custom messages —
//     are left unset, which renders as the terminal's default background
//     (\x1b[49m) rather than a panel colour, so the theme never paints over a
//     transparent or blurred terminal. This is why every list marks its
//     selection with an accent-coloured "→ " prefix instead of a fill.
//   - **The signal backgrounds are carried, in this port's colours.**
//     toolPendingBg / toolSuccessBg / toolErrorBg and userMessageBg are kept,
//     because they are not decoration: the first three are the only thing that
//     says a tool call is running, failed or succeeded, and the fourth is the
//     only thing that says a block is yours. Left unset, a failed `bash` call
//     was byte-identical to a successful one. They are not upstream's values
//     either — your messages get a warm panel tied to the accent, where
//     upstream's is a cool blue-gray — so the palette stays recognisable.
//   - **The accent is amber, not teal.** Upstream's accent (#8abeb7, with blue
//     borders) is replaced throughout, so the palette is recognisably this port
//     at a glance — the header wordmark, borders, selection and list bullets all
//     carry it.
//
// The keyword for everything else is minimal: colour is spent on structure
// (borders, diffs) and emphasis (the accent) rather than on decoration. The
// palette paints its own `background` token, so the terminal's theme can no
// longer make the text unreadable; `text` is therefore an explicit colour and
// not the terminal's foreground.

// transparentBackgroundColors are the decorative background tokens. They are
// left unset so the terminal's own background shows through; bgAnsi renders an
// unset value as \x1b[49m, and every list marks its selection with an
// accent-coloured "→ " prefix, so a transparent selectedBg costs nothing in
// legibility.
//
// The filled tokens are the exception — see filledBackgroundColors — and
// pierColors gives a hex in the palette precedence over this list.
var transparentBackgroundColors = []string{
	"selectedBg", "searchMatchBg", "customMessageBg",
}

// toolStateBackgroundColors are the three tokens that say whether a tool call is
// running, failed or succeeded: upstream components/tool-execution.ts picks
// between them and fills the whole block, and the port does the same in
// toolexecution.go. They carry a colour in both palettes because for a failed
// call this fill is the *only* signal — without it a failure is
// indistinguishable from a success.
var toolStateBackgroundColors = []string{
	"toolPendingBg", "toolSuccessBg", "toolErrorBg",
}

// filledBackgroundColors are every background token the palette paints: the
// tool states above plus userMessageBg, the fill that marks a block as yours
// (upstream components/user-message.ts, and the assistant message has no fill).
// Not decoration, so not transparent — and not upstream's colour either, since
// the port's palette is meant to be recognisable at a glance.
var filledBackgroundColors = append(append([]string{}, toolStateBackgroundColors...), "userMessageBg")

func pierColors(hex map[string]string) map[string]ColorValue {
	colors := make(map[string]ColorValue, len(hex)+len(transparentBackgroundColors))
	for _, key := range transparentBackgroundColors {
		colors[key] = ColorValue{Value: ""}
	}
	for key, value := range hex {
		colors[key] = ColorValue{Value: value}
	}
	return colors
}

// pierExport is the HTML export palette (the export is a document, not a
// terminal, so it cannot inherit the terminal's background).
func pierExport(pageBg, cardBg, infoBg string) *ThemeExport {
	return &ThemeExport{
		PageBg: &ColorValue{Value: pageBg},
		CardBg: &ColorValue{Value: cardBg},
		InfoBg: &ColorValue{Value: infoBg},
	}
}

// pierDarkJSON is the palette for a dark terminal background.
func pierDarkJSON() *ThemeJSON {
	return &ThemeJSON{
		Name: "dark",
		Colors: pierColors(map[string]string{
			// Primary text is an explicit light colour: the palette paints its own
			// background, so the terminal's foreground can no longer be trusted.
			"background":   "#101010",
			"text":         "#d4d4d4",
			"accent":       "#ffb454",
			"border":       "#4a4a4a",
			"borderAccent": "#ffb454",
			"borderMuted":  "#333333",
			"success":      "#7ec699",
			"error":        "#ff6b6b",
			"warning":      "#ffd479",
			"muted":        "#8b8b8b",
			"dim":          "#5f5f5f",

			"thinkingText":       "#9a9a9a",
			"toolTitle":          "#d4d4d4",
			"toolOutput":         "#8b8b8b",
			"userMessageText":    "#d4d4d4",
			"customMessageText":  "#d4d4d4",
			"customMessageLabel": "#c792ea",

			// The tool-state fills: a neutral panel while a call runs, and the
			// success/error hues as a tint of it. With userMessageBg below they are
			// the only things the palette paints behind text — see
			// filledBackgroundColors.
			"toolPendingBg": "#2b2b2b",
			"toolSuccessBg": "#22302a",
			"toolErrorBg":   "#3a2424",

			// Your own messages get a warm panel tied to the amber accent, where
			// upstream's is a cool blue-gray (#343541). Same lightness as
			// upstream's, so a transcript reads the same way.
			"userMessageBg": "#393630",

			"mdHeading":         "#ffb454",
			"mdLink":            "#6cb6ff",
			"mdLinkUrl":         "#5f5f5f",
			"mdCode":            "#ffb454",
			"mdCodeBlock":       "#7ec699",
			"mdCodeBlockBorder": "#5f5f5f",
			"mdQuote":           "#8b8b8b",
			"mdQuoteBorder":     "#5f5f5f",
			"mdHr":              "#5f5f5f",
			"mdListBullet":      "#ffb454",

			"toolDiffAdded":   "#7ec699",
			"toolDiffRemoved": "#ff6b6b",
			"toolDiffContext": "#8b8b8b",

			"syntaxComment":     "#6b7280",
			"syntaxKeyword":     "#c792ea",
			"syntaxFunction":    "#ffb454",
			"syntaxVariable":    "#6cb6ff",
			"syntaxString":      "#7ec699",
			"syntaxNumber":      "#f78c6c",
			"syntaxType":        "#89ddff",
			"syntaxOperator":    "#a0a0a0",
			"syntaxPunctuation": "#8b8b8b",

			"thinkingOff":     "#5f5f5f",
			"thinkingMinimal": "#6b7280",
			"thinkingLow":     "#6cb6ff",
			"thinkingMedium":  "#c792ea",
			"thinkingHigh":    "#ffb454",
			"thinkingXhigh":   "#ff8f6b",
			"thinkingMax":     "#ff5fff",

			"bashMode": "#ffb454",

			"scrollbarTrack":  "#5f5f5f",
			"scrollbarThumb":  "#d4d4d4",
			"searchMatchText": "#d4d4d4",
		}),
		Export: pierExport("#101010", "#171717", "#2a2318"),
	}
}

// pierLightJSON is the palette for a light terminal background. The same design
// with the accent and every secondary colour darkened enough to sit on white.
func pierLightJSON() *ThemeJSON {
	return &ThemeJSON{
		Name: "light",
		Colors: pierColors(map[string]string{
			"background":   "#ffffff",
			"text":         "#1f2328",
			"accent":       "#b45309",
			"border":       "#c9c9c9",
			"borderAccent": "#b45309",
			"borderMuted":  "#e0e0e0",
			"success":      "#1a7f37",
			"error":        "#cf222e",
			"warning":      "#9a6700",
			"muted":        "#6b7280",
			"dim":          "#9aa0a6",

			"thinkingText":       "#6b7280",
			"toolTitle":          "#1f2328",
			"toolOutput":         "#6b7280",
			"userMessageText":    "#1f2328",
			"customMessageText":  "#1f2328",
			"customMessageLabel": "#8250df",

			// Warm-neutral while a call runs; the success/error hues as a light tint.
			"toolPendingBg": "#ececec",
			"toolSuccessBg": "#e6f2e9",
			"toolErrorBg":   "#f7e7e7",

			// A warm cream for your own messages, where upstream's is plain grey
			// (#e8e8e8).
			"userMessageBg": "#f4eee1",

			"mdHeading":         "#b45309",
			"mdLink":            "#0969da",
			"mdLinkUrl":         "#9aa0a6",
			"mdCode":            "#b45309",
			"mdCodeBlock":       "#1a7f37",
			"mdCodeBlockBorder": "#c9c9c9",
			"mdQuote":           "#6b7280",
			"mdQuoteBorder":     "#c9c9c9",
			"mdHr":              "#c9c9c9",
			"mdListBullet":      "#b45309",

			"toolDiffAdded":   "#1a7f37",
			"toolDiffRemoved": "#cf222e",
			"toolDiffContext": "#6b7280",

			"syntaxComment":     "#8b949e",
			"syntaxKeyword":     "#8250df",
			"syntaxFunction":    "#b45309",
			"syntaxVariable":    "#0550ae",
			"syntaxString":      "#0a3069",
			"syntaxNumber":      "#953800",
			"syntaxType":        "#116329",
			"syntaxOperator":    "#24292f",
			"syntaxPunctuation": "#57606a",

			"thinkingOff":     "#9aa0a6",
			"thinkingMinimal": "#8b949e",
			"thinkingLow":     "#0550ae",
			"thinkingMedium":  "#8250df",
			"thinkingHigh":    "#b45309",
			"thinkingXhigh":   "#bc4c00",
			"thinkingMax":     "#a3008b",

			"bashMode": "#b45309",

			"scrollbarTrack":  "#c9c9c9",
			"scrollbarThumb":  "#1f2328",
			"searchMatchText": "#1f2328",
		}),
		Export: pierExport("#ffffff", "#f6f8fa", "#fff4e5"),
	}
}

// InstallPierTheme registers the port's palette under the upstream theme names,
// shadowing the embedded dark.json/light.json. The embedded pair stays on disk
// on purpose: it is upstream's reference palette, it is what the upstream-parity
// test corpus renders with (those tests clear the registry first), and it stays
// the fallback for library consumers that never call this.
//
// An invalid palette panics inside theme creation, the same way a malformed
// built-in theme does — this is a programming error, and piertheme_test.go
// pins every palette it installs.
func InstallPierTheme() {
	SetRegisteredThemes([]*Theme{
		CreateTheme(pierDarkJSON(), "", ""),
		CreateTheme(pierLightJSON(), "", ""),
	})
}
