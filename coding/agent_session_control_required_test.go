package coding

import (
	"testing"

	"github.com/dat267/pier/ai"
)

// TestNewAgentSessionHasCollaborators pins the constructor contract: a session
// always carries its collaborator block, so no method has to ask whether the
// session was half-built, and the accessors keep their documented empty answers
// for a session built without a runtime, settings, or tools.
func TestNewAgentSessionHasCollaborators(t *testing.T) {
	manager, _ := newTestSession(t)
	session, err := NewAgentSession(&SessionConfig{Cwd: t.TempDir(), Sessions: manager, StreamFn: stubStreamFn})
	if err != nil {
		t.Fatal(err)
	}
	if session.control == nil {
		t.Fatal("NewAgentSession returned a session without its collaborator block")
	}
	if runtime := session.ModelRuntime(); runtime != nil {
		t.Fatalf("model runtime = %v; want nil without one", runtime)
	}
	if models := session.ScopedModels(); models != nil {
		t.Fatalf("scoped models = %#v; want nil", models)
	}
	if templates := session.PromptTemplates(); templates != nil {
		t.Fatalf("prompt templates = %#v; want nil", templates)
	}
	if tools := session.GetAllTools(); tools != nil {
		t.Fatalf("tools = %#v; want nil", tools)
	}
	if tool := session.GetToolDefinition("read"); tool != nil {
		t.Fatalf("tool = %v; want nil", tool)
	}
	// These are no-ops on an empty registry instead of returning early.
	session.SetActiveToolsByName([]string{"read"})
	session.SetScopedModels([]ScopedModel{{Model: &ai.Model{Provider: "anthropic", ID: "claude-opus-4-5"}}})
	if models := session.ScopedModels(); len(models) != 1 {
		t.Fatalf("scoped models = %#v", models)
	}
	session.SetScopedModels(nil)
	if models := session.ScopedModels(); models != nil {
		t.Fatalf("scoped models = %#v after clearing; want nil", models)
	}
	session.SetAutoRetryEnabled(true)
	if !session.AutoRetryEnabled() {
		t.Fatal("the auto-retry toggle is not stored")
	}
	session.SetAutoCompactionEnabled(true)
	if !session.AutoCompactionEnabled() {
		t.Fatal("the auto-compaction toggle is not stored")
	}
}
