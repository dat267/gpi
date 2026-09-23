package coding

import (
	"context"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
)

// Port of the user's modeldefault extension (divergence D151).
//
// The extension's tests are the spec: every session start syncs the session
// onto the settings default model; a session already on the default is left
// alone silently; a default that cannot be applied warns. The extension polls
// because pi's provider-auth snapshot lands asynchronously — this port resolves
// availability synchronously (queueAvailabilityRefresh runs inline), so a
// single attempt carries the same meaning and the warnings are the same.

func modelDefaultSession(t *testing.T, runtime *ModelRuntime, settings *SettingsManager, current *ai.Model) *AgentSession {
	t.Helper()
	session, err := NewAgentSession(&SessionConfig{
		Cwd:      t.TempDir(),
		Model:    current,
		StreamFn: stubStreamFn,
	})
	if err != nil {
		t.Fatal(err)
	}
	session.control = &AgentSessionControl{
		Settings:     settings,
		ModelRuntime: runtime,
		Tools:        map[string]AgentToolDefinition{},
	}
	return session
}

// configuredModelDefaultRuntime builds a runtime with two stub providers and a
// key on alpha only, or on both when both are asked for.
func configuredModelDefaultRuntime(t *testing.T, configure ...string) *ModelRuntime {
	t.Helper()
	runtime := runtimeWithProviders(t, stubProvider("alpha"), stubProvider("beta"))
	for _, id := range configure {
		if err := runtime.SetRuntimeAPIKey(id, "sk-"+id, context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return runtime
}

func settingsWithDefault(t *testing.T, provider, model string) *SettingsManager {
	t.Helper()
	tempAgentDir(t)
	settings := NewSettingsManagerFromFiles(t.TempDir(), GetAgentDir(), SettingsManagerCreateOptions{})
	if provider != "" {
		settings.SetDefaultModelAndProvider(provider, model)
	}
	return settings
}

func TestReadModelDefaultRef(t *testing.T) {
	settings := settingsWithDefault(t, "alpha", "alpha-model")
	ref := ReadModelDefaultRef(settings)
	if ref == nil || ref.Provider != "alpha" || ref.ID != "alpha-model" {
		t.Fatalf("ref = %+v", ref)
	}

	// Either half missing (or blank) means no default at all.
	blank := settingsWithDefault(t, "alpha", "")
	if ref := ReadModelDefaultRef(blank); ref != nil {
		t.Errorf("ref = %+v, want nil when the model id is empty", ref)
	}
	empty := settingsWithDefault(t, "", "")
	if ref := ReadModelDefaultRef(empty); ref != nil {
		t.Errorf("ref = %+v, want nil when no default is set", ref)
	}
	if ref := ReadModelDefaultRef(nil); ref != nil {
		t.Errorf("ref = %+v, want nil for a nil settings manager", ref)
	}
}

func TestModelDefaultSyncSwitchesToTheDefault(t *testing.T) {
	runtime := configuredModelDefaultRuntime(t, "alpha")
	settings := settingsWithDefault(t, "alpha", "alpha-model")
	current := runtime.GetModel("beta", "beta-model")
	session := modelDefaultSession(t, runtime, settings, current)

	result := session.SyncSessionModelToDefault(context.Background(), "session start")

	if !result.Applied {
		t.Fatalf("result = %+v, want the switch applied", result)
	}
	if result.Warning {
		t.Errorf("a successful switch must not warn: %+v", result)
	}
	// Regression the extension's suite guards: the full model object must be
	// applied, not a bare {provider, id} ref, or the footer's context window
	// reads as "?/0".
	model := session.Model()
	if model == nil || model.ID != "alpha-model" {
		t.Fatalf("model = %+v, want alpha-model", model)
	}
	if model.ContextWindow != 1000 || model.MaxTokens != 100 {
		t.Errorf("model limits = %d/%d, want the catalog values 1000/100", model.ContextWindow, model.MaxTokens)
	}
	if !strings.Contains(result.Message, "alpha/alpha-model") || !strings.Contains(result.Message, "session start") {
		t.Errorf("message = %q, want the ref and the reason", result.Message)
	}
}

func TestModelDefaultSyncLeavesTheDefaultAlone(t *testing.T) {
	runtime := configuredModelDefaultRuntime(t, "alpha")
	settings := settingsWithDefault(t, "alpha", "alpha-model")
	session := modelDefaultSession(t, runtime, settings, runtime.GetModel("alpha", "alpha-model"))

	result := session.SyncSessionModelToDefault(context.Background(), "session start")

	if result.Applied || result.Message != "" {
		t.Fatalf("result = %+v, want a silent no-op", result)
	}
}

func TestModelDefaultSyncDoesNothingWithoutADefault(t *testing.T) {
	runtime := configuredModelDefaultRuntime(t, "alpha", "beta")
	settings := settingsWithDefault(t, "", "")
	session := modelDefaultSession(t, runtime, settings, runtime.GetModel("beta", "beta-model"))

	result := session.SyncSessionModelToDefault(context.Background(), "session start")

	if result.Applied || result.Message != "" {
		t.Fatalf("result = %+v, want a silent no-op", result)
	}
	if session.Model().ID != "beta-model" {
		t.Errorf("model = %q, want it untouched", session.Model().ID)
	}
}

func TestModelDefaultSyncWarnsWhenTheModelIsNotInTheCatalog(t *testing.T) {
	runtime := configuredModelDefaultRuntime(t, "alpha", "beta")
	settings := settingsWithDefault(t, "alpha", "ghost-model")
	session := modelDefaultSession(t, runtime, settings, runtime.GetModel("beta", "beta-model"))

	result := session.SyncSessionModelToDefault(context.Background(), "session start")

	if result.Applied {
		t.Fatal("a model that is not in the catalog must not be applied")
	}
	if !result.Warning {
		t.Errorf("result = %+v, want a warning", result)
	}
	for _, want := range []string{"not available yet", "alpha/ghost-model", "beta/beta-model"} {
		if !strings.Contains(result.Message, want) {
			t.Errorf("message = %q, want it to contain %q", result.Message, want)
		}
	}
	if !strings.Contains(result.Message, "not in the catalog") {
		t.Errorf("message = %q, want the catalog explanation", result.Message)
	}
	if session.Model().ID != "beta-model" {
		t.Errorf("model = %q, want it left on the previous model", session.Model().ID)
	}
}

func TestModelDefaultSyncWarnsWhenTheProviderHasNoAuth(t *testing.T) {
	// alpha is in the catalog but has no configured auth, so setModel refuses.
	runtime := configuredModelDefaultRuntime(t, "beta")
	settings := settingsWithDefault(t, "alpha", "alpha-model")
	session := modelDefaultSession(t, runtime, settings, runtime.GetModel("beta", "beta-model"))

	result := session.SyncSessionModelToDefault(context.Background(), "session start")

	if result.Applied {
		t.Fatal("a model without configured auth must not be applied")
	}
	if !result.Warning {
		t.Errorf("result = %+v, want a warning", result)
	}
	if !strings.Contains(result.Message, "no configured auth") {
		t.Errorf("message = %q, want the auth explanation", result.Message)
	}
	if session.Model().ID != "beta-model" {
		t.Errorf("model = %q, want it left on the previous model", session.Model().ID)
	}
}

// A manual pick persisted in the session from an earlier run must not survive
// the next start: the extension has no per-session claim, so the sync wins.
func TestModelDefaultSyncOverridesAStaleSessionPick(t *testing.T) {
	runtime := configuredModelDefaultRuntime(t, "alpha", "beta")
	settings := settingsWithDefault(t, "alpha", "alpha-model")
	session := modelDefaultSession(t, runtime, settings, runtime.GetModel("beta", "beta-model"))

	result := session.SyncSessionModelToDefault(context.Background(), "resume")

	if !result.Applied || session.Model().ID != "alpha-model" {
		t.Fatalf("result = %+v model = %q, want the default applied over the stale pick", result, session.Model().ID)
	}
}

// The switch is recorded in the session like any other model change.
func TestModelDefaultSyncRecordsTheModelChange(t *testing.T) {
	runtime := configuredModelDefaultRuntime(t, "alpha")
	settings := settingsWithDefault(t, "alpha", "alpha-model")
	session := modelDefaultSession(t, runtime, settings, runtime.GetModel("beta", "beta-model"))

	if result := session.SyncSessionModelToDefault(context.Background(), "session start"); !result.Applied {
		t.Fatalf("result = %+v, want the switch applied", result)
	}

	entries := session.Sessions.GetBranch("")
	found := false
	for _, entry := range entries {
		if entry.Type == "model_change" && entry.Provider == "alpha" && entry.ModelID == "alpha-model" {
			found = true
		}
	}
	if !found {
		t.Errorf("no model_change entry for the sync in %+v", entries)
	}
}

// The wiring: session creation runs the sync, and an explicit CLI model
// override is not overruled by it.
func newModelDefaultCreateOptions(t *testing.T, runtime *ModelRuntime, settings *SettingsManager, model *ai.Model) *CreateAgentSessionOptions {
	t.Helper()
	return &CreateAgentSessionOptions{
		Cwd:             t.TempDir(),
		AgentDir:        GetAgentDir(),
		Model:           model,
		ModelRuntime:    runtime,
		SettingsManager: settings,
		SessionManager:  NewSessionManager(t.TempDir(), nil),
	}
}

// resumedSessionManager builds a session manager that looks like a previous
// run: it carries a message and a model change to the other provider.
func resumedSessionManager(t *testing.T, provider, model string) *SessionManager {
	t.Helper()
	manager := NewSessionManager(t.TempDir(), nil)
	manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "earlier work"}})
	manager.AppendModelChange(provider, model)
	return manager
}

