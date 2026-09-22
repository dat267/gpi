package coding

// Port of packages/coding-agent/src/utils/clipboard.ts and
// clipboard-command.ts: system clipboard copy/read via platform commands,
// with OSC 52 fallback for remote sessions. Upstream also tries a native
// (Bun FFI) clipboard first; the Go port has no native clipboard and relies
// on the platform commands (divergence D106).

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// MaxOSC52EncodedLength matches upstream's OSC 52 payload cap.
const MaxOSC52EncodedLength = 100_000

type clipboardCommandOptions struct {
	input     string
	timeoutMS int
}

// runClipboardCommand runs one clipboard helper. A nil result means the
// command failed; an empty result is a success (upstream runClipboardCommand).
// Writers are not given output pipes so daemons cannot retain them.
func runClipboardCommand(ctx context.Context, command string, args []string, options clipboardCommandOptions) []byte {
	timeoutMS := options.timeoutMS
	if timeoutMS == 0 {
		timeoutMS = 3000
	}
	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, command, args...)
	if options.input == "" {
		cmd.Stdin = nil
	} else {
		cmd.Stdin = strings.NewReader(options.input)
		cmd.Stdout = nil
	}
	cmd.Stderr = nil
	cmd.Env = os.Environ()
	if options.input == "" {
		output, err := cmd.Output()
		if err != nil {
			return nil
		}
		return output
	}
	if err := cmd.Run(); err != nil {
		return nil
	}
	return []byte{}
}

func isRemoteSession() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_CLIENT") != "" || os.Getenv("MOSH_CONNECTION") != "" {
		return true
	}
	return false
}

func emitOSC52(text string) bool {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if len(encoded) > MaxOSC52EncodedLength {
		return false
	}
	_, err := os.Stdout.WriteString("\x1b]52;c;" + encoded + "\x07")
	return err == nil
}

func copyCommands() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "windows":
		return [][]string{{"clip"}}
	}
	commands := [][]string{}
	if os.Getenv("TERMUX_VERSION") != "" {
		commands = append(commands, []string{"termux-clipboard-set"})
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		commands = append(commands, []string{"wl-copy"})
	}
	if os.Getenv("DISPLAY") != "" {
		commands = append(commands, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "--clipboard", "--input"})
	}
	return commands
}

func pasteCommands() [][]string {
	if runtime.GOOS != "linux" {
		return nil
	}
	commands := [][]string{}
	if os.Getenv("TERMUX_VERSION") != "" {
		commands = append(commands, []string{"termux-clipboard-get"})
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		commands = append(commands, []string{"wl-paste", "--no-newline", "--type", "text"})
	}
	if os.Getenv("DISPLAY") != "" {
		commands = append(commands, []string{"xclip", "-selection", "clipboard", "-out"}, []string{"xsel", "--clipboard", "--output"})
	}
	return commands
}

func clipboardUnavailableError() error {
	if runtime.GOOS == "linux" {
		if os.Getenv("TERMUX_VERSION") != "" {
			return fmt.Errorf("Clipboard unavailable: install the Termux:API app and `termux-api` package")
		}
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return fmt.Errorf("Clipboard unavailable: install `wl-clipboard` (`wl-copy`) or check Wayland access")
		}
		if os.Getenv("DISPLAY") != "" {
			return fmt.Errorf("Clipboard unavailable: install `xclip` or `xsel`, or check X11 access")
		}
		return fmt.Errorf("Clipboard unavailable: no Wayland or X11 display detected")
	}
	return fmt.Errorf("Clipboard unavailable")
}

// CopyTextToClipboard copies text to the system clipboard (upstream
// copyToClipboard): platform command writers, then OSC 52 for remote
// sessions.
func CopyTextToClipboard(text string) error {
	copied := false
	for _, command := range copyCommands() {
		if runClipboardCommand(context.Background(), command[0], command[1:], clipboardCommandOptions{input: text, timeoutMS: 5000}) != nil {
			copied = true
			break
		}
	}
	if !copied && isRemoteSession() {
		copied = emitOSC52(text)
	}
	if !copied {
		return clipboardUnavailableError()
	}
	return nil
}

// ReadClipboardText reads plain text from the system clipboard (upstream
// readClipboardText; the native clipboard fallback has no Go counterpart).
// Empty text returns ("", nil) like upstream's null.
func ReadClipboardText() (string, error) {
	for _, command := range pasteCommands() {
		if bytes := runClipboardCommand(context.Background(), command[0], command[1:], clipboardCommandOptions{timeoutMS: 5000}); bytes != nil {
			return string(bytes), nil
		}
	}
	return "", nil
}
