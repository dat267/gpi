package interactive

import (
	"context"
	"fmt"
	"os"

	"github.com/dat267/pier/coding"
)

// SwitchSession replaces the running session with the one at sessionPath
// (upstream AgentSessionRuntime.switchSession plus the interactive-mode
// rebind).
func (a *App) SwitchSession(ctx context.Context, sessionPath string, cwdOverride string) (*SessionSwitchResult, error) {
	sessionManager, err := coding.OpenSession(sessionPath, "", cwdOverride)
	if err != nil {
		return nil, err
	}
	// Upstream assertSessionCwdExists: the error names the cwd so the resume
	// flow can prompt for a replacement directory.
	if _, statErr := os.Stat(sessionManager.GetCwd()); statErr != nil {
		return nil, fmt.Errorf("session cwd %s does not exist", sessionManager.GetCwd())
	}
	return a.applySessionReplacement(sessionManager)
}

// SessionNew starts a fresh session in the current session directory (upstream
// AgentSessionRuntime.newSession).
func (a *App) SessionNew(ctx context.Context) (*SessionSwitchResult, error) {
	sessionDir := a.SessionMgr.GetSessionDir()
	persist := a.SessionMgr.IsPersisted()
	sessionManager := coding.NewSessionManager(a.SessionMgr.GetCwd(), &coding.SessionManagerOptions{
		SessionDir: sessionDir,
		Persist:    &persist,
	})
	return a.applySessionReplacement(sessionManager)
}

// applySessionReplacement tears down the current session, builds a new agent
// session around sessionManager and rebinds the UI (upstream teardownCurrent +
// createRuntime + rebindCurrentSession; the extension lifecycle events are
// no-ops in this port, D41).
func (a *App) applySessionReplacement(sessionManager *coding.SessionManager) (*SessionSwitchResult, error) {
	// teardownCurrent: settle any active response, dispose, unsubscribe.
	a.Session.Abort(context.Background())
	a.Session.Dispose()
	if a.unsubscribe != nil {
		a.unsubscribe()
		a.unsubscribe = nil
	}

	created, err := coding.CreateAgentSession(context.Background(), &coding.CreateAgentSessionOptions{
		Cwd:             sessionManager.GetCwd(),
		AgentDir:        a.options.AgentDir,
		SessionManager:  sessionManager,
		ModelRuntime:    a.Runtime,
		SettingsManager: a.Settings,
	})
	if err != nil {
		return nil, err
	}

	// The *AppSession pointer is shared by every wiring, so swapping the
	// embedded AgentSession rebinds them all at once.
	a.Session.AgentSession = created.Session
	a.SessionMgr = sessionManager
	a.Transcript.SessionInfo = sessionManager
	a.Events.SessionInfo = sessionManager
	a.Startup.SessionInfo = sessionManager
	a.Sessions.SessionInfo = sessionManager
	a.Commands.SessionInfo = sessionManager
	a.Selectors.SessionInfo = sessionManager
	a.Trust.SessionInfo = sessionManager
	a.Autocomplete.SessionInfo = sessionManager

	if a.unsubscribe == nil {
		a.unsubscribe = a.Session.Subscribe(func(event *coding.SessionEvent) {
			a.Events.HandleEvent(event)
		})
	}
	a.Footer.Invalidate()
	a.Startup.RebuildChatFromMessages()
	a.UI.RequestRender(false)
	return &SessionSwitchResult{}, nil
}
