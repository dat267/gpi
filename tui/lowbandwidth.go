package tui

import "sync/atomic"

// lowBandwidthEnabled is the process-wide low-bandwidth render toggle.
var lowBandwidthEnabled atomic.Bool

// SetLowBandwidth toggles the low-bandwidth render path. When on, the renderer
// trims the trailing padding that styled lines carry and skips line clears the
// screen clear already performed, without changing the visible result. It
// exists for SSH/serial links, where every byte written crosses the network
// (D163: one keystroke measured 127 bytes padded, 48 trimmed).
//
// Default off so the upstream-parity goldens keep asserting the padded bytes.
// The interactive mode enables it when it detects a remote session
// (SSH_CONNECTION/SSH_TTY) unless PIER_LOW_BANDWIDTH overrides.
func SetLowBandwidth(enabled bool) { lowBandwidthEnabled.Store(enabled) }

// LowBandwidth reports whether the low-bandwidth render path is active.
func LowBandwidth() bool { return lowBandwidthEnabled.Load() }
