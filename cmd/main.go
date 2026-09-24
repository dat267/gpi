// Package cmd boots the port of pi's interactive coding-agent CLI: it wires
// the settings, auth, model runtime and agent session from the command-line
// flags and runs the ported interactive mode (coding/interactive). The upstream
// print/json/rpc modes, package manager, extensions and migrations are out of
// scope (see README).
//
// The module root's main.go is a thin wrapper over Execute, so the module is
// installable with `go install github.com/dat267/pier@latest`.
package cmd

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

// installThemeCapabilities enables the port's colour depth and installs its
// palette. Both --export and the interactive run need it, and the install has to
// follow the capability switch: a theme bakes its 256-colour or truecolor
// escapes when it is created (D154).
func installThemeCapabilities() {
	interactive.SetTrueColorSupport(true)
	interactive.SetStyleColorsEnabled(true)
	interactive.InstallPierTheme()
}

// errAlreadyReported marks a failure that has already been written to stderr as
// a diagnostic, so main only has to set the exit status.
var errAlreadyReported = errors.New("already reported")

// Execute runs the CLI, exiting the process on a fatal error.
func Execute() {
	appName := executableName()
	args := coding.ParseArgs(os.Args[1:])

	// Parse diagnostics are reported before anything they would invalidate: an
	// unusable flag value should not be followed by a run that ignores it, and
	// upstream reports them straight after parsing, ahead of --version. An error
	// among them is fatal.
	for _, diagnostic := range args.Diagnostics {
		fmt.Fprintln(os.Stderr, coding.FormatCLIDiagnostic(diagnostic))
	}
	if coding.HasErrorDiagnostics(args.Diagnostics) {
		os.Exit(1)
	}

	if args.Help {
		fmt.Print(coding.PrintHelpNamed(appName))
		return
	}
	if args.Version {
		fmt.Println(coding.Version)
		return
	}
	// --export renders a session file and exits before any runtime or TUI is
	// built (upstream handles it right after --version). The output path is the
	// first positional message, which is upstream's convention.
	if args.Export != nil {
		outputPath := ""
		if len(args.Messages) > 0 {
			outputPath = args.Messages[0]
		}
		installThemeCapabilities()
		result, err := interactive.ExportFromFile(*args.Export, outputPath, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, coding.FormatCLIDiagnostic(coding.CLIDiagnostic{Type: "error", Message: err.Error()}))
			os.Exit(1)
		}
		fmt.Println("Exported to: " + result)
		return
	}
	if args.Print || args.Mode == coding.CLIModeJSON || args.Mode == coding.CLIModeRPC {
		fmt.Fprintln(os.Stderr, appName+": print, json and rpc modes are not supported by this build")
		os.Exit(1)
	}

	if err := run(appName, args); err != nil {
		if !errors.Is(err, errAlreadyReported) {
			fmt.Fprintln(os.Stderr, appName+": "+err.Error())
		}
		os.Exit(1)
	}
}

// applyOfflineMode mirrors upstream main.ts, which folds --offline and a truthy
// PI_OFFLINE into the environment before anything reads it. Offline is
// process-wide there: the model runtime and the version check consult the
// environment, not the parsed arguments, and an unset variable would leave the
// release check free to reach the network under --offline.
func applyOfflineMode(args *coding.Args) {
	if !args.Offline && !coding.IsTruthyEnvFlag(os.Getenv("PI_OFFLINE")) {
		return
	}
	_ = os.Setenv("PI_OFFLINE", "1")
	_ = os.Setenv("PI_SKIP_VERSION_CHECK", "1")
}

