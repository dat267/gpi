package interactive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// SwitchSession replaces the running session with the one at sessionPath
// (upstream AgentSessionRuntime.switchSession plus the interactive-mode
// rebind).
func (a *App) switchSession(ctx context.Context, sessionPath string, cwdOverride string) (*SessionSwitchResult, error) {
	sessionManager, err := coding.OpenSession(sessionPath, "", cwdOverride)
	if err != nil {
		return nil, err
	}
	if err := coding.AssertSessionCwdExists(sessionManager, a.options.Cwd); err != nil {
		return nil, err
	}
	return a.applySessionReplacement(sessionManager)
}

// importFromJSONL imports a session file into the current session directory and
// switches to it (upstream AgentSessionRuntime.importFromJsonl). The file is
// copied rather than opened in place so the imported session becomes part of
// this project's session list; an existing session with the same name is never
// clobbered — the copy takes a numbered name instead.
func (a *App) importFromJSONL(_ context.Context, inputPath string, cwdOverride string) (*SessionSwitchResult, error) {
	resolved := coding.ResolvePath(inputPath, "", coding.PathInputOptions{})
	if _, err := os.Stat(resolved); err != nil {
		return nil, fmt.Errorf("File not found: %s", resolved)
	}

	sessionDir := a.sessionMgr.GetSessionDir()
	if sessionDir == "" {
		// An in-memory session (--no-session) has no directory of its own yet;
		// an import has to land in the default one, the same place a persisted
		// manager would have resolved at creation.
		sessionDir = coding.DefaultSessionDir(a.sessionMgr.GetCwd(), "")
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, err
	}
	destination := filepath.Join(sessionDir, filepath.Base(resolved))
	alreadyStored := filepath.Clean(destination) == filepath.Clean(resolved)
	if !alreadyStored {
		extension := filepath.Ext(destination)
		stem := strings.TrimSuffix(destination, extension)
		for suffix := 1; ; suffix++ {
			if _, err := os.Stat(destination); err != nil {
				break
			}
			destination = fmt.Sprintf("%s-%d%s", stem, suffix, extension)
		}
		if err := copyFileExclusive(resolved, destination); err != nil {
			return nil, err
		}
	}

	sessionManager, err := coding.OpenSession(destination, sessionDir, cwdOverride)
	if err != nil {
		return nil, err
	}
	if err := coding.AssertSessionCwdExists(sessionManager, a.options.Cwd); err != nil {
		return nil, err
	}
	return a.applySessionReplacement(sessionManager)
}

// copyFileExclusive copies src to dst, refusing to overwrite (upstream's
// COPYFILE_EXCL).
func copyFileExclusive(src string, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return err
	}
	return destination.Close()
}

// SessionNew starts a fresh session in the current session directory (upstream
// AgentSessionRuntime.newSession).
func (a *App) sessionNew(ctx context.Context) (*SessionSwitchResult, error) {
	sessionDir := a.sessionMgr.GetSessionDir()
	persist := a.sessionMgr.IsPersisted()
	sessionManager := coding.NewSessionManager(a.sessionMgr.GetCwd(), &coding.SessionManagerOptions{
		SessionDir: sessionDir,
		Persist:    &persist,
		WriteQueue: a.sessionMgr.GetWriteQueue(),
	})
	return a.applySessionReplacement(sessionManager)
}

// applySessionReplacement tears down the current session, builds a new agent
// session around sessionManager and rebinds the UI (upstream teardownCurrent +
// createRuntime + rebindCurrentSession; the extension lifecycle events are
// no-ops in this port, D41).
func (a *App) applySessionReplacement(sessionManager *coding.SessionManager) (*SessionSwitchResult, error) {
	// teardownCurrent: settle any active response, dispose, unsubscribe.
	a.session.Abort(context.Background())
	a.session.Dispose()
	if a.unsubscribe != nil {
		a.unsubscribe()
		a.unsubscribe = nil
	}

	// The runtime is cwd-bound: upstream's createRuntime resolves the session's
	// cwd's project trust and builds a settings manager for it, so a session from
	// another directory gets that directory's settings, resources and trust
	// rather than this project's. Resolving here — without a prompt, because
	// upstream passes hasUI false for every runtime but the initial one — and
	// re-pointing the one settings manager (D160) keeps every holder consistent.
	if err := a.rebindProjectSettings(sessionManager.GetCwd()); err != nil {
		return nil, err
	}

	created, err := coding.CreateAgentSession(context.Background(), &coding.CreateAgentSessionOptions{
		Cwd:             sessionManager.GetCwd(),
		AgentDir:        a.options.AgentDir,
		SessionManager:  sessionManager,
		ModelRuntime:    a.runtime,
		SettingsManager: a.settings,
	})
	if err != nil {
		return nil, err
	}

	// The *AppSession pointer is shared by every wiring, so swapping the
	// embedded AgentSession rebinds them all at once.
	a.session.AgentSession = created.Session
	a.sessionMgr = sessionManager
	a.footerData.SetCwd(sessionManager.GetCwd())
	a.transcript.SessionInfo = sessionManager
	a.events.SessionInfo = sessionManager
	a.startup.SessionInfo = sessionManager
	a.sessions.SessionInfo = sessionManager
	a.commands.SessionInfo = sessionManager
	a.selectors.SessionInfo = sessionManager
	a.trust.SessionInfo = sessionManager
	a.autocomplete.SessionInfo = sessionManager

	if a.unsubscribe == nil {
		a.unsubscribe = a.session.Subscribe(func(event *coding.SessionEvent) {
			a.events.HandleEvent(event)
		})
	}
	a.footer.Invalidate()
	// upstream rebindCurrentSession({renderBeforeBind: true}) → the session's own
	// reset, which is the initial render and not the reload's rebuild.
	a.startup.RenderCurrentSessionState()
	a.ui.RequestRender(false)
	return &SessionSwitchResult{}, nil
}

// rebindProjectSettings re-points the settings manager at a session's project,
// resolving that project's trust first. A switch within the same project, or one
// with no directory to resolve, keeps the current settings.
func (a *App) rebindProjectSettings(cwd string) error {
	if cwd == "" || cwd == a.settings.Cwd() {
		return nil
	}
	key := coding.CanonicalizePath(coding.ResolvePath(cwd, "", coding.PathInputOptions{}))
	if known, ok := a.projectTrustByCwd[key]; ok {
		a.applyProjectSettings(cwd, known)
		return nil
	}
	trusted, err := coding.ResolveProjectTrusted(coding.ResolveProjectTrustedOptions{
		Cwd:                 cwd,
		TrustStore:          coding.NewProjectTrustStore(a.options.AgentDir),
		TrustOverride:       a.options.ProjectTrustOverride,
		DefaultProjectTrust: a.settings.GetDefaultProjectTrust(),
		// No UI: a switch is never the startup prompt (upstream's
		// projectTrustContextFactory reports hasUI false off the initial runtime),
		// so an undecided project stays untrusted.
		ProjectTrustContext: coding.ProjectTrustContext{},
	})
	if err != nil {
		return err
	}
	a.projectTrustByCwd[key] = trusted
	a.applyProjectSettings(cwd, trusted)
	return nil
}

// applyProjectSettings points the settings manager at a project under a decided
// trust and re-applies what follows from the swap.
func (a *App) applyProjectSettings(cwd string, trusted bool) {
	a.settings.RebindProject(cwd, trusted)
	// The run's own override is not part of the project scope, so re-apply it
	// (upstream applies the CLI overrides to the runtime settings manager).
	if a.options.InitialThemeSetting != nil {
		a.settings.ApplyOverrides(&coding.Settings{Theme: a.options.InitialThemeSetting})
	}
	// Settings-derived UI state follows the manager (upstream rebindCurrentSession
	// calls applyRuntimeSettings).
	a.applySettingsDependentUI()
}

// forkAtEntry is the runtime's fork (upstream AgentSessionRuntime.fork), shared
// by /clone and by forking from a user message: it branches a new session at the
// given entry and switches the app to it.
//
// The two positions are upstream's: "at" keeps the entry (clone at the current
// position, so the new leaf is that entry), while "before" requires a user
// message, keeps its parent as the leaf and hands the message text back so the
// editor can send it again.
func (a *App) forkAtEntry(_ context.Context, entryID string, atPosition bool) (*SelectorForkResult, error) {
	entry := a.sessionMgr.GetEntry(entryID)
	if entry == nil {
		return nil, errors.New("Invalid entry ID for forking")
	}

	targetLeafID := entryID
	var selectedText *string
	if !atPosition {
		text, ok := userMessageEntryText(entry)
		if !ok {
			return nil, errors.New("Invalid entry ID for forking")
		}
		selectedText = &text
		targetLeafID = ""
		if entry.ParentID != nil {
			targetLeafID = *entry.ParentID
		}
	}

	forked, err := coding.ForkSessionAtEntry(a.sessionMgr, targetLeafID, a.sessionMgr.GetCwd(), a.sessionMgr.GetSessionDir(), nil)
	if err != nil {
		return nil, err
	}
	result, err := a.applySessionReplacement(forked)
	if err != nil {
		return nil, err
	}
	return &SelectorForkResult{Cancelled: result.Cancelled, SelectedText: selectedText}, nil
}

// userMessageEntryText returns a message entry's user text, and whether the
// entry is a user message at all.
func userMessageEntryText(entry *coding.SessionEntry) (string, bool) {
	if entry.Type != "message" {
		return "", false
	}
	message, err := ai.UnmarshalMessage(entry.Message)
	if err != nil {
		return "", false
	}
	user, ok := message.(*ai.UserMessage)
	if !ok {
		return "", false
	}
	return user.Content.Text, true
}