func TestCreateAgentSessionSyncsToTheDefault(t *testing.T) {
	tempAgentDir(t)
	runtime := configuredModelDefaultRuntime(t, "alpha", "beta")
	settings := settingsWithDefault(t, "alpha", "alpha-model")

	// A resumed session on beta: without the sync it would keep beta forever,
	// because pi only consults the settings default for sessions that never
	// chose.
	options := newModelDefaultCreateOptions(t, runtime, settings, nil)
	options.SessionManager = resumedSessionManager(t, "beta", "beta-model")

	created, err := CreateAgentSession(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}

	if created.Session.Model().ID != "alpha-model" {
		t.Fatalf("model = %q, want the settings default", created.Session.Model().ID)
	}
	if !strings.Contains(created.ModelDefaultMessage, "alpha/alpha-model") {
		t.Errorf("message = %q, want the default named", created.ModelDefaultMessage)
	}
	if created.ModelDefaultWarning {
		t.Errorf("a successful sync must not be reported as a warning: %+v", created)
	}
}

func TestCreateAgentSessionDefersToAnExplicitModelChoice(t *testing.T) {
	tempAgentDir(t)
	runtime := configuredModelDefaultRuntime(t, "alpha", "beta")
	settings := settingsWithDefault(t, "alpha", "alpha-model")

	// A --model/--provider choice is the caller's decision for this session.
	created, err := CreateAgentSession(context.Background(),
		newModelDefaultCreateOptions(t, runtime, settings, runtime.GetModel("beta", "beta-model")))
	if err != nil {
		t.Fatal(err)
	}

	if created.Session.Model().ID != "beta-model" {
		t.Fatalf("model = %q, want the explicit choice kept", created.Session.Model().ID)
	}
	if created.ModelDefaultMessage != "" {
		t.Errorf("message = %q, want a silent no-op", created.ModelDefaultMessage)
	}

	// The override survives a reload too, or reload would silently move it.
	created.Session.Reload()
	if created.Session.Model().ID != "beta-model" {
		t.Errorf("model = %q after reload, want the explicit choice kept", created.Session.Model().ID)
	}
}

