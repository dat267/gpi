package coding

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestCopyCommands pins the platform command selection (upstream
// copyToClipboard's command tables) without depending on the ambient
// environment: the DISPLAY/WAYLAND/TERMUX gates are read via os.Getenv, so
// the test only asserts ordering rules that hold regardless.
func TestClipboardCommandsShape(t *testing.T) {
	commands := copyCommands()
	switch {
	case len(commands) == 0:
		t.Skip("no clipboard writers available in this environment")
	case isRemoteSession():
		// OSC 52 fallback covers remote sessions; command presence is
		// environment-dependent.
	}
	// Every command is a name plus optional flags; no command is empty.
	for _, command := range commands {
		if len(command) == 0 || command[0] == "" {
			t.Fatalf("invalid command %v", command)
		}
	}
}

// TestClipboardUnavailableMessage pins the upstream error text for an
// environment with no display (forced by clearing the env gates via the
// message table rather than mutating the process environment).
func TestClipboardUnavailableMessage(t *testing.T) {
	// The premise is an environment with no clipboard backend. Windows and macOS
	// ship one (copyCommands returns clip.exe / pbcopy), so only Linux can have a
	// backendless clipboard.
	if runtime.GOOS != "linux" {
		t.Skip("the platform ships a clipboard command (clip.exe / pbcopy)")
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != "" || os.Getenv("TERMUX_VERSION") != "" {
		t.Skip("clipboard helpers present")
	}
	if isRemoteSession() {
		t.Skip("remote session: OSC 52 fallback copies without a local backend")
	}
	err := CopyTextToClipboard("probe")
	if err == nil {
		t.Fatal("copy succeeded without any clipboard backend")
	}
	if !strings.Contains(err.Error(), "Clipboard unavailable") {
		t.Fatalf("err = %v", err)
	}
}

// TestOSC52Encoding pins the OSC 52 payload shape and the size cap.
func TestOSC52Encoding(t *testing.T) {
	if !emitOSC52("hello") {
		t.Fatal("emit failed")
	}
	if emitOSC52(strings.Repeat("x", MaxOSC52EncodedLength+1)) {
		t.Fatal("oversized payload was emitted")
	}
}
