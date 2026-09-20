//go:build windows

package tui

import "os"

func signalNotifySIGWINCH(sig chan os.Signal) {}
func signalStopSIGWINCH(sig chan os.Signal)   {}
