//go:build windows

package tui

import "runtime"

// Platform helpers for the terminal port (windows build). The SIGWINCH-based
// dimension refresh and the resize watcher are unix-specific; on Windows the
// dimensions come from the console API and a watcher is not wired up.

func isDarwin() bool  { return false }
func isWindows() bool { return runtime.GOOS == "windows" }

// nativeShiftPressed is upstream's isNativeModifierPressed("shift"), which
// needs the native platform helper. Out of scope (divergence D52).
func nativeShiftPressed() bool { return false }

func killSelfSIGWINCH() {}

func startResizeWatcher(onResize func()) {}
func stopResizeWatcher()                 {}
