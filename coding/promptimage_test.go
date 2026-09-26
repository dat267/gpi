package coding

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// capturePromptSession builds a session whose stream captures the request and
// answers with a stop, so tests can inspect what the model would receive.
func capturePromptSession(t *testing.T, model *ai.Model, settings *SettingsManager) (*AgentSession, *[]ai.Message) {
	t.Helper()
	captured := &[]ai.Message{}
	streamFn := func(m *ai.Model, c ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		*captured = append([]ai.Message{}, c.Messages...)
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: m.Provider, Model: m.ID,
				Content: ai.ContentList{ai.TextContent{Text: "ok"}}, StopReason: ai.StopStop,
			}})
		}()
		return stream
	}
	session, err := NewAgentSession(&SessionConfig{
		Cwd:      t.TempDir(),
		Model:    model,
		StreamFn: streamFn,
		Control:  &AgentSessionControl{Settings: settings},
	})
	if err != nil {
		t.Fatal(err)
	}
	return session, captured
}

// firstUserContent returns the user message's text and first image block.
func firstUserContent(t *testing.T, messages []ai.Message) (string, *ai.ImageContent) {
	t.Helper()
	for _, message := range messages {
		user, ok := message.(*ai.UserMessage)
		if !ok {
			continue
		}
		text := ""
		for _, block := range user.Content.Blocks {
			switch value := block.(type) {
			case ai.TextContent:
				text += value.Text
			case ai.ImageContent:
				imageBlock := value
				return text, &imageBlock
			}
		}
		return text, nil
	}
	t.Fatal("no user message captured")
	return "", nil
}

func findSessionTool(session *AgentSession, name string) (agent.AgentTool, bool) {
	for _, tool := range session.Agent.State().Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return agent.AgentTool{}, false
}

// A prompt image is run through ProcessImage so the request's resize profile
// (the imageAutoResize setting and the model's image limits) applies before the
// message is built (upstream AgentSession._normalizePromptImages). Before this,
// attachments were appended raw and an oversized image could make the provider
// reject the whole conversation.
func TestPromptNormalizesImageAttachments(t *testing.T) {
	original := base64.StdEncoding.EncodeToString(solidPNG(t, 200, 200))
	model := &ai.Model{
		ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000,
		InputLimits: &ai.ModelInputLimits{Images: &ai.ModelImageLimits{
			Resize: &ai.ModelImageResize{MaxWidth: 20, MaxHeight: 20},
		}},
	}
	session, captured := capturePromptSession(t, model, nil)
	err := session.Prompt(context.Background(), "look", &PromptOptions{
		Images: []ai.ImageContent{{Data: original, MimeType: "image/png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, imageBlock := firstUserContent(t, *captured)
	if imageBlock == nil {
		t.Fatal("the user message carried no image")
	}
	if imageBlock.Data == original {
		t.Fatal("the prompt image was sent unnormalized; the model's resize limits were not applied")
	}
	raw, err := base64.StdEncoding.DecodeString(imageBlock.Data)
	if err != nil {
		t.Fatalf("normalized image is not base64: %v", err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("normalized image does not decode: %v", err)
	}
	if config.Width > 20 || config.Height > 20 {
		t.Fatalf("normalized image is %dx%d, want at most 20x20", config.Width, config.Height)
	}
}

// An image ProcessImage cannot prepare is omitted and its message is appended to
// the prompt text, so the model (and the user's transcript) sees why it is not
// there (upstream's hints join).
func TestPromptAppendsImageOmissionHint(t *testing.T) {
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000}
	session, captured := capturePromptSession(t, model, nil)
	err := session.Prompt(context.Background(), "look", &PromptOptions{
		Images: []ai.ImageContent{{Data: base64.StdEncoding.EncodeToString([]byte("not an image")), MimeType: "image/webp"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, imageBlock := firstUserContent(t, *captured)
	if imageBlock != nil {
		t.Fatalf("an unprocessable image was still attached: %#v", imageBlock)
	}
	if !strings.Contains(text, "[Image omitted:") {
		t.Fatalf("the omission hint is missing from %q", text)
	}
}

// The read tool takes the imageAutoResize setting at session build time
// (upstream createAllToolDefinitions reads it), so turning the setting off keeps
// a large image at its original size instead of resizing it.
func TestReadToolHonorsTheImageAutoResizeSetting(t *testing.T) {
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
	settings.SetImageAutoResize(false)
	result, err := CreateAgentSession(context.Background(), &CreateAgentSessionOptions{
		Cwd: t.TempDir(), ModelRuntime: runtime, SettingsManager: settings,
	})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "wide.png")
	if err := os.WriteFile(path, solidPNG(t, 2100, 4), 0o644); err != nil {
		t.Fatal(err)
	}
	readTool, ok := findSessionTool(result.Session, "read")
	if !ok {
		t.Fatal("the session has no read tool")
	}
	readResult := execTool(t, readTool, `{"path":"`+path+`"}`)
	if len(readResult.Content) != 2 {
		t.Fatalf("content = %#v, want text and image", readResult.Content)
	}
	imageContent := readResult.Content[1].(ai.ImageContent)
	data, err := base64.StdEncoding.DecodeString(imageContent.Data)
	if err != nil {
		t.Fatalf("image data is not base64: %v", err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("attached image does not decode: %v", err)
	}
	if config.Width != 2100 || config.Height != 4 {
		t.Fatalf("attached image is %dx%d, want the original 2100x4", config.Width, config.Height)
	}
}
