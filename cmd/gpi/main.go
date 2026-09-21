// Command gpi is the Go port's interactive coding-agent CLI.
//
// It is a pragmatic entrypoint: it boots the ported session/services (settings,
// auth, model runtime, agent session) and runs the ported interactive mode
// (coding/interactive). The upstream print/json/rpc modes, package manager,
// extensions and migrations are out of scope (see README).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/coding/interactive"
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

	// Session manager: resume the newest session or start a fresh one.
	var sessions *coding.SessionManager
	if args.Resume || args.Continue || args.Session != nil || args.SessionID != nil {
		sessions, err = resumeSession(args, cwd, agentDir)
		if err != nil {
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

	// Theme: initialize before building the component tree.
	themeName := "dark"
	if setting := settings.GetTheme(); setting != nil && *setting != "" {
		themeName = *setting
	}
	interactive.SetTrueColorSupport(true)
	interactive.SetStyleColorsEnabled(true)
	interactive.InitTheme(themeName, false)

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

// resumeSession opens the newest session for the cwd, or an explicit one.
func resumeSession(args *coding.Args, cwd string, agentDir string) (*coding.SessionManager, error) {
	sessionDir := ""
	if args.SessionDir != nil {
		sessionDir = *args.SessionDir
	}
	if args.Continue {
		// Upstream -c uses SessionManager.continueRecent(cwd, sessionDir), which
		// keeps sessionDir = default(cwd) so the resume hint stays `pi --session …`.
		return coding.ContinueRecentSession(cwd, sessionDir), nil
	}
	if args.Session != nil {
		return coding.OpenSession(*args.Session, sessionDir, "")
	}
	if args.SessionID != nil {
		for _, info := range coding.ListSessions(cwd, sessionDir) {
			if info.ID == *args.SessionID {
				return coding.OpenSession(info.Path, sessionDir, "")
			}
		}
		return nil, fmt.Errorf("session %s not found", *args.SessionID)
	}
	listed := coding.ListSessions(cwd, sessionDir)
	if len(listed) == 0 {
		return nil, fmt.Errorf("no sessions to resume")
	}
	return coding.OpenSession(listed[0].Path, sessionDir, "")
}

// ensure syscall is used on platforms where the signal set is empty.
var _ = syscall.SIGTERM
