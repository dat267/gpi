package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/coding"
)

// Project trust at startup: the override, the store's decision, the
// defaultProjectTrust setting, and finally the startup prompt. This is what
// used to be missing entirely — every project counted as trusted, so its .pi
// settings and resources were read without a decision.

func trustTestProject(t *testing.T) (cwd string, agentDir string, bootstrap *coding.SettingsManager) {
	t.Helper()
	cwd = t.TempDir()
	agentDir = t.TempDir()
	// A trust-requiring project resource, so the fast path does not apply.
	if err := os.MkdirAll(filepath.Join(cwd, coding.ConfigDirName, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	untrusted := false
	bootstrap = coding.NewSettingsManagerFromFiles(cwd, agentDir, coding.SettingsManagerCreateOptions{
		ProjectTrusted: &untrusted,
	})
	return cwd, agentDir, bootstrap
}

func trustTestOptions(cwd, agentDir string, bootstrap *coding.SettingsManager) startupTrustOptions {
	return startupTrustOptions{cwd: cwd, agentDir: agentDir, bootstrap: bootstrap, hasUI: true}
}

func TestStartupTrustWithoutRequiringResources(t *testing.T) {
	plain := t.TempDir()
	agentDir := t.TempDir()
	untrusted := false
	bootstrap := coding.NewSettingsManagerFromFiles(plain, agentDir, coding.SettingsManagerCreateOptions{
		ProjectTrusted: &untrusted,
	})
	options := trustTestOptions(plain, agentDir, bootstrap)
	options.prompt = func(string, []string) (string, error) {
		t.Error("a project without trust-requiring resources must not ask")
		return "", nil
	}
	trusted, err := resolveStartupProjectTrust(options)
	if err != nil || !trusted {
		t.Fatalf("trusted = %v, err = %v", trusted, err)
	}
}

func TestStartupTrustStoredDecisionWins(t *testing.T) {
	cwd, agentDir, bootstrap := trustTestProject(t)
	decision := false
	if err := coding.NewProjectTrustStore(agentDir).Set(cwd, &decision); err != nil {
		t.Fatal(err)
	}
	// The default policy would say otherwise; the stored decision comes first.
	bootstrap.SetDefaultProjectTrust("always")
	options := trustTestOptions(cwd, agentDir, bootstrap)
	options.prompt = func(string, []string) (string, error) {
		t.Error("a stored decision must not ask")
		return "", nil
	}
	trusted, err := resolveStartupProjectTrust(options)
	if err != nil || trusted {
		t.Fatalf("trusted = %v, err = %v, want the stored \"Do not trust\"", trusted, err)
	}
}

func TestStartupTrustOverrideShortCircuits(t *testing.T) {
	cwd, agentDir, bootstrap := trustTestProject(t)
	decision := false
	if err := coding.NewProjectTrustStore(agentDir).Set(cwd, &decision); err != nil {
		t.Fatal(err)
	}
	yes := true
	options := trustTestOptions(cwd, agentDir, bootstrap)
	options.override = &yes
	options.prompt = func(string, []string) (string, error) {
		t.Error("--approve settles trust without asking")
		return "", nil
	}
	trusted, err := resolveStartupProjectTrust(options)
	if err != nil || !trusted {
		t.Fatalf("trusted = %v, err = %v, want the override", trusted, err)
	}
	// The override never writes to the store.
	if stored := coding.NewProjectTrustStore(agentDir).Get(cwd); stored != nil && *stored {
		t.Error("--approve must not update the trust store")
	}
}

func TestStartupTrustDefaultPolicy(t *testing.T) {
	cwd, agentDir, bootstrap := trustTestProject(t)
	options := trustTestOptions(cwd, agentDir, bootstrap)
	options.prompt = func(string, []string) (string, error) {
		t.Error("always/never must not ask")
		return "", nil
	}

	bootstrap.SetDefaultProjectTrust("never")
	if trusted, err := resolveStartupProjectTrust(options); err != nil || trusted {
		t.Fatalf("never: trusted = %v, err = %v", trusted, err)
	}
	bootstrap.SetDefaultProjectTrust("always")
	if trusted, err := resolveStartupProjectTrust(options); err != nil || !trusted {
		t.Fatalf("always: trusted = %v, err = %v", trusted, err)
	}
}

func TestStartupTrustPromptDecidesAndPersists(t *testing.T) {
	cwd, agentDir, bootstrap := trustTestProject(t)
	var asked string
	var offered []string
	options := trustTestOptions(cwd, agentDir, bootstrap)
	options.prompt = func(prompt string, choices []string) (string, error) {
		asked = prompt
		offered = choices
		return "Trust", nil
	}
	trusted, err := resolveStartupProjectTrust(options)
	if err != nil || !trusted {
		t.Fatalf("trusted = %v, err = %v", trusted, err)
	}
	if !strings.Contains(asked, cwd) || !strings.Contains(asked, "Trust project folder?") {
		t.Errorf("prompt = %q", asked)
	}
	if len(offered) == 0 || offered[0] != "Trust" {
		t.Errorf("options = %v", offered)
	}
	// "Trust" is a stored decision, so the next run does not ask again.
	stored := coding.NewProjectTrustStore(agentDir).Get(cwd)
	if stored == nil || !*stored {
		t.Errorf("stored decision = %v, want true", stored)
	}
}

func TestStartupTrustCancelledPromptIsUntrusted(t *testing.T) {
	cwd, agentDir, bootstrap := trustTestProject(t)
	options := trustTestOptions(cwd, agentDir, bootstrap)
	options.prompt = func(string, []string) (string, error) { return "", nil }
	trusted, err := resolveStartupProjectTrust(options)
	if err != nil || trusted {
		t.Fatalf("trusted = %v, err = %v, want untrusted after a cancelled prompt", trusted, err)
	}
}

func TestStartupTrustSessionOnlyChoiceIsNotPersisted(t *testing.T) {
	cwd, agentDir, bootstrap := trustTestProject(t)
	options := trustTestOptions(cwd, agentDir, bootstrap)
	options.prompt = func(string, []string) (string, error) { return "Trust (this session only)", nil }
	trusted, err := resolveStartupProjectTrust(options)
	if err != nil || !trusted {
		t.Fatalf("trusted = %v, err = %v", trusted, err)
	}
	if stored := coding.NewProjectTrustStore(agentDir).Get(cwd); stored != nil {
		t.Errorf("a session-only choice must not be saved: %v", *stored)
	}
}

func TestStartupTrustWithoutUI(t *testing.T) {
	cwd, agentDir, bootstrap := trustTestProject(t)
	options := trustTestOptions(cwd, agentDir, bootstrap)
	options.hasUI = false
	options.prompt = func(string, []string) (string, error) {
		t.Error("no UI means no question")
		return "", nil
	}
	trusted, err := resolveStartupProjectTrust(options)
	if err != nil || trusted {
		t.Fatalf("trusted = %v, err = %v, want untrusted without a UI", trusted, err)
	}
}
