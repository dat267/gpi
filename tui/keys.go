package tui

import "strings"

// Early slice of the keys.ts port: the Kitty key-release/repeat detectors used
// by the renderer's input routing. The rest of keys.ts (parseKey,
// matchesKey, the Key helper, and the protocol state) lands with the key
// parsing round.

// IsKeyRelease reports whether the input is a Kitty protocol key-release event.
// Release events with flag 2 contain ":3" after the modifier.
func IsKeyRelease(data string) bool {
	// Bracketed paste content must not be treated as a key release, even when
	// it contains patterns like ":3F" (e.g. bluetooth MAC addresses). The
	// terminal re-wraps pasted data with bracketed paste markers, so pasted
	// data always contains \x1b[200~.
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	return containsAny(data,
		":3u", ":3~", ":3A", ":3B", ":3C", ":3D", ":3H", ":3F")
}

// IsKeyRepeat reports whether the input is a Kitty protocol key-repeat event.
// Only meaningful when the Kitty keyboard protocol with flag 2 is active.
func IsKeyRepeat(data string) bool {
	// See IsKeyRelease for why bracketed paste content is excluded.
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	return containsAny(data,
		":2u", ":2~", ":2A", ":2B", ":2C", ":2D", ":2H", ":2F")
}

func containsAny(data string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(data, needle) {
			return true
		}
	}
	return false
}
