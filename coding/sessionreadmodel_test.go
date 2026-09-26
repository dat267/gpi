package coding

import (
	ctxpkg "context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

func sessionTool(t *testing.T, session *AgentSession, name string) agent.AgentTool {
	t.Helper()
	for _, tool := range session.Agent.State().Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("the session has no %q tool", name)
	return agent.AgentTool{}
}

// TestSessionReadToolResolvesItsModelPerCall pins the binding between the session
// and the read tool: the tool resolves the model per invocation through the
// session, which is upstream reading ctx.model. Without it the non-vision note
// never appeared in a real session, and a configured inputLimits block was
// ignored.
//
// The second half matters as much as the first: a snapshot of the model at
// construction would pass the text-only case and fail after the switch.
func TestSessionReadToolResolvesItsModelPerCall(t *testing.T) {
	tempAgentDir(t)
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(GetAgentDir(), "auth.json"),
		ModelsPath:      filepath.Join(GetAgentDir(), "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})

	dir := t.TempDir()
	imagePath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(imagePath, solidPNG(t, 16, 16), 0o644); err != nil {
		t.Fatal(err)
	}

	textOnly := &ai.Model{
		ID: "text-only", Name: "Text only", API: ai.APIAnthropicMessages, Provider: "anthropic",
		Input: []string{"text"}, ContextWindow: 100000, MaxTokens: 4096,
	}
	result, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: dir, Model: textOnly, ModelRuntime: runtime, SettingsManager: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := result.Session
	readTool := sessionTool(t, session, "read")

	params := `{"path":` + quoteJSONForTest(t, imagePath) + `}`
	text := execTool(t, readTool, params).Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "does not support images") {
		t.Fatalf("a text-only session got no non-vision note: %q", text)
	}

	// Replacing the model is visible to the next call. This goes through the
	// agent rather than session.SetModel, which validates credentials for the new
	// model: the point here is the getter's liveness, not the switch's auth path.
	vision := &ai.Model{
		ID: "vision", Name: "Vision", API: ai.APIAnthropicMessages, Provider: "anthropic",
		Input: []string{"text", "image"}, ContextWindow: 100000, MaxTokens: 4096,
	}
	session.Agent.SetModel(vision)
	text = execTool(t, readTool, params).Content[0].(ai.TextContent).Text
	if strings.Contains(text, "does not support images") {
		t.Fatalf("the tool kept the old model's capability: %q", text)
	}
	if !strings.Contains(text, "Read image file [image/png]") {
		t.Fatalf("text = %q", text)
	}
}

// quoteJSONForTest renders a path as a JSON string for tool parameters.
func quoteJSONForTest(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
