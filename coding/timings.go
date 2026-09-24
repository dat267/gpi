package coding

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Port of core/timings.ts: central startup timing instrumentation, enabled with
// PI_TIMING=1. Upstream reads the flag once at module load; the Go port reads it
// per call so tests can toggle it (D30). `cmd` marks the boot phases with Time
// and prints the table before entering the run loop, as upstream's main.ts does.

// TimingNamespace groups timings ("main" and "extensions" upstream).
type TimingNamespace string

const (
	// TimingMain is the default namespace.
	TimingMain TimingNamespace = "main"
	// TimingExtensions is the extension-loading namespace.
	TimingExtensions TimingNamespace = "extensions"
)

// timingEntry is one labeled measurement.
type timingEntry struct {
	Label string
	Ms    int64
}

type timingState struct {
	timings  []timingEntry
	lastTime time.Time
}

var (
	timingsMu         sync.Mutex
	timingNamespaces            = map[TimingNamespace]*timingState{}
	timingOut         io.Writer = os.Stderr
	timingNow                   = time.Now
	timingsEnabledVar           = ""
)

// SetTimingsOutput overrides the timings writer (defaults to stderr).
func SetTimingsOutput(writer io.Writer) {
	timingsMu.Lock()
	timingOut = writer
	timingsMu.Unlock()
}

// SetTimingsEnabled overrides the PI_TIMING flag; enabled nil restores the
// environment read.
func SetTimingsEnabled(enabled *bool) {
	timingsMu.Lock()
	if enabled == nil {
		timingsEnabledVar = ""
	} else if *enabled {
		timingsEnabledVar = "1"
	} else {
		timingsEnabledVar = "0"
	}
	timingsMu.Unlock()
}

func timingsEnabled() bool {
	timingsMu.Lock()
	override := timingsEnabledVar
	timingsMu.Unlock()
	if override != "" {
		return override == "1"
	}
	return os.Getenv("PI_TIMING") == "1"
}

// ResetTimings starts a fresh timing namespace.
func ResetTimings(namespace TimingNamespace) {
	if !timingsEnabled() {
		return
	}
	timingsMu.Lock()
	timingNamespaces[namespace] = &timingState{lastTime: timingNow()}
	timingsMu.Unlock()
}

// Time records the elapsed time since the namespace's previous mark.
func Time(label string, namespace TimingNamespace) {
	if !timingsEnabled() {
		return
	}
	now := timingNow()
	timingsMu.Lock()
	state, ok := timingNamespaces[namespace]
	if !ok {
		state = &timingState{lastTime: now}
		timingNamespaces[namespace] = state
	}
	state.timings = append(state.timings, timingEntry{Label: label, Ms: now.Sub(state.lastTime).Milliseconds()})
	state.lastTime = now
	timingsMu.Unlock()
}

// PrintTimings writes every recorded namespace to the timings writer.
func PrintTimings() {
	if !timingsEnabled() {
		return
	}
	timingsMu.Lock()
	namespaces := make([]TimingNamespace, 0, len(timingNamespaces))
	for namespace := range timingNamespaces {
		namespaces = append(namespaces, namespace)
	}
	// Stable namespace order: main before extensions, then alphabetical.
	sortTimingNamespaces(namespaces)
	snapshots := make([]timingState, 0, len(namespaces))
	for _, namespace := range namespaces {
		state := timingNamespaces[namespace]
		snapshots = append(snapshots, timingState{timings: append([]timingEntry{}, state.timings...)})
	}
	writer := timingOut
	timingsMu.Unlock()

	for index, namespace := range namespaces {
		printTimingGroup(writer, fmt.Sprintf("Startup Timings: %s", namespace), snapshots[index].timings)
	}
}

func sortTimingNamespaces(namespaces []TimingNamespace) {
	for outer := 1; outer < len(namespaces); outer++ {
		for inner := outer; inner > 0; inner-- {
			left, right := namespaces[inner-1], namespaces[inner]
			if timingNamespaceOrder(left) <= timingNamespaceOrder(right) {
				break
			}
			namespaces[inner-1], namespaces[inner] = namespaces[inner], namespaces[inner-1]
		}
	}
}

func timingNamespaceOrder(namespace TimingNamespace) string {
	return string(namespace)
}

// printTimingGroup renders one namespace exactly as upstream does.
func printTimingGroup(writer io.Writer, title string, timings []timingEntry) {
	var printable []timingEntry
	for _, timing := range timings {
		if timing.Ms >= 0 {
			printable = append(printable, timing)
		}
	}
	if len(printable) == 0 {
		return
	}
	fmt.Fprintf(writer, "\n--- %s ---\n", title)
	total := int64(0)
	for _, timing := range printable {
		fmt.Fprintf(writer, "  %s: %dms\n", timing.Label, timing.Ms)
		total += timing.Ms
	}
	fmt.Fprintf(writer, "  TOTAL: %dms\n", total)
	fmt.Fprintf(writer, "%s\n\n", repeatDash(len(title)+8))
}

func repeatDash(count int) string {
	out := make([]byte, count)
	for index := range out {
		out[index] = '-'
	}
	return string(out)
}

// AreExperimentalFeaturesEnabled reports PI_EXPERIMENTAL=1
// (core/experimental.ts).
func AreExperimentalFeaturesEnabled() bool {
	return os.Getenv("PI_EXPERIMENTAL") == "1"
}