func run(appName string, args *coding.Args) error {
	ctx := context.Background()
	applyOfflineMode(args)
	// Bounds the create-time catalog refresh (upstream leaves it unbounded; the
	// Go refresh is synchronous, so it needs a ceiling).
	modelRefreshTimeoutMS := int64(15000)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	agentDir := coding.GetAgentDir()

	// Bootstrap settings: global settings only, because whether the project's may
	// be read is exactly what is not decided yet (upstream's
	// `SettingsManager.create(cwd, agentDir, { projectTrusted: false })`). The
	// theme and the -r picker run off this manager.
	projectUntrusted := false
	settings := coding.NewSettingsManagerFromFiles(cwd, agentDir, coding.SettingsManagerCreateOptions{
		ProjectTrusted: &projectUntrusted,
	})
	// The global settings' proxy seeds the HTTP environment before anything
	// makes a request (upstream applyHttpProxySettings on the bootstrap manager).
	if global := settings.GetGlobalSettings(); global != nil && global.HTTPProxy != nil {
		coding.ApplyHTTPProxySettings(*global.HTTPProxy)
	}

	// Theme: --use-theme overrides the configured theme for this run only, and
	// has to be applied before any TUI because the -r picker runs before the app.
	if args.UseTheme != nil {
		settings.ApplyOverrides(&coding.Settings{Theme: args.UseTheme})
	}
	// The port ships its own palette under the upstream theme names (D154):
	// terminal-default backgrounds and an amber accent, so it is obvious at a
	// glance which build is running. The embedded upstream palettes stay as the
	// fallback for library consumers and as what the upstream-parity test corpus
	// renders with.
	installThemeCapabilities()
	// Custom themes come from the agent's themes directory plus any theme paths
	// the settings or --theme name. --no-themes drops the discovered set and
	// keeps the named ones (upstream's noThemes).
	interactive.SetCustomThemeSources(interactive.CustomThemeSources{
		Dir:         filepath.Join(agentDir, "themes"),
		Paths:       themePathsFor(args, settings, cwd),
		NoDiscovery: args.NoThemes,
	})
	themeName := "dark"
	if setting := settings.GetTheme(); setting != nil && *setting != "" {
		themeName = *setting
	}
	interactive.InitTheme(themeName, false)

	// Session manager: resume the newest session or start a fresh one. A metadata
	// command (--list-models) resolves no session and writes nothing, which is
	// also why it never resumes (upstream createSessionManager returns an
	// in-memory manager for it).
	listingModels := args.ListModels != nil || args.ListModelsAll
	var sessions *coding.SessionManager
	if !listingModels && (args.Resume || args.Continue || args.Fork != nil || args.Session != nil || args.SessionID != nil) {
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
		if args.NoSession || listingModels {
			persist := false
			options.Persist = &persist
		}
		sessions = coding.NewSessionManager(cwd, options)
	}

	// --name labels the session, recorded as a session_info entry — the same
	// entry the /name command writes.
	name, err := sessionNameFor(args)
	if err != nil {
		return err
	}
	if name != nil {
		sessions.AppendSessionInfo(*name)
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

	// --list-models is a metadata command: it prints the catalog and exits
	// without starting the TUI or creating a session (upstream lists once the
	// runtime exists, before the interactive mode).
	if listingModels {
		return listModels(runtime, settings, args, ctx)
	}

	// Project trust, now that the picker and the metadata commands are done and
	// before any project resource is read: the override, the store's decision,
	// the defaultProjectTrust setting, then the startup prompt. The answer builds
	// the runtime settings manager, so an untrusted project's .pi settings,
	// skills, prompts, themes and prompt files stay unread.
	trusted, trustErr := resolveStartupProjectTrust(startupTrustOptions{
		cwd:       cwd,
		agentDir:  agentDir,
		override:  args.ProjectTrustOverride,
		bootstrap: settings,
		hasUI:     true,
		prompt:    startupTrustPrompt(settings),
	})
	if trustErr != nil {
		return trustErr
	}
	settings = coding.NewSettingsManagerFromFiles(cwd, agentDir, coding.SettingsManagerCreateOptions{
		ProjectTrusted: &trusted,
	})
	if args.UseTheme != nil {
		settings.ApplyOverrides(&coding.Settings{Theme: args.UseTheme})
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

	// --models sets the model cycle scope, overriding the settings' enabled
	// models (upstream `parsed.models ?? settingsManager.getEnabledModels()`).
	// A pattern that matches nothing is reported rather than silently ignored.
	scopedModels, scopeDiagnostics, err := scopedModelsFor(args, settings, runtime, ctx)
	if err != nil {
		return err
	}
	startupDiagnostics := make([]interactive.StartupDiagnostic, 0, len(scopeDiagnostics))
	for _, diagnostic := range scopeDiagnostics {
		startupDiagnostics = append(startupDiagnostics, interactive.StartupDiagnostic{
			Type:    diagnostic.Type,
			Message: diagnostic.Message,
		})
	}

	// @file arguments are read into the session's first message, so a file and
	// the question about it arrive as one prompt.
	initialPrompt, err := initialPromptFor(args, cwd)
	if err != nil {
		return err
	}
	if len(initialPrompt.Images) > 0 {
		// The interactive mode has no image-input path, so an @file image cannot
		// be attached the way upstream attaches it. Say so rather than let the
		// model be asked about an image it never received (D153).
		fmt.Fprintln(os.Stderr, coding.FormatCLIDiagnostic(coding.CLIDiagnostic{
			Type: "warning",
			Message: fmt.Sprintf(
				"%d image(s) from @file arguments were ignored: this build cannot send image content",
				len(initialPrompt.Images)),
		}))
	}

	// Tool flags reach the session through the same projection the tests
	// exercise: --tools/--exclude-tools pass through and --no-tools /
	// --no-builtin-tools map onto the noTools option.
	tools := args.ToolSelection()
	created, err := coding.CreateAgentSession(ctx, &coding.CreateAgentSessionOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		Model:           model,
		ThinkingLevel:   thinking,
		SessionManager:  sessions,
		ModelRuntime:    runtime,
		SettingsManager: settings,
		// Prompt sources: text, or a path to read (the files are discovered
		// when the flags are absent).
		SystemPrompt:       args.SystemPrompt,
		AppendSystemPrompt: args.AppendSystemPrompt,
		// Tool selection (allowlist, denylist, and the disable flags).
		Tools:        tools.Tools,
		ExcludeTools: tools.ExcludeTools,
		NoTools:      tools.NoTools,
		// Model cycle scope (--models, or the settings' enabled models).
		ScopedModels: scopedModels,
		// Resource flags: explicit skill directories (resolved against cwd, as
		// upstream resolveCliPaths does) and the discovery switches.
		SkillPaths:     coding.ResolveCLIPaths(cwd, args.Skills),
		NoSkills:       args.NoSkills,
		NoContextFiles: args.NoContextFiles,
		// Prompt templates: explicit paths load even when discovery is off.
		PromptTemplatePaths: coding.ResolveCLIPaths(cwd, args.PromptTemplates),
		NoPromptTemplates:   args.NoPromptTemplates,
	})
	if err != nil {
		return err
	}

	// --api-key pins the credential for this run. It needs a model to attach to,
	// and upstream reports that requirement rather than ignoring the key (the
	// model may come from the cycle scope, so it is read back rather than
	// guessed at).
	if args.APIKey != nil {
		provider := ""
		if sessionModel := created.Session.Model(); sessionModel != nil {
			provider = sessionModel.Provider
		}
		if provider == "" {
			fmt.Fprintln(os.Stderr, coding.FormatCLIDiagnostic(coding.CLIDiagnostic{
				Type:    "error",
				Message: "--api-key requires a model to be specified via --model, --provider/--model, or --models",
			}))
			return errAlreadyReported
		}
		if err := runtime.SetRuntimeAPIKey(provider, *args.APIKey, ctx); err != nil {
			return err
		}
	}

	tuiMode := settings.GetTuiMode()
	if args.TuiMode != nil {
		tuiMode = *args.TuiMode
	}

	app := interactive.NewApp(interactive.AppOptions{
		Cwd:          cwd,
		AgentDir:     agentDir,
		TuiMode:      tuiMode,
		Version:      coding.Version,
		AppName:      appName,
		QuietStartup: settings.GetQuietStartup(),
		Verbose:      args.Verbose,
		Settings:     settings,
		Session:      created.Session,
		Runtime:      runtime,
		SessionMgr:   sessions,
		Offline:      args.Offline,
		// The first message carries any @file text ahead of the first positional
		// message; the rest stay queued behind it.
		InitialMessage:      initialPrompt.Message,
		InitialMessages:     initialPrompt.Rest,
		InitialThemeSetting: args.UseTheme,
		// A model restore or resolution fallback is surfaced at startup.
		ModelFallbackMessage: created.ModelFallbackMessage,
		// Model-scope warnings ("No models match pattern ...") are shown at
		// startup, the way upstream passes its startup diagnostics in.
		StartupDiagnostics: startupDiagnostics,
		Exit:               os.Exit,
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

// themePathsFor resolves the theme files and directories named by the settings
// and by --theme against the working directory.
func themePathsFor(args *coding.Args, settings *coding.SettingsManager, cwd string) []string {
	paths := append([]string{}, settings.GetThemePaths()...)
	paths = append(paths, args.Themes...)
	return coding.ResolveCLIPaths(cwd, paths)
}

// listModels prints the available-model table (upstream main.ts's --list-models
// block): the settings diagnostics and the models.json load error go to stderr
// first, then the table to stdout. An empty pattern lists everything.
func listModels(runtime *coding.ModelRuntime, settings *coding.SettingsManager, args *coding.Args, ctx context.Context) error {
	for _, diagnostic := range coding.CollectSettingsDiagnostics(settings) {
		fmt.Fprintln(os.Stderr, coding.FormatCLIDiagnostic(coding.CLIDiagnostic{
			Type: diagnostic.Type, Message: diagnostic.Message,
		}))
	}
	if loadError := runtime.GetError(); loadError != "" {
		fmt.Fprintln(os.Stderr, coding.FormatCLIDiagnostic(coding.CLIDiagnostic{
			Type: "warning", Message: "errors loading models.json:\n" + loadError,
		}))
	}
	models, err := runtime.GetAvailable("", ctx)
	if err != nil {
		return err
	}
	pattern := ""
	if args.ListModels != nil {
		pattern = *args.ListModels
	}
	fmt.Print(coding.ListModelsText(models, pattern))
	return nil
}

// sessionNameFor normalizes a --name value: blank input is an error, not a
// silently unnamed session (upstream normalizeSessionName plus its
// "--name requires a non-empty value" check).
func sessionNameFor(args *coding.Args) (*string, error) {
	if args.Name == nil {
		return nil, nil
	}
	name := coding.NormalizeSessionName(*args.Name)
	if name == nil {
		return nil, errors.New("--name requires a non-empty value")
	}
	return name, nil
}

// scopedModelsFor resolves the models that --models puts in the cycle scope,
// falling back to the settings' enabled models when the flag is absent. Its
// diagnostics report patterns that matched nothing (or carried an unusable
// thinking level) without failing the run.
func scopedModelsFor(args *coding.Args, settings *coding.SettingsManager, runtime coding.ModelRuntimeSource, ctx context.Context) ([]coding.ScopedModel, []coding.ModelScopeDiagnostic, error) {
	patterns := args.Models
	if len(patterns) == 0 {
		patterns = settings.GetEnabledModels()
	}
	if len(patterns) == 0 {
		return nil, nil, nil
	}
	result, err := coding.ResolveModelScopeWithDiagnostics(patterns, runtime, ctx)
	if err != nil {
		return nil, nil, err
	}
	return result.ScopedModels, result.Diagnostics, nil
}

// initialPromptFor reads any @file arguments and composes the session's first
// message from them and the first positional message.
func initialPromptFor(args *coding.Args, cwd string) (coding.InitialPrompt, error) {
	var fileText string
	var fileImages []ai.ImageContent
	if len(args.FileArgs) > 0 {
		// Upstream leaves resizing to the session, which resizes the images
		// once the request model is known.
		autoResize := false
		processed, err := coding.ProcessFileArguments(args.FileArgs, &coding.ProcessFileOptions{
			AutoResizeImages: &autoResize,
			Cwd:              cwd,
		})
		if err != nil {
			return coding.InitialPrompt{}, err
		}
		fileText, fileImages = processed.Text, processed.Images
	}
	return coding.BuildInitialPrompt(args.Messages, fileText, fileImages), nil
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
