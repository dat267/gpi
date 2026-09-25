package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// stubModelSource is the model runtime as the scoped-model resolution sees it.
type stubModelSource struct {
	models []*ai.Model
}

func (s stubModelSource) GetModels(string) []*ai.Model { return s.models }
func (s stubModelSource) GetModel(providerID, modelID string) *ai.Model {
	for _, model := range s.models {
		if model.Provider == providerID && model.ID == modelID {
			return model
		}
	}
	return nil
}
func (s stubModelSource) GetAvailable(string, context.Context) ([]*ai.Model, error) {
	return s.models, nil
}
func (s stubModelSource) GetAvailableSnapshot() []*ai.Model { return s.models }
func (s stubModelSource) HasConfiguredAuth(string) bool     { return true }

func newTestSettings(t *testing.T) *coding.SettingsManager {
	t.Helper()
	isolatedAgentDir(t)
	cwd := t.TempDir()
	return coding.NewSettingsManagerFromFiles(cwd, coding.GetAgentDir(), coding.SettingsManagerCreateOptions{})
}

func scopeIDs(models []coding.ScopedModel) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.Model.Provider+"/"+model.Model.ID)
	}
	return ids
}

// --models wins over the settings' enabled models, and an absent flag falls
// back to them (upstream `parsed.models ?? getEnabledModels()`).
func TestScopedModelsFromFlag(t *testing.T) {
	settings := newTestSettings(t)
	settings.SetEnabledModels([]string{"settings-model"})
	source := stubModelSource{models: []*ai.Model{
		{Provider: "p", ID: "flag-model"},
		{Provider: "p", ID: "settings-model"},
	}}

	args := coding.ParseArgs([]string{"--models", "flag-model"})
	scoped, _, err := scopedModelsFor(args, settings, source, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := scopeIDs(scoped); len(got) != 1 || got[0] != "p/flag-model" {
		t.Errorf("scope = %v, want [p/flag-model] — the flag should win", got)
	}
}

func TestScopedModelsFallBackToSettings(t *testing.T) {
	settings := newTestSettings(t)
	settings.SetEnabledModels([]string{"settings-model"})
	source := stubModelSource{models: []*ai.Model{
		{Provider: "p", ID: "flag-model"},
		{Provider: "p", ID: "settings-model"},
	}}

	args := coding.ParseArgs(nil)
	scoped, _, err := scopedModelsFor(args, settings, source, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := scopeIDs(scoped); len(got) != 1 || got[0] != "p/settings-model" {
		t.Errorf("scope = %v, want [p/settings-model]", got)
	}
}

func TestScopedModelsEmptyWhenNothingConfigured(t *testing.T) {
	settings := newTestSettings(t)
	source := stubModelSource{models: []*ai.Model{{Provider: "p", ID: "model"}}}

	args := coding.ParseArgs(nil)
	scoped, _, err := scopedModelsFor(args, settings, source, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 0 {
		t.Errorf("scope = %v, want empty", scopeIDs(scoped))
	}
}

func TestScopedModelsReportsUnknownPatterns(t *testing.T) {
	settings := newTestSettings(t)
	source := stubModelSource{models: []*ai.Model{{Provider: "p", ID: "model"}}}

	args := coding.ParseArgs([]string{"--models", "missing"})
	scoped, diagnostics, err := scopedModelsFor(args, settings, source, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 0 {
		t.Errorf("scope = %v, want empty for an unmatched pattern", scopeIDs(scoped))
	}
	// The pattern is reported rather than silently scoping nothing.
	if len(diagnostics) != 1 || diagnostics[0].Type != "warning" {
		t.Fatalf("diagnostics = %#v, want one warning", diagnostics)
	}
	if !strings.Contains(diagnostics[0].Message, "missing") {
		t.Errorf("message = %q, want it to name the pattern", diagnostics[0].Message)
	}
}

// @file arguments are read into the first message, ahead of the question.
func TestInitialPromptReadsFileArguments(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "notes.txt")
	if err := os.WriteFile(path, []byte("the file contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := coding.ParseArgs([]string{"@" + path, "explain this"})
	prompt, err := initialPromptFor(args, cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt.Message, "the file contents") {
		t.Errorf("Message = %q, want the file contents", prompt.Message)
	}
	if !strings.HasPrefix(prompt.Message, "<file name=") {
		t.Errorf("Message = %q, want the file block first", prompt.Message)
	}
	if !strings.HasSuffix(prompt.Message, "explain this") {
		t.Errorf("Message = %q, want the question last", prompt.Message)
	}
	if len(prompt.Rest) != 0 {
		t.Errorf("Rest = %#v, want nothing queued", prompt.Rest)
	}
}

func TestInitialPromptKeepsFollowUpsQueued(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "notes.txt")
	if err := os.WriteFile(path, []byte("contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := coding.ParseArgs([]string{"@" + path, "first", "second"})
	prompt, err := initialPromptFor(args, cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt.Message, "contents") || !strings.HasSuffix(prompt.Message, "first") {
		t.Errorf("Message = %q, want the file contents then the question", prompt.Message)
	}
	if strings.Contains(prompt.Message, "second") {
		t.Errorf("Message = %q, should not fold in the follow-up", prompt.Message)
	}
	if len(prompt.Rest) != 1 || prompt.Rest[0] != "second" {
		t.Errorf("Rest = %#v, want [second]", prompt.Rest)
	}
}

// Without @file arguments the positional messages are folded the same way, so
// the first one is still the prompt and the rest follow it.
func TestInitialPromptWithoutFileArguments(t *testing.T) {
	args := coding.ParseArgs([]string{"hello", "again"})
	prompt, err := initialPromptFor(args, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if prompt.Message != "hello" {
		t.Errorf("Message = %q, want hello", prompt.Message)
	}
	if len(prompt.Rest) != 1 || prompt.Rest[0] != "again" {
		t.Errorf("Rest = %#v, want [again]", prompt.Rest)
	}
}

func TestInitialPromptReportsUnreadableFiles(t *testing.T) {
	args := coding.ParseArgs([]string{"@" + filepath.Join(t.TempDir(), "missing.txt")})
	if _, err := initialPromptFor(args, t.TempDir(), ""); err == nil {
		t.Error("a missing @file argument should be an error, not silence")
	}
}

func TestSessionNameFor(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		want    string
		wantErr bool
	}{
		{"absent", nil, "", false},
		{"plain", []string{"--name", "my label"}, "my label", false},
		{"trimmed", []string{"--name", "  my label  "}, "my label", false},
		// Present but blank is an error rather than a silent no-op.
		{"empty value", []string{"--name", ""}, "", true},
		{"blank value", []string{"--name", "   "}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, err := sessionNameFor(coding.ParseArgs(tc.argv))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("name = %v, want an error", name)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if name != nil {
					t.Fatalf("name = %q, want nil", *name)
				}
				return
			}
			if name == nil || *name != tc.want {
				t.Fatalf("name = %v, want %q", name, tc.want)
			}
		})
	}
}
