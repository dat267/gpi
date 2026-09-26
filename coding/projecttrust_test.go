package coding

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Round 103 tests: the project trust store, trust resolution, source info,
// login guidance, settings diagnostics, and the post-exit pipe drain.

func TestToolTrustStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewProjectTrustStore(dir)
	if got := store.Get(dir); got != nil {
		t.Fatalf("decision = %v", got)
	}
	if entry := store.GetEntry(dir); entry != nil {
		t.Fatalf("entry = %+v", entry)
	}

	// Set writes a sorted, trailing-newline file.
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "aatop")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(project, boolPointer(true)); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(other, boolPointer(false)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.TrustPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.HasSuffix(text, "}\n") {
		t.Fatalf("file = %q", text)
	}
	canonicalProject := CanonicalizePath(project)
	if strings.Index(text, "aatop") > strings.Index(text, filepath.Base(canonicalProject)) {
		t.Fatalf("keys must be sorted: %q", text)
	}

	// A decision applies to descendants.
	nested := filepath.Join(project, "sub", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if decision := store.Get(nested); decision == nil || !*decision {
		t.Fatalf("decision = %v", decision)
	}
	entry := store.GetEntry(nested)
	if entry == nil || entry.Path != CanonicalizePath(project) || !entry.Decision {
		t.Fatalf("entry = %+v", entry)
	}
	// A nearer entry wins.
	if err := store.Set(nested, boolPointer(false)); err != nil {
		t.Fatal(err)
	}
	if decision := store.Get(nested); decision == nil || *decision {
		t.Fatalf("decision = %v", decision)
	}
	// Clearing a decision removes the key.
	if err := store.Set(nested, nil); err != nil {
		t.Fatal(err)
	}
	if decision := store.Get(nested); decision == nil || !*decision {
		t.Fatalf("decision = %v", decision)
	}
	if err := store.SetMany([]ProjectTrustUpdate{
		{Path: project, Decision: boolPointer(true)},
		{Path: nested, Decision: nil},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestToolTrustStoreValidation(t *testing.T) {
	dir := t.TempDir()
	store := NewProjectTrustStore(dir)
	path := store.TrustPath()

	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readTrustFile(path); err == nil || !strings.HasPrefix(err.Error(), "Failed to read trust store ") {
		t.Fatalf("err = %v", err)
	}
	if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readTrustFile(path); err == nil || !strings.HasSuffix(err.Error(), "expected an object") {
		t.Fatalf("err = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"/x": "yes"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readTrustFile(path); err == nil || !strings.Contains(err.Error(), `value for "/x" must be true, false, or null`) {
		t.Fatalf("err = %v", err)
	}
	// A missing file is empty.
	if data, err := readTrustFile(filepath.Join(dir, "missing.json")); err != nil || len(data) != 0 {
		t.Fatalf("data = %+v err = %v", data, err)
	}
}

func TestTrustOptions(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	options := GetProjectTrustOptions(project, false)
	if len(options) != 3 {
		t.Fatalf("options = %+v", options)
	}
	if options[0].Label != "Trust" || !options[0].Trusted || options[0].SavedPath != CanonicalizePath(project) {
		t.Fatalf("first = %+v", options[0])
	}
	if options[1].SavedPath != CanonicalizePath(dir) || len(options[1].Updates) != 2 {
		t.Fatalf("parent = %+v", options[1])
	}
	if options[2].Label != "Do not trust" || options[2].Trusted {
		t.Fatalf("last = %+v", options[2])
	}

	withSession := GetProjectTrustOptions(project, true)
	if len(withSession) != 5 || withSession[2].Label != "Trust (this session only)" ||
		len(withSession[2].Updates) != 0 || withSession[4].Label != "Do not trust (this session only)" {
		t.Fatalf("options = %+v", withSession)
	}

	if parent, ok := GetProjectTrustParentPath(project); !ok || parent != CanonicalizePath(dir) {
		t.Fatalf("parent = %q ok = %v", parent, ok)
	}
}

func TestHasTrustRequiringProjectResources(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if HasTrustRequiringProjectResources(project) {
		t.Fatal("empty project must not require trust")
	}
	// A trust-requiring .pi entry does.
	configDir := filepath.Join(project, ConfigDirName)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if HasTrustRequiringProjectResources(project) {
		t.Fatal("empty config dir must not require trust")
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasTrustRequiringProjectResources(project) {
		t.Fatal("settings.json must require trust")
	}
	// .agents/skills in cwd or an ancestor does too.
	other := filepath.Join(dir, "other")
	nested := filepath.Join(other, "nested")
	if err := os.MkdirAll(filepath.Join(other, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasTrustRequiringProjectResources(nested) {
		t.Fatal("ancestor .agents/skills must require trust")
	}
}

func TestResolveProjectTrusted(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(project, ConfigDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ConfigDirName, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewProjectTrustStore(dir)

	// The override wins.
	trusted, err := ResolveProjectTrusted(ResolveProjectTrustedOptions{
		Cwd: project, TrustStore: store, TrustOverride: boolPointer(true),
	})
	if err != nil || !trusted {
		t.Fatalf("trusted = %v err = %v", trusted, err)
	}
	// A folder without trust-requiring resources is trusted.
	plain := filepath.Join(dir, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if trusted, _ := ResolveProjectTrusted(ResolveProjectTrustedOptions{Cwd: plain, TrustStore: store}); !trusted {
		t.Fatal("plain folder must be trusted")
	}
	// The stored decision wins over the default.
	if err := store.Set(project, boolPointer(true)); err != nil {
		t.Fatal(err)
	}
	if trusted, _ := ResolveProjectTrusted(ResolveProjectTrustedOptions{
		Cwd: project, TrustStore: store, DefaultProjectTrust: "never",
	}); !trusted {
		t.Fatal("stored decision must win")
	}
	if err := store.Set(project, nil); err != nil {
		t.Fatal(err)
	}

	// Defaults.
	if trusted, _ := ResolveProjectTrusted(ResolveProjectTrustedOptions{
		Cwd: project, TrustStore: store, DefaultProjectTrust: "always",
	}); !trusted {
		t.Fatal("always default")
	}
	if trusted, _ := ResolveProjectTrusted(ResolveProjectTrustedOptions{
		Cwd: project, TrustStore: store, DefaultProjectTrust: "never",
	}); trusted {
		t.Fatal("never default")
	}
	// ask without a UI is untrusted.
	if trusted, _ := ResolveProjectTrusted(ResolveProjectTrustedOptions{Cwd: project, TrustStore: store}); trusted {
		t.Fatal("ask without UI must be untrusted")
	}

	// ask with a UI saves the selection.
	selectedLabel := ""
	trusted, err = ResolveProjectTrusted(ResolveProjectTrustedOptions{
		Cwd: project, TrustStore: store,
		ProjectTrustContext: ProjectTrustContext{
			HasUI: true,
			Select: func(prompt string, options []string) (string, error) {
				if !strings.Contains(prompt, "Trust project folder?") || !strings.Contains(prompt, ConfigDirName) {
					t.Fatalf("prompt = %q", prompt)
				}
				if len(options) != 5 {
					t.Fatalf("options = %v", options)
				}
				selectedLabel = options[1]
				return options[1], nil
			},
		},
	})
	if err != nil || !trusted {
		t.Fatalf("trusted = %v err = %v", trusted, err)
	}
	if !strings.Contains(selectedLabel, "Trust parent folder") {
		t.Fatalf("selected = %q", selectedLabel)
	}
	if decision := store.Get(dir); decision == nil || !*decision {
		t.Fatalf("parent decision = %v", decision)
	}
	// The project key itself is cleared (the parent entry still resolves).
	rawTrust, err := os.ReadFile(store.TrustPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawTrust), CanonicalizePath(project)) {
		t.Fatalf("project key must be cleared: %s", rawTrust)
	}
	if decision := store.Get(project); decision == nil || !*decision {
		t.Fatalf("parent decision must resolve: %v", decision)
	}

	// A session-only selection saves nothing.
	before, err := os.ReadFile(store.TrustPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(project, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(dir, nil); err != nil {
		t.Fatal(err)
	}
	afterClear, _ := os.ReadFile(store.TrustPath())
	_ = before
	_ = afterClear
	if trusted, err := ResolveProjectTrusted(ResolveProjectTrustedOptions{
		Cwd: project, TrustStore: store,
		ProjectTrustContext: ProjectTrustContext{
			HasUI:  true,
			Select: func(string, []string) (string, error) { return "Do not trust (this session only)", nil },
		},
	}); err != nil || trusted {
		t.Fatalf("trusted = %v err = %v", trusted, err)
	}
	if decision := store.Get(project); decision != nil {
		t.Fatalf("session-only must not persist: %v", decision)
	}

	// An unknown label is untrusted, and select errors propagate.
	if trusted, _ := ResolveProjectTrusted(ResolveProjectTrustedOptions{
		Cwd: project, TrustStore: store,
		ProjectTrustContext: ProjectTrustContext{HasUI: true, Select: func(string, []string) (string, error) { return "nope", nil }},
	}); trusted {
		t.Fatal("unknown label must be untrusted")
	}
	if _, err := ResolveProjectTrusted(ResolveProjectTrustedOptions{
		Cwd: project, TrustStore: store,
		ProjectTrustContext: ProjectTrustContext{HasUI: true, Select: func(string, []string) (string, error) {
			return "", errSelect
		}},
	}); err != errSelect {
		t.Fatalf("err = %v", err)
	}
}

var errSelect = errorString("select failed")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestSourceInfoAndGuidance(t *testing.T) {
	info := CreateSourceInfo("/path", PathMetadata{Source: "npm:x", Scope: SourceScopeUser, Origin: SourceOriginPackage, BaseDir: "/base"})
	if info.Path != "/path" || info.Source != "npm:x" || info.Scope != SourceScopeUser ||
		info.Origin != SourceOriginPackage || info.BaseDir != "/base" {
		t.Fatalf("info = %+v", info)
	}
	synthetic := CreateSyntheticSourceInfo("/p", "local", SourceScopeProject, SourceOriginPackage, "/base")
	if synthetic.Scope != SourceScopeProject || synthetic.Origin != SourceOriginPackage {
		t.Fatalf("synthetic = %+v", synthetic)
	}

	if help := GetProviderLoginHelp(); !strings.Contains(help, "Use /login") || !strings.Contains(help, "providers.md") {
		t.Fatalf("help = %q", help)
	}
	if message := FormatNoModelsAvailableMessage(); !strings.HasPrefix(message, "No models available. Use /login") {
		t.Fatalf("message = %q", message)
	}
	if message := FormatNoModelSelectedMessage(); !strings.Contains(message, "Then use /model to select a model.") {
		t.Fatalf("message = %q", message)
	}
	if message := FormatNoAPIKeyFoundMessage("unknown"); !strings.HasPrefix(message, "No API key found for the selected model.") {
		t.Fatalf("message = %q", message)
	}
	if message := FormatNoAPIKeyFoundMessage("anthropic"); !strings.HasPrefix(message, "No API key found for anthropic.") {
		t.Fatalf("message = %q", message)
	}
}

func TestSettingsDiagnostics(t *testing.T) {
	dir := t.TempDir()
	// A malformed settings file records an error.
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte("{ bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewSettingsManagerFromFiles(dir, dir, SettingsManagerCreateOptions{})
	diagnostics := CollectSettingsDiagnostics(manager)
	if len(diagnostics) == 0 {
		t.Fatal("expected a diagnostic")
	}
	if diagnostics[0].Type != "warning" || !strings.Contains(diagnostics[0].Message, "Invalid settings file") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	// Draining clears them.
	if again := CollectSettingsDiagnostics(manager); len(again) != 0 {
		t.Fatalf("diagnostics = %+v", again)
	}

	duplicates := DeduplicateDiagnostics([]AgentSessionRuntimeDiagnostic{
		{Type: "warning", Message: "a"},
		{Type: "warning", Message: "a"},
		{Type: "error", Message: "a"},
		{Type: "warning", Message: "b"},
	})
	if len(duplicates) != 3 {
		t.Fatalf("duplicates = %+v", duplicates)
	}
}

func TestWaitForPipeDrain(t *testing.T) {
	// A finished stream returns immediately.
	var finished syncWaitGroup
	finished.Add(1)
	finished.Done()
	done := make(chan struct{})
	close(done)
	start := timeNowMS()
	waitForPipeDrain(&finished.WaitGroup, done, done)
	if timeNowMS()-start > 50 {
		t.Fatal("finished streams must not wait")
	}

	// A stream that keeps reporting activity keeps the drain waiting until it
	// finishes.
	var active syncWaitGroup
	active.Add(1)
	activity := make(chan struct{}, 1)
	startedCh := make(chan struct{}, 2)
	startedCh <- struct{}{}
	startedCh <- struct{}{}
	go func() {
		for index := 0; index < 5; index++ {
			activity <- struct{}{}
			timeSleepMS(20)
		}
		active.Done()
	}()
	start = timeNowMS()
	waitForPipeDrain(&active.WaitGroup, activity, startedCh)
	if elapsed := timeNowMS() - start; elapsed < 80 {
		t.Fatalf("drain returned early: %dms", elapsed)
	}

	// A quiet inherited handle releases after the grace window.
	var stuck syncWaitGroup
	alreadyStarted := make(chan struct{}, 2)
	alreadyStarted <- struct{}{}
	alreadyStarted <- struct{}{}
	stuck.Add(1)
	start = timeNowMS()
	waitForPipeDrain(&stuck.WaitGroup, make(chan struct{}), alreadyStarted)
	elapsed := timeNowMS() - start
	// The lower edge is the assertion — the grace window was honoured. The upper
	// edge only has to catch a drain that never returns, so it carries headroom
	// for a loaded runner.
	if elapsed < exitStdioGraceMS || elapsed > exitStdioGraceMS+2000 {
		t.Fatalf("grace elapsed = %dms", elapsed)
	}

}

// syncWaitGroup wraps sync.WaitGroup so the drain helper can be exercised.
type syncWaitGroup struct{ sync.WaitGroup }

func timeNowMS() int64 { return time.Now().UnixMilli() }

func timeSleepMS(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }
