// Command pier is the Go port's interactive coding-agent CLI.
//
// It is a pragmatic entrypoint: it boots the ported session/services (settings,
// auth, model runtime, agent session) and runs the ported interactive mode
// (coding/interactive). The upstream print/json/rpc modes, package manager,
// extensions and migrations are out of scope (see README).
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/coding/interactive"
)

// executableName returns the invoked binary name (without the .exe suffix),
// falling back to the upstream product name.
func executableName() string {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if name == "" || name == "." || name == string(filepath.Separator) {
		return coding.AppName
	}
	return name
}

func main() {
	appName := executableName()
	args := coding.ParseArgs(os.Args[1:])

	if args.Help {
		fmt.Print(coding.PrintHelpNamed(appName))
		return
	}
	if args.Version {
		fmt.Println(coding.Version)
		return
	}
	if args.Print || args.Mode == coding.CLIModeJSON || args.Mode == coding.CLIModeRPC {
		fmt.Fprintln(os.Stderr, appName+": print, json and rpc modes are not supported by this build")
		os.Exit(1)
	}

	if err := run(appName, args); err != nil {
		fmt.Fprintln(os.Stderr, appName+": "+err.Error())
		os.Exit(1)
	}
}

func run(appName string, args *coding.Args) error {
	ctx := context.Background()
	// Bounds the create-time catalog refresh (upstream leaves it unbounded; the
	// Go refresh is synchronous, so it needs a ceiling).
	modelRefreshTimeoutMS := int64(15000)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	agentDir := coding.GetAgentDir()

	settings := coding.NewSettingsManagerFromFiles(cwd, agentDir, coding.SettingsManagerCreateOptions{})

	// Theme: initialize before any TUI (the -r picker runs before the app).
	themeName := "dark"
	if setting := settings.GetTheme(); setting != nil && *setting != "" {
		themeName = *setting
	}
	interactive.SetTrueColorSupport(true)
	interactive.SetStyleColorsEnabled(true)
	interactive.InitTheme(themeName, false)

	// Session manager: resume the newest session or start a fresh one.
	var sessions *coding.SessionManager
	if args.Resume || args.Continue || args.Fork != nil || args.Session != nil || args.SessionID != nil {
		sessions, err = resumeSession(args, cwd, agentDir, settings)
		if err != nil {
			if errors.Is(err, errNoSessionSelected) {
				fmt.Println("\x1b[2mNo session selected\x1b[0m")
				return nil
			}
			if errors.Is(err, errSessionAborted) {
				fmt.Println("\x1b[2mAborted.\x1b[0m")
				return nil
			}
			return err
		}
	}
	if sessions == nil {
		options := &coding.SessionManagerOptions{}
		if args.SessionDir != nil {
			options.SessionDir = *args.SessionDir
		}
		if args.NoSession {
			persist := false
			options.Persist = &persist
		}
		sessions = coding.NewSessionManager(cwd, options)
	}

	// Model runtime backed by auth.json. Upstream refreshes the catalogs at
	// create (refreshOnCreate defaults to true), which is what populates the
	// availability snapshot and the configured-provider set used to resolve the
	// default model; skipping it left every provider unconfigured.
	allowNetwork := !args.Offline
	runtime, err := coding.CreateModelRuntime(coding.CreateModelRuntimeOptions{
		AuthPath:              agentDir + "/auth.json",
		AllowModelNetwork:     allowNetwork,
		ModelRefreshTimeoutMS: &modelRefreshTimeoutMS,
		Signal:                ctx,
	})
	if err != nil {
		return err
	}

	// Resolve the CLI model/thinking overrides.
	var model *ai.Model
	thinking := ai.ThinkingLevel("")
	if args.Model != nil || args.Provider != nil {
		provider := ""
		if args.Provider != nil {
			provider = *args.Provider
		}
		modelID := ""
		if args.Model != nil {
			modelID = *args.Model
		}
		resolved := coding.ResolveCliModel(coding.ResolveCliModelOptions{
			CLIProvider: provider, CLIModel: modelID, ModelRuntime: runtime,
		})
		if resolved.Error != "" {
			return fmt.Errorf("%s", resolved.Error)
		}
		model = resolved.Model
		if resolved.HasThinking {
			thinking = resolved.ThinkingLevel
		}
	}
	if args.Thinking != nil {
		thinking = *args.Thinking
	}

	created, err := coding.CreateAgentSession(ctx, &coding.CreateAgentSessionOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		Model:           model,
		ThinkingLevel:   thinking,
		SessionManager:  sessions,
		ModelRuntime:    runtime,
		SettingsManager: settings,
	})
	if err != nil {
		return err
	}

	tuiMode := settings.GetTuiMode()
	if args.TuiMode != nil {
		tuiMode = *args.TuiMode
	}

	app := interactive.NewApp(interactive.AppOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		TuiMode:         tuiMode,
		Version:         coding.Version,
		AppName:         appName,
		QuietStartup:    settings.GetQuietStartup(),
		Verbose:         args.Verbose,
		Settings:        settings,
		Session:         created.Session,
		Runtime:         runtime,
		SessionMgr:      sessions,
		Offline:         args.Offline,
		InitialMessages: args.Messages,
		Exit:            os.Exit,
		RegisterSignal: func(sig os.Signal, handler func()) func() {
			channel := make(chan os.Signal, 1)
			signal.Notify(channel, sig)
			done := make(chan struct{})
			go func() {
				for {
					select {
					case <-done:
						return
					case <-channel:
						handler()
					}
				}
			}()
			return func() {
				signal.Stop(channel)
				close(done)
			}
		},
	})
	app.Run(ctx)
	return nil
}

// resolvedSession is upstream main.ts's ResolvedSession (kind + payload).
type resolvedSession struct {
	kind string // "path" | "local" | "global" | "not_found"
	path string
	arg  string
	cwd  string
}

// resolveSessionPath resolves a --session/--fork argument (upstream
// main.ts resolveSessionPath): path-looking arguments open directly,
// otherwise the argument matches session IDs — exact first, then prefix —
// against the local project and then globally across all projects.
func resolveSessionPath(sessionArg, cwd, sessionDir string) resolvedSession {
	if strings.Contains(sessionArg, "/") || strings.Contains(sessionArg, "\\") || strings.HasSuffix(sessionArg, ".jsonl") {
		return resolvedSession{kind: "path", path: coding.ResolvePath(sessionArg, cwd, coding.PathInputOptions{})}
	}

	local := coding.ListSessions(cwd, sessionDir)
	for _, info := range local {
		if info.ID == sessionArg {
			return resolvedSession{kind: "local", path: info.Path}
		}
	}
	for _, info := range local {
		if strings.HasPrefix(info.ID, sessionArg) {
			return resolvedSession{kind: "local", path: info.Path}
		}
	}

	all := coding.ListAllSessions(sessionDir)
	for _, info := range all {
		if info.ID == sessionArg {
			return resolvedSession{kind: "global", path: info.Path, cwd: info.Cwd}
		}
	}
	for _, info := range all {
		if strings.HasPrefix(info.ID, sessionArg) {
			return resolvedSession{kind: "global", path: info.Path, cwd: info.Cwd}
		}
	}

	return resolvedSession{kind: "not_found", arg: sessionArg}
}

// errNoSessionSelected is returned when the -r picker is dismissed.
var errNoSessionSelected = errors.New("no session selected")

// errSessionAborted is returned when a prompt is declined (upstream prints
// dim "Aborted." and exits 0).
var errSessionAborted = errors.New("session aborted")