func TestCreateAgentSessionGivenNoDefaultLeavesTheModelAlone(t *testing.T) {
	tempAgentDir(t)
	runtime := configuredModelDefaultRuntime(t, "beta")
	settings := settingsWithDefault(t, "", "")

	created, err := CreateAgentSession(context.Background(),
		newModelDefaultCreateOptions(t, runtime, settings, runtime.GetModel("beta", "beta-model")))
	if err != nil {
		t.Fatal(err)
	}

	if created.Session.Model().ID != "beta-model" {
		t.Fatalf("model = %q, want it untouched", created.Session.Model().ID)
	}
	if created.ModelDefaultMessage != "" {
		t.Errorf("message = %q, want a silent no-op", created.ModelDefaultMessage)
	}
}

// A reload reasserts the settings default over a model the session picked.
func TestReloadReassertsTheDefaultModel(t *testing.T) {
	tempAgentDir(t)
	runtime := configuredModelDefaultRuntime(t, "alpha", "beta")
	settings := settingsWithDefault(t, "", "")

	options := newModelDefaultCreateOptions(t, runtime, settings, nil)
	options.SessionManager = resumedSessionManager(t, "beta", "beta-model")
	created, err := CreateAgentSession(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if created.Session.Model().ID != "beta-model" {
		t.Fatalf("model = %q, want the resumed model while no default exists", created.Session.Model().ID)
	}

	// The default appears after the session started.
	settings.SetDefaultModelAndProvider("alpha", "alpha-model")
	created.Session.Reload()

	if created.Session.Model().ID != "alpha-model" {
		t.Fatalf("model = %q after reload, want the default applied", created.Session.Model().ID)
	}
	notice := created.Session.LastModelDefaultSync()
	if !notice.Applied || notice.Warning {
		t.Errorf("notice = %+v, want an applied information notice", notice)
	}
	if !strings.Contains(notice.Message, "reload") {
		t.Errorf("message = %q, want the reload reason", notice.Message)
	}
}
