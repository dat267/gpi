//go:build unix

package tui

import (
	"os"
	"os/signal"
	"syscall"
)

func signalNotifySIGWINCH(sig chan os.Signal) {
	signal.Notify(sig, syscall.SIGWINCH)
}

func signalStopSIGWINCH(sig chan os.Signal) {
	signal.Stop(sig)
}