// promptConfirm reads a yes/no answer (upstream promptConfirm's
// `${message} [y/N] ` readline question). A var so tests can stub it.
var promptConfirm = func(message string) bool {
	fmt.Printf("%s [y/N] ", message)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// selectResumeSession shows the interactive session picker (upstream
// cli/session-picker.ts selectSession). A var so tests can stub it.
var selectResumeSession = func(cwd string, sessionDir string, settings *coding.SettingsManager) (string, bool) {
	current := func(_ interactive.SessionListProgress, ctx context.Context) ([]coding.SessionInfo, error) {
		return coding.ListSessions(cwd, sessionDir), nil
	}
	all := func(_ interactive.SessionListProgress, ctx context.Context) ([]coding.SessionInfo, error) {
		if sessionDir != "" {
			return coding.ListAllSessions(sessionDir), nil
		}
		return coding.ListAllSessions(""), nil
	}
	selected := interactive.SelectSession(interactive.SelectSessionOptions{
		Settings:      settings,
		CurrentLoader: current,
		AllLoader:     all,
	})
	return selected, selected != ""
}

// resumeSession opens the session requested by the CLI flags (upstream
// createSessionManager's session selection).
func resumeSession(args *coding.Args, cwd string, agentDir string, settings *coding.SettingsManager) (*coding.SessionManager, error) {
	sessionDir := ""
	if args.SessionDir != nil {
		sessionDir = *args.SessionDir
	}
	// Upstream validateSessionIdFlags: --session-id rejects conflicting flags
	// and invalid id formats before any session is opened.
	if args.SessionID != nil {
		var conflicts []string
		if args.Session != nil {
			conflicts = append(conflicts, "--session")
		}
		if args.Continue {
			conflicts = append(conflicts, "--continue")
		}
		if args.Resume {
			conflicts = append(conflicts, "--resume")
		}
		if len(conflicts) > 0 {
			return nil, fmt.Errorf("--session-id cannot be combined with %s", strings.Join(conflicts, ", "))
		}
		if err := coding.AssertValidSessionID(*args.SessionID); err != nil {
			return nil, err
		}
	}
	// Upstream validateForkFlags runs before any session is opened.
	if args.Fork != nil {
		var conflicts []string
		if args.Session != nil {
			conflicts = append(conflicts, "--session")
		}
		if args.Continue {
			conflicts = append(conflicts, "--continue")
		}
		if args.Resume {
			conflicts = append(conflicts, "--resume")
		}
		if args.NoSession {
			conflicts = append(conflicts, "--no-session")
		}
		if len(conflicts) > 0 {
			return nil, fmt.Errorf("--fork cannot be combined with %s", strings.Join(conflicts, ", "))
		}
	}
	if args.Continue {
		// Upstream -c uses SessionManager.continueRecent(cwd, sessionDir), which
		// keeps sessionDir = default(cwd) so the resume hint stays `pi --session …`.
		return coding.ContinueRecentSession(cwd, sessionDir), nil
	}
	if args.Fork != nil {
		// Upstream createSessionManager's fork branch.
		forkID := ""
		if args.SessionID != nil {
			for _, info := range coding.ListSessions(cwd, sessionDir) {
				if info.ID == *args.SessionID {
					return nil, fmt.Errorf("Session already exists with id '%s'", *args.SessionID)
				}
			}
			forkID = *args.SessionID
		}
		resolved := resolveSessionPath(*args.Fork, cwd, sessionDir)
		switch resolved.kind {
		case "path", "local", "global":
			return coding.ForkSession(resolved.path, cwd, sessionDir, &coding.NewSessionOptions{ID: forkID})
		default:
			return nil, fmt.Errorf("No session found matching '%s'", resolved.arg)
		}
	}
	if args.Session != nil {
		resolved := resolveSessionPath(*args.Session, cwd, sessionDir)
		switch resolved.kind {
		case "path", "local":
			return coding.OpenSession(resolved.path, sessionDir, "")
		case "global":
			// Upstream prompts to fork the session into the current directory
			// (promptConfirm + forkSessionOrExit); declining aborts.
			fmt.Printf("\033[33mSession found in different project: %s\033[0m\n", resolved.cwd)
			if !promptConfirm("Fork this session into current directory?") {
				return nil, errSessionAborted
			}
			return coding.ForkSession(resolved.path, cwd, sessionDir, nil)
		default:
			return nil, fmt.Errorf("No session found matching '%s'", resolved.arg)
		}
	}
	if args.SessionID != nil {
		for _, info := range coding.ListSessions(cwd, sessionDir) {
			if info.ID == *args.SessionID {
				return coding.OpenSession(info.Path, sessionDir, "")
			}
		}
		// Upstream (createSessionManager): warn, then create a new session
		// carrying the requested id.
		fmt.Printf("\033[33mWarning: No project session found with id '%s'; creating a new session with that id.\033[0m\n", *args.SessionID)
		sm := coding.NewSessionManager(cwd, &coding.SessionManagerOptions{SessionDir: sessionDir})
		sm.NewSession(&coding.NewSessionOptions{ID: *args.SessionID})
		return sm, nil
	}
	if args.Resume {
		// Upstream parsed.resume opens the interactive session picker.
		selected, ok := selectResumeSession(cwd, sessionDir, settings)
		if !ok {
			return nil, errNoSessionSelected
		}
		return coding.OpenSession(selected, sessionDir, "")
	}
	listed := coding.ListSessions(cwd, sessionDir)
	if len(listed) == 0 {
		return nil, fmt.Errorf("no sessions to resume")
	}
	return coding.OpenSession(listed[0].Path, sessionDir, "")
}

// ensure syscall is used on platforms where the signal set is empty.
var _ = syscall.SIGTERM
