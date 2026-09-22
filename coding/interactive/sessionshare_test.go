package interactive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// shareTestSession implements ShareSession.
type shareTestSession struct {
	state     ShareState
	exported  []string
	exportErr error
	manager   *coding.SessionManager
}

func (s *shareTestSession) GetState() ShareState { return s.state }
func (s *shareTestSession) ExportToHTML(outputPath string) (string, error) {
	if s.exportErr != nil {
		return "", s.exportErr
	}
	s.exported = append(s.exported, outputPath)
	return outputPath, nil
}
func (s *shareTestSession) ModelRuntime() *coding.ModelRuntime     { return nil }
func (s *shareTestSession) SessionManager() *coding.SessionManager { return s.manager }

func newShareTestContext(t *testing.T) (*ShareContext, *shareTestSession) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	manager.AppendMessage(ai.Message(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}}))
	session := &shareTestSession{manager: manager}
	screen := tui.NewMainScreen(&fakeRendererTerminal{width: 80, height: 24}, false, "")
	screen.DisableAutoRender()
	editorContainer := &tui.Container{}
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	editorContainer.AddChild(editor)
	share := &ShareContext{
		Session:         session,
		UI:              screen,
		EditorContainer: editorContainer,
		Editor:          editor,
		ShowStatus:      func(string) {},
		ShowError:       func(string) {},
		TempDir:         t.TempDir(),
	}
	return share, session
}

// TestShareTrailingEntries covers the pi.share entry payload.
func TestShareTrailingEntries(t *testing.T) {
	share, session := newShareTestContext(t)
	session.state = ShareState{
		SystemPrompt: "you are pi",
		Tools: []ShareTool{
			{Name: "read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	}
	parentID := "parent"
	entries := CreateShareTrailingEntries(session, &parentID, "2026-01-01T00:00:00Z")
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	entry := entries[0]
	if entry.Type != "custom" || entry.CustomType != "pi.share" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.ParentID == nil || *entry.ParentID != "parent" || entry.Timestamp != "2026-01-01T00:00:00Z" {
		t.Fatalf("entry = %+v", entry)
	}
	if len(entry.ID) != 8 {
		t.Fatalf("id = %q", entry.ID)
	}
	var data struct {
		SystemPrompt string `json:"systemPrompt"`
		Tools        []struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(entry.Data, &data); err != nil {
		t.Fatalf("data: %v", err)
	}
	if data.SystemPrompt != "you are pi" || len(data.Tools) != 1 || data.Tools[0].Name != "read" {
		t.Fatalf("data = %+v", data)
	}
	if string(data.Tools[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("parameters = %s", data.Tools[0].Parameters)
	}
	_ = share
}

// TestExportSessionForShare covers the share export.
func TestExportSessionForShare(t *testing.T) {
	_, session := newShareTestContext(t)
	session.state = ShareState{SystemPrompt: "prompt"}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := ExportSessionForShare(path, session, session.manager); err != nil {
		t.Fatalf("export: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, `"pi.share"`) || !strings.Contains(text, `"systemPrompt":"prompt"`) {
		t.Fatalf("content = %q", text)
	}
	// The exported file is a valid session.
	manager, err := coding.OpenSession(path, "", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(manager.GetEntries()) == 0 {
		t.Fatal("no entries")
	}
}

// TestShareViaRadius covers the Radius upload path and its fallback.
func TestShareViaRadius(t *testing.T) {
	share, session := newShareTestContext(t)
	statuses := []string{}
	errorsShown := []string{}
	share.ShowStatus = func(message string) { statuses = append(statuses, message) }
	share.ShowError = func(message string) { errorsShown = append(errorsShown, message) }
	share.RadiusToken = func(context.Context) string { return "token" }
	share.RadiusGateway = "https://radius.example"

	uploads := 0
	share.UploadArtifact = func(_ context.Context, body []byte, token string, gateway string) (string, error) {
		uploads++
		if token != "token" || gateway != "https://radius.example" {
			t.Fatalf("token=%q gateway=%q", token, gateway)
		}
		if !strings.Contains(string(body), "pi.share") {
			t.Fatalf("body = %q", body)
		}
		return "https://share.example/abc", nil
	}
	ShareSessionThroughRadius(context.Background(), *share)
	if uploads != 1 || len(statuses) != 1 || !strings.Contains(statuses[0], "https://share.example/abc") {
		t.Fatalf("uploads = %d statuses = %v", uploads, statuses)
	}
	_ = session

	// An upload failure reports the error and does not fall back.
	failing, _ := newShareTestContext(t)
	errorsShown = nil
	failing.ShowError = func(message string) { errorsShown = append(errorsShown, message) }
	failing.RadiusToken = func(context.Context) string { return "token" }
	failing.UploadArtifact = func(context.Context, []byte, string, string) (string, error) {
		return "", errors.New("boom")
	}
	ShareSessionThroughRadius(context.Background(), *failing)
	if len(errorsShown) != 1 || !strings.Contains(errorsShown[0], "Failed to upload Radius artifact: boom") {
		t.Fatalf("errors = %v", errorsShown)
	}
}

// TestShareViaGist covers the gist fallback.
func TestShareViaGist(t *testing.T) {
	share, session := newShareTestContext(t)
	statuses := []string{}
	errorsShown := []string{}
	share.ShowStatus = func(message string) { statuses = append(statuses, message) }
	share.ShowError = func(message string) { errorsShown = append(errorsShown, message) }
	share.RadiusToken = func(context.Context) string { return "" }
	share.CheckGitHubAuth = func() bool { return true }
	share.CreateGist = func(context.Context, string) (string, error) { return "https://gist.github.com/u/abc123", nil }
	share.ShareViewerURL = func(gistID string) string { return "https://pi.dev/share/" + gistID }
	ShareSessionThroughRadius(context.Background(), *share)
	if len(statuses) != 1 || !strings.Contains(statuses[0], "https://pi.dev/share/abc123") ||
		!strings.Contains(statuses[0], "https://gist.github.com/u/abc123") {
		t.Fatalf("statuses = %v", statuses)
	}
	if len(session.exported) != 1 {
		t.Fatalf("exported = %v", session.exported)
	}

	// Without GitHub auth the flow reports the hint.
	unauth, _ := newShareTestContext(t)
	unauth.ShowError = func(message string) { errorsShown = append(errorsShown, message) }
	unauth.RadiusToken = func(context.Context) string { return "" }
	unauth.CheckGitHubAuth = func() bool { return false }
	ShareSessionThroughRadius(context.Background(), *unauth)
	if !strings.Contains(errorsShown[len(errorsShown)-1], "GitHub CLI is not logged in") {
		t.Fatalf("errors = %v", errorsShown)
	}
}
