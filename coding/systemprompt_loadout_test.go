package coding

import (
	"context"
	"strings"
	"testing"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// TestPromptInstallsTheSystemPromptSections pins the missing loadout: the
// built prompt sections (preamble/rules/project_context/skills/...) must be
// installed as a system message before the request, the way upstream
// _preparePromptAndToolLoadout does. Without it the model receives an empty
// system prompt.
func TestPromptInstallsTheSystemPromptSections(t *testing.T) {
	var captured []ai.Message
	streamFn := func(model *ai.Model, c ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		captured = append([]ai.Message{}, c.Messages...)
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content: ai.ContentList{ai.TextContent{Text: "ok"}}, StopReason: ai.StopStop,
			}})
		}()
		return stream
	}
	session, err := NewAgentSession(&SessionConfig{
		Cwd:      t.TempDir(),
		Model:    &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000},
		StreamFn: streamFn,
		Tools: []agent.AgentTool{
			{Name: "read", Description: "read files", Parameters: []byte(`{"type":"object"}`)},
			{Name: "bash", Description: "run commands", Parameters: []byte(`{"type":"object"}`)},
		},
		Skills:       []Skill{{Name: "demo", Description: "Does the demo", FilePath: "/tmp/SKILL.md"}},
		ContextFiles: []ContextFile{{Path: "AGENTS.md", Content: "PROJECT RULE ONE"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.PromptText(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	prompt := ai.GetCurrentSystemPrompt(captured)
	for _, want := range []string{"PROJECT RULE ONE", "available_skills", "coding agent harness", "- read: Read file contents", "- bash: Execute bash commands"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q; got:\n%s", want, prompt)
		}
	}
	if tools := session.Agent.State().Tools; len(tools) != 2 {
		t.Fatalf("tools after the loadout = %d; want 2 (the loadout must not strip them)", len(tools))
	}
}
