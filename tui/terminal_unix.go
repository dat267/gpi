//go:build unix

package tui

import (
	"os"
	"runtime"
	"syscall"
)

// Platform helpers for the terminal port (unix build).

func isDarwin() bool  { return runtime.GOOS == "darwin" }
func isWindows() bool { return runtime.GOOS == "windows" }

// nativeShiftPressed is upstream's isNativeModifierPressed("shift"), which
// needs the native platform helper (C/ObjC). Out of scope (divergence D52);
// native Shift+Enter detection stays disabled.
func nativeShiftPressed() bool { return false }

// killSelfSIGWINCH sends SIGWINCH to this process. Best-effort: restricted
// seccomp or LSM policies may return EACCES for kill(2); the refresh is
// skipped rather than crashing (upstream regression test:
// regression-sigwinch-kill-eacces).
func killSelfSIGWINCH() {
	if os.Getpid() <= 0 {
		return
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGWINCH)
}

// resizeWatcher delivers SIGWINCH-driven resize notifications.
var resizeWatcher struct {
	quit chan struct{}
	done chan struct{}
}

func startResizeWatcher(onResize func()) {
	stopResizeWatcher()
	resizeWatcher.quit = make(chan struct{})
	resizeWatcher.done = make(chan struct{})
	sig := make(chan os.Signal, 1)
	signalNotifySIGWINCH(sig)
	go func() {
		defer close(resizeWatcher.done)
		for {
			select {
			case <-resizeWatcher.quit:
				signalStopSIGWINCH(sig)
				return
			case <-sig:
				if onResize != nil {
					onResize()
				}
			}
		}
	}()
}

func stopResizeWatcher() {
	if resizeWatcher.quit != nil {
		close(resizeWatcher.quit)
		<-resizeWatcher.done
		resizeWatcher.quit = nil
		resizeWatcher.done = nil
	}
}
