package coding

import (
	"fmt"
	"os"
)

// Port of core/session-cwd.ts: detecting a stored session working directory
// that no longer exists.

// SessionCwdIssue describes a session file whose stored cwd is gone.
type SessionCwdIssue struct {
	SessionFile string
	SessionCwd  string
	FallbackCwd string
}

// SessionCwdSource is the minimal session-manager surface needed.
type SessionCwdSource interface {
	GetCwd() string
	GetSessionFile() string
}

// GetMissingSessionCwdIssue reports the issue when the session's stored working
// directory does not exist.
func GetMissingSessionCwdIssue(sessionManager SessionCwdSource, fallbackCwd string) *SessionCwdIssue {
	sessionFile := sessionManager.GetSessionFile()
	if sessionFile == "" {
		return nil
	}
	sessionCwd := sessionManager.GetCwd()
	if sessionCwd == "" {
		return nil
	}
	if _, err := os.Stat(sessionCwd); err == nil {
		return nil
	}
	return &SessionCwdIssue{SessionFile: sessionFile, SessionCwd: sessionCwd, FallbackCwd: fallbackCwd}
}

// FormatMissingSessionCwdError renders the CLI error text.
func FormatMissingSessionCwdError(issue SessionCwdIssue) string {
	sessionFile := ""
	if issue.SessionFile != "" {
		sessionFile = fmt.Sprintf("\nSession file: %s", issue.SessionFile)
	}
	return fmt.Sprintf("Stored session working directory does not exist: %s%s\nCurrent working directory: %s",
		issue.SessionCwd, sessionFile, issue.FallbackCwd)
}

// FormatMissingSessionCwdPrompt renders the interactive confirmation prompt.
func FormatMissingSessionCwdPrompt(issue SessionCwdIssue) string {
	return fmt.Sprintf("cwd from session file does not exist\n%s\n\ncontinue in current cwd\n%s",
		issue.SessionCwd, issue.FallbackCwd)
}

// MissingSessionCwdError is returned by AssertSessionCwdExists.
type MissingSessionCwdError struct {
	Issue SessionCwdIssue
}

func (e *MissingSessionCwdError) Error() string {
	return FormatMissingSessionCwdError(e.Issue)
}

// AssertSessionCwdExists fails when the session's stored cwd is missing.
func AssertSessionCwdExists(sessionManager SessionCwdSource, fallbackCwd string) error {
	issue := GetMissingSessionCwdIssue(sessionManager, fallbackCwd)
	if issue != nil {
		return &MissingSessionCwdError{Issue: *issue}
	}
	return nil
}
