package coding

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// Port of utils/shell.ts (shell resolution and binary-output sanitizing).

// ShellConfig describes how to invoke a shell.
type ShellConfig struct {
	Shell string
	Args  []string
	// CommandTransport is "argv" (default) or "stdin".
	CommandTransport string
}

// PowerShellArgs are the PowerShell invocation arguments.
var PowerShellArgs = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command"}

// isLegacyWslBashPath detects the System32 bash.exe stub that needs stdin
// transport.
func isLegacyWslBashPath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, "/", "\\"))
	trimmed := strings.TrimSuffix(normalized, "\\bash.exe")
	if !strings.HasSuffix(normalized, "\\bash.exe") {
		return false
	}
	if len(trimmed) < 2 || trimmed[1] != ':' {
		return false
	}
	rest := trimmed[2:]
	return rest == "\\windows\\system32" || rest == "\\windows\\sysnative"
}

func bashShellConfig(shell string) ShellConfig {
	if isLegacyWslBashPath(shell) {
		return ShellConfig{Shell: shell, Args: []string{"-s"}, CommandTransport: "stdin"}
	}
	return ShellConfig{Shell: shell, Args: []string{"-c"}}
}

// findExecutableOnPath resolves an executable the way upstream does: `where`
// on Windows (verifying the file exists) and `which` elsewhere.
func findExecutableOnPath(executable string) string {
	if runtime.GOOS == "windows" {
		output, err := exec.Command("where", executable).Output()
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if _, err := os.Stat(trimmed); err == nil {
				return trimmed
			}
		}
		return ""
	}
	output, err := exec.Command("which", executable).Output()
	if err != nil {
		return ""
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return ""
	}
	return trimmed
}

// GetShellConfig resolves the bash shell configuration (upstream
// getShellConfig).
func GetShellConfig(customShellPath string) (ShellConfig, error) {
	if customShellPath != "" {
		if _, err := os.Stat(customShellPath); err == nil {
			return bashShellConfig(customShellPath), nil
		}
		return ShellConfig{}, fmt.Errorf("Custom shell path not found: %s", customShellPath)
	}

	if runtime.GOOS == "windows" {
		var paths []string
		if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
			paths = append(paths, filepath.Join(programFiles, "Git", "bin", "bash.exe"))
		}
		if programFilesX86 := os.Getenv("ProgramFiles(x86)"); programFilesX86 != "" {
			paths = append(paths, filepath.Join(programFilesX86, "Git", "bin", "bash.exe"))
		}
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				return bashShellConfig(path), nil
			}
		}
		if bashOnPath := findExecutableOnPath("bash.exe"); bashOnPath != "" {
			return bashShellConfig(bashOnPath), nil
		}
		return ShellConfig{}, fmt.Errorf("%s", "No bash shell found. Options:\n"+
			"  1. Install Git for Windows: https://git-scm.com/download/win\n"+
			"  2. Add your bash to PATH (Cygwin, MSYS2, etc.)\n"+
			"  3. Set shellPath in settings.json\n\n"+
			"Searched Git Bash in:\n"+indentLines(paths))
	}

	if _, err := os.Stat("/bin/bash"); err == nil {
		return bashShellConfig("/bin/bash"), nil
	}
	if bashOnPath := findExecutableOnPath("bash"); bashOnPath != "" {
		return bashShellConfig(bashOnPath), nil
	}
	return ShellConfig{Shell: "sh", Args: []string{"-c"}}, nil
}

func indentLines(paths []string) string {
	var builder strings.Builder
	for index, path := range paths {
		if index > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("  " + path)
	}
	return builder.String()
}

// GetPowerShellConfig resolves PowerShell, preferring PowerShell 7 (upstream
// getPowerShellConfig).
func GetPowerShellConfig() (ShellConfig, error) {
	if runtime.GOOS != "windows" {
		return ShellConfig{}, fmt.Errorf("The powershell tool is only available on Windows.")
	}
	shell := findExecutableOnPath("pwsh.exe")
	if shell == "" {
		shell = findExecutableOnPath("powershell.exe")
	}
	if shell == "" {
		return ShellConfig{}, fmt.Errorf("No PowerShell executable found. Install PowerShell or add powershell.exe/pwsh.exe to PATH.")
	}
	return ShellConfig{Shell: shell, Args: append([]string{}, PowerShellArgs...)}, nil
}

// SanitizeBinaryOutput removes characters that crash display handling:
// control characters (except tab, newline, carriage return) and the Unicode
// Format range FFF9-FFFB (upstream sanitizeBinaryOutput).
//
// Clean text — the overwhelming majority — is returned as-is. The sanitizer
// runs over the whole accumulated tool output on every streaming result
// update, so rebuilding it rune by rune cost 670 us per update on a 155 KB
// build log (and quadratic work over a long command).
func SanitizeBinaryOutput(text string) string {
	if !hasBinaryOutput(text) {
		return text
	}
	var builder strings.Builder
	builder.Grow(len(text))
	start := 0
	for i := 0; i < len(text); {
		char, size := utf8.DecodeRuneInString(text[i:])
		if sanitizeDrops(char) || (char == utf8.RuneError && size <= 1) {
			if start < i {
				builder.WriteString(text[start:i])
			}
			if char == utf8.RuneError && size <= 1 {
				// A range over a string yields U+FFFD per invalid byte; keep
				// that substitution exactly.
				builder.WriteRune(utf8.RuneError)
			}
			i += size
			start = i
			continue
		}
		i += size
	}
	if start < len(text) {
		builder.WriteString(text[start:])
	}
	return builder.String()
}

// hasBinaryOutput reports whether SanitizeBinaryOutput would change text.
// ASCII — nearly all shell output — is tested a byte at a time; only non-ASCII
// bytes pay for a rune decode.
func hasBinaryOutput(text string) bool {
	for i := 0; i < len(text); i++ {
		char := text[i]
		if char < utf8.RuneSelf {
			if char < 0x20 && char != 0x09 && char != 0x0a && char != 0x0d {
				return true
			}
			continue
		}
		decoded, size := utf8.DecodeRuneInString(text[i:])
		if sanitizeDrops(decoded) || (decoded == utf8.RuneError && size <= 1) {
			return true
		}
		i += size - 1
	}
	return false
}

// sanitizeDrops reports whether a decoded rune is removed.
func sanitizeDrops(char rune) bool {
	return char <= 0x1f && char != 0x09 && char != 0x0a && char != 0x0d ||
		char >= 0xfff9 && char <= 0xfffb
}
