package tui

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Port of src/terminal-colors.ts and the terminal color queries of src/tui.ts.

// RgbColor is an RGB color.
type RgbColor struct {
	R int
	G int
	B int
}

// TerminalColorScheme is the terminal's light/dark preference.
type TerminalColorScheme = string

// Terminal color schemes.
const (
	TerminalColorSchemeDark  TerminalColorScheme = "dark"
	TerminalColorSchemeLight TerminalColorScheme = "light"
)

var (
	osc11BackgroundColorResponsePattern = regexp.MustCompile(`(?is)^\x1b\]11;([^\x07\x1b]*)(?:\x07|\x1b\\)$`)
	colorSchemeReportPattern            = regexp.MustCompile(`^(?:\x1b\[\?997;(1|2)n)+$`)
	oscHexChannelPattern                = regexp.MustCompile(`(?i)^[0-9a-f]+$`)
)

// IsOsc11BackgroundColorResponse reports whether data is an OSC 11 reply.
func IsOsc11BackgroundColorResponse(data string) bool {
	return osc11BackgroundColorResponsePattern.MatchString(data)
}

// ParseOsc11BackgroundColor parses an OSC 11 background-color reply.
func ParseOsc11BackgroundColor(data string) (RgbColor, bool) {
	match := osc11BackgroundColorResponsePattern.FindStringSubmatch(data)
	if match == nil {
		return RgbColor{}, false
	}
	value := strings.TrimSpace(match[1])
	if strings.HasPrefix(value, "#") {
		hex := value[1:]
		if len(hex) == 6 && oscHexChannelPattern.MatchString(hex) {
			r, _ := strconv.ParseInt(hex[0:2], 16, 32)
			g, _ := strconv.ParseInt(hex[2:4], 16, 32)
			b, _ := strconv.ParseInt(hex[4:6], 16, 32)
			return RgbColor{R: int(r), G: int(g), B: int(b)}, true
		}
		if len(hex) == 12 && oscHexChannelPattern.MatchString(hex) {
			r, okR := parseOscHexChannel(hex[0:4])
			g, okG := parseOscHexChannel(hex[4:8])
			b, okB := parseOscHexChannel(hex[8:12])
			if !okR || !okG || !okB {
				return RgbColor{}, false
			}
			return RgbColor{R: r, G: g, B: b}, true
		}
		return RgbColor{}, false
	}

	rgbValue := regexp.MustCompile(`(?i)^rgba?:`).ReplaceAllString(value, "")
	parts := strings.Split(rgbValue, "/")
	if len(parts) < 3 {
		return RgbColor{}, false
	}
	r, okR := parseOscHexChannel(parts[0])
	g, okG := parseOscHexChannel(parts[1])
	b, okB := parseOscHexChannel(parts[2])
	if !okR || !okG || !okB {
		return RgbColor{}, false
	}
	return RgbColor{R: r, G: g, B: b}, true
}

// ParseTerminalColorSchemeReport parses a `CSI ? 997 ; n n` color-scheme report.
func ParseTerminalColorSchemeReport(data string) (TerminalColorScheme, bool) {
	match := colorSchemeReportPattern.FindStringSubmatch(data)
	if match == nil {
		return "", false
	}
	if match[1] == "2" {
		return TerminalColorSchemeLight, true
	}
	return TerminalColorSchemeDark, true
}

func parseOscHexChannel(channel string) (int, bool) {
	if !oscHexChannelPattern.MatchString(channel) {
		return 0, false
	}
	max := math.Pow(16, float64(len(channel))) - 1
	if max <= 0 {
		return 0, false
	}
	parsed, err := strconv.ParseInt(channel, 16, 64)
	if err != nil {
		return 0, false
	}
	return int(math.Round(float64(parsed) / max * 255)), true
}
