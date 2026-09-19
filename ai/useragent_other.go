//go:build !linux

package ai

import "runtime"

func runtimeGOOS() string   { return runtime.GOOS }
func runtimeGOARCH() string { return runtime.GOARCH }

// kernelRelease falls back to the Go runtime version where uname is not
// wired up for this platform yet (documented D5 limitation).
func kernelRelease() string { return runtime.Version() }
