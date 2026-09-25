//go:build windows

package tui

import (
	"runtime"
	"sync"
	"time"
)

// Platform helpers for the terminal port (windows build). There is no
// SIGWINCH, so the size cache is refreshed by a poller that runs off the UI
// loop (a console size query can block while a mouse selection is active).
// nativeShiftPressed is upstream's isNativeModifierPressed("shift"), which
// needs the native platform helper; it is out of scope (divergence D52).

// windowsResizePollInterval is how often the poller asks the console for its
// size. It runs on its own goroutine, so the cost is not on the UI loop; 200ms
// keeps a manual resize feeling responsive without busy-polling.
const windowsResizePollInterval = 200 * time.Millisecond

func isDarwin() bool  { return false }
func isWindows() bool { return runtime.GOOS == "windows" }

func nativeShiftPressed() bool { return false }

func killSelfSIGWINCH() {}

var (
	resizeWatcherMu   sync.Mutex
	resizeWatcherStop chan struct{}
	resizeWatcherDone chan struct{}
)

// startResizeWatcher polls the console size off the UI loop and calls
// onChange; the ProcessTerminal wrapper repaints only when the size actually
// changed.
func startResizeWatcher(onChange func()) {
	stopResizeWatcher()

	stop := make(chan struct{})
	done := make(chan struct{})
	resizeWatcherMu.Lock()
	resizeWatcherStop = stop
	resizeWatcherDone = done
	resizeWatcherMu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(windowsResizePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if onChange != nil {
					onChange()
				}
			}
		}
	}()
}

func stopResizeWatcher() {
	resizeWatcherMu.Lock()
	stop, done := resizeWatcherStop, resizeWatcherDone
	resizeWatcherStop, resizeWatcherDone = nil, nil
	resizeWatcherMu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
}
