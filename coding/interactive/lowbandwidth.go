package interactive

import (
	"os"
	"strings"

	"github.com/dat267/pier/tui"
)

// ConfigureLowBandwidth enables the low-bandwidth render path when the process
// runs over a remote shell, unless PIER_LOW_BANDWIDTH overrides it. The CLI
// calls it once at boot; the renderer reads the resulting global at paint time.
func ConfigureLowBandwidth() bool {
	enabled := detectLowBandwidth(os.Getenv)
	tui.SetLowBandwidth(enabled)
	return enabled
}

// detectLowBandwidth decides the default from the environment. An explicit
// PIER_LOW_BANDWIDTH wins ("0" forces the padded upstream-parity output, "1"
// forces the trimmed output); otherwise SSH means optimize.
func detectLowBandwidth(env func(string) string) bool {
	if env == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(env("PIER_LOW_BANDWIDTH"))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return env("SSH_CONNECTION") != "" || env("SSH_TTY") != ""
}
