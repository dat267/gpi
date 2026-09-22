package interactive

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// TestAnsiToHTML pins the converter against upstream ansi-to-html.ts
// semantics: SGR styling spans, 256-color and RGB extensions, resets, and
// HTML escaping of the payload text.
func TestAnsiToHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain escapes nothing", "hello", "hello"},
		{"escapes html", `a<b>&"c"`, "a&lt;b&gt;&amp;&quot;c&quot;"},
		{"bold", "\x1b[1mbold\x1b[0m", `<span style="font-weight:bold">bold</span>`},
		{"standard fg", "\x1b[31mred\x1b[0m", `<span style="color:#800000">red</span>`},
		{"bright fg", "\x1b[91mbright\x1b[0m", `<span style="color:#ff0000">bright</span>`},
		{"bg", "\x1b[41;37mrev\x1b[0m", `<span style="color:#c0c0c0;background-color:#800000">rev</span>`},
		{"256 color", "\x1b[38;5;208morange\x1b[0m", `<span style="color:#ff8700">orange</span>`},
		{"rgb", "\x1b[38;2;12;34;56mx\x1b[0m", `<span style="color:rgb(12,34,56)">x</span>`},
		{"dim italic underline", "\x1b[2;3;4ms\x1b[0m", `<span style="opacity:0.6;font-style:italic;text-decoration:underline">s</span>`},
		{"reset between", "\x1b[1ma\x1b[0mb", `<span style="font-weight:bold">a</span>b`},
		{"empty reset only", "\x1b[0m", ""},
	}
	for _, testCase := range cases {
		if got := AnsiToHTML(testCase.in); got != testCase.want {
			t.Errorf("%s: got %q want %q", testCase.name, got, testCase.want)
		}
	}
	if got := AnsiLinesToHTML([]string{"a", ""}); got != `<div class="ansi-line">a</div><div class="ansi-line">&nbsp;</div>` {
		t.Errorf("lines: %q", got)
	}
}

// TestExportSessionToHTML drives a real session file through the exporter
// and pins the document structure plus the embedded session payload.
func TestExportSessionToHTML(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	dir := t.TempDir()
	manager := coding.NewSessionManager(dir, &coding.SessionManagerOptions{Persist: boolPtr(true)})
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic"}
	agentSession, sessionErr := coding.NewAgentSession(&coding.SessionConfig{
		Cwd: dir, Model: model, StreamFn: func(*ai.Model, ai.TranscriptContext, *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return nil
		},
	})
	if sessionErr != nil {
		t.Fatalf("session: %v", sessionErr)
	}
	agentSession.Sessions = manager
	userMessage := &ai.UserMessage{Content: ai.StringOrBlocks{Text: "export me"}}
	manager.AppendMessage(userMessage)
	assistant := &ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "here you go"}}, StopReason: ai.StopStop}
	manager.AppendMessage(assistant)

	session := &AppSession{AgentSession: agentSession}
	// Upstream's default output path is a relative file in process.cwd().
	defaultOutput, err := session.ExportSessionToHTML("", "dark")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	wantName := coding.AppName + "-session-" + strings.TrimSuffix(filepath.Base(manager.GetSessionFile()), ".jsonl") + ".html"
	if defaultOutput != wantName {
		t.Fatalf("output = %q want %q", defaultOutput, wantName)
	}
	os.Remove(defaultOutput)

	output := filepath.Join(dir, "export.html")
	if _, err := session.ExportSessionToHTML(output, "dark"); err != nil {
		t.Fatalf("export: %v", err)
	}
	html, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	document := string(html)
	for _, placeholder := range []string{"{{CSS}}", "{{JS}}", "{{SESSION_DATA}}", "{{MARKED_JS}}", "{{HIGHLIGHT_JS}}", "{{THEME_VARS}}", "{{BODY_BG}}"} {
		if strings.Contains(document, placeholder) {
			t.Errorf("placeholder %s not substituted", placeholder)
		}
	}
	// Decode the embedded payload and check the session data round-trips.
	start := strings.Index(document, `<script id="session-data" type="application/json">`) + len(`<script id="session-data" type="application/json">`)
	end := strings.Index(document[start:], "</script>")
	payload, err := base64.StdEncoding.DecodeString(document[start : start+end])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var data struct {
		Header  json.RawMessage `json:"header"`
		Entries []struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"entries"`
		LeafID        *string                    `json:"leafId"`
		SystemPrompt  string                     `json:"systemPrompt"`
		RenderedTools map[string]json.RawMessage `json:"renderedTools"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	if data.Header == nil || len(data.Entries) != 2 || data.LeafID == nil {
		t.Fatalf("payload shape: header=%v entries=%d leaf=%v", data.Header, len(data.Entries), data.LeafID)
	}
	if !strings.Contains(string(data.Entries[0].Message.Content), "export me") {
		t.Fatalf("user message missing: %s", data.Entries[0].Message.Content)
	}
	// Bash/read/write/edit/ls are template-rendered; nothing pre-rendered here.
	if len(data.RenderedTools) != 0 {
		t.Fatalf("renderedTools = %v", data.RenderedTools)
	}
}

// TestExportSessionToHTMLErrors pins the upstream error messages.
func TestExportSessionToHTMLErrors(t *testing.T) {
	dir := t.TempDir()
	manager := coding.NewSessionManager(dir, &coding.SessionManagerOptions{Persist: boolPtr(true)})
	session := &AppSession{AgentSession: &coding.AgentSession{Sessions: manager}}
	if _, err := session.ExportSessionToHTML("", ""); err == nil || err.Error() != "Nothing to export yet - start a conversation first" {
		t.Fatalf("err = %v", err)
	}
	empty := coding.NewSessionManager(dir, &coding.SessionManagerOptions{Persist: boolPtr(false)})
	bare := &AppSession{AgentSession: &coding.AgentSession{Sessions: empty}}
	if _, err := bare.ExportSessionToHTML("", ""); err == nil || err.Error() != "Cannot export in-memory session to HTML" {
		t.Fatalf("err = %v", err)
	}
}
