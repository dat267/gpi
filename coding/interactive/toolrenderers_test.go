package interactive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

func homeDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func newRendererTestTheme(t *testing.T) *Theme {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)
	return ActiveTheme()
}

func renderCall(name string, args any, theme *Theme) string {
	renderers := WithBuiltInRenderers(name, nil)
	if renderers == nil {
		return ""
	}
	component := renderers.RenderCall(args, theme, &ToolRenderContext{Cwd: "/tmp/proj"})
	return coding.StripAnsi(strings.Join(component.Render(120), "\n"))
}

func renderResult(name string, result *SortToolResultContent, expanded bool, theme *Theme) string {
	renderers := WithBuiltInRenderers(name, nil)
	if renderers == nil {
		return ""
	}
	component := renderers.RenderResult(result, ToolRenderResultOptions{Expanded: expanded}, theme,
		&ToolRenderContext{Cwd: "/tmp/proj", Args: nil, ShowImages: false})
	return coding.StripAnsi(strings.Join(component.Render(120), "\n"))
}

func textResult(text string) *SortToolResultContent {
	return &SortToolResultContent{Content: []ToolResultContent{{Type: "text", Text: text}}}
}

// TestToolRenderersCallHeaders covers the upstream call headers (the ones that
// used to render as raw JSON).
func TestToolRenderersCallHeaders(t *testing.T) {
	theme := newRendererTestTheme(t)

	args := func(raw string) any { return json.RawMessage(raw) }

	if got := renderCall("read", args(`{"file_path":"/tmp/proj/main.go"}`), theme); !strings.Contains(got, "read /tmp/proj/main.go") {
		t.Fatalf("read call = %q", got)
	}
	// Build the home case with json.Marshal: concatenating a Windows path into
	// JSON leaves its backslashes unescaped (\U, \n), which is invalid JSON and
	// renders as the empty-path fallback. upstream shortenPath swaps the home
	// prefix for "~" and keeps the remainder verbatim, so the separator here is
	// the platform's.
	homePath := filepath.Join(homeDir(t), "notes.md")
	homeArgs, err := json.Marshal(map[string]string{"file_path": homePath})
	if err != nil {
		t.Fatal(err)
	}
	if got := renderCall("read", args(string(homeArgs)), theme); !strings.Contains(got, "read ~"+strings.TrimPrefix(homePath, homeDir(t))) {
		t.Fatalf("read home call = %q", got)
	}
	if got := renderCall("read", args(`{"file_path":"/tmp/proj/main.go","offset":10,"limit":5}`), theme); !strings.Contains(got, ":10-14") {
		t.Fatalf("read call range = %q", got)
	}
	if got := renderCall("bash", args(`{"command":"ls -la"}`), theme); !strings.Contains(got, "$ ls -la") {
		t.Fatalf("bash call = %q", got)
	}
	if got := renderCall("grep", args(`{"pattern":"TODO","path":"/tmp/proj/src"}`), theme); !strings.Contains(got, "grep /TODO/ in /tmp/proj/src") {
		t.Fatalf("grep call = %q", got)
	}
	if got := renderCall("find", args(`{"pattern":"*.go"}`), theme); !strings.Contains(got, "find *.go in .") {
		t.Fatalf("find call = %q", got)
	}
	if got := renderCall("ls", args(`{}`), theme); !strings.Contains(got, "ls .") {
		t.Fatalf("ls call = %q", got)
	}
	if got := renderCall("write", args(`{"file_path":"/tmp/proj/a.go","content":"package main"}`), theme); !strings.Contains(got, "write /tmp/proj/a.go") {
		t.Fatalf("write call = %q", got)
	}
	if got := renderCall("edit", args(`{"file_path":"/tmp/proj/a.go","oldText":"a","newText":"b"}`), theme); !strings.Contains(got, "edit /tmp/proj/a.go") {
		t.Fatalf("edit call = %q", got)
	}
	// No raw JSON in any header.
	for _, name := range BuiltinToolRendererNames() {
		got := renderCall(name, args(`{"file_path":"/tmp/proj/main.go"}`), theme)
		if strings.Contains(got, "{") && strings.Contains(got, "\"file_path\"") {
			t.Errorf("%s call renders raw JSON: %q", name, got)
		}
	}
}

// TestToolRenderersResults covers the collapsed/expanded result bodies.
func TestToolRenderersResults(t *testing.T) {
	theme := newRendererTestTheme(t)

	// Collapsed (not expanded) results are empty for read.
	if got := renderResult("read", textResult("hello"), false, theme); strings.Contains(got, "hello") {
		t.Fatalf("collapsed read result should be empty: %q", got)
	}
	expanded := renderResult("read", textResult("hello"), true, theme)
	if !strings.Contains(expanded, "hello") {
		t.Fatalf("expanded read result = %q", expanded)
	}

	// ls collapses to 20 lines with a truncation hint.
	many := make([]string, 30)
	for index := range many {
		many[index] = "entry"
	}
	lsResult := renderResult("ls", textResult(strings.Join(many, "\n")), false, theme)
	if !strings.Contains(lsResult, "... (10 more lines,") {
		t.Fatalf("ls truncation hint missing: %q", lsResult)
	}

	// Truncation details surface a warning.
	details := &SortToolResultContent{
		Content: []ToolResultContent{{Type: "text", Text: "out"}},
		Details: &coding.LsToolDetails{Truncation: &coding.TruncationResult{
			Truncated: true, TruncatedBy: "bytes", OutputLines: 20, MaxBytes: 50 * 1024,
		}},
	}
	if got := renderResult("ls", details, false, theme); !strings.Contains(got, "[Truncated:") {
		t.Fatalf("ls truncation warning missing: %q", got)
	}
}

// TestEditRendererPreview covers the edit preview diff.
func TestEditRendererPreview(t *testing.T) {
	newRendererTestTheme(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preview := computeEditsPreview(file, []coding.Edit{{OldText: "two", NewText: "TWO"}}, dir)
	if preview == nil || preview.Error != "" {
		t.Fatalf("preview = %+v", preview)
	}
	if !strings.Contains(preview.Diff, "TWO") || !strings.Contains(preview.Diff, "one") {
		t.Fatalf("preview diff = %q", preview.Diff)
	}
	if preview.FirstChangedLine == nil || *preview.FirstChangedLine != 2 {
		t.Fatalf("firstChangedLine = %+v", preview.FirstChangedLine)
	}

	// A missing file reports the upstream error shape.
	missing := computeEditsPreview(filepath.Join(dir, "gone.txt"), []coding.Edit{{OldText: "a", NewText: "b"}}, dir)
	if missing == nil || !strings.Contains(missing.Error, "Could not edit file") {
		t.Fatalf("missing preview = %+v", missing)
	}
}

// TestWithBuiltInRenderers covers the merge semantics.
func TestWithBuiltInRenderers(t *testing.T) {
	// Unknown tools pass the definition through (nil stays nil).
	if got := WithBuiltInRenderers("unknown-tool", nil); got != nil {
		t.Fatalf("unknown tool = %+v", got)
	}
	custom := &ToolRenderers{RenderShell: "self"}
	if got := WithBuiltInRenderers("unknown-tool", custom); got != custom {
		t.Fatalf("unknown tool with definition = %+v", got)
	}
	// Built-in merge keeps the custom call renderer and fills the result.
	merged := WithBuiltInRenderers("read", &ToolRenderers{RenderShell: "self"})
	if merged == nil || merged.RenderShell != "self" || merged.RenderCall == nil || merged.RenderResult == nil {
		t.Fatalf("merged = %+v", merged)
	}
}

// TestToolRendererShellComponent verifies the collapsed bash preview component
// renders the tail of the output.
func TestToolRendererShellComponent(t *testing.T) {
	theme := newRendererTestTheme(t)
	lines := make([]string, 40)
	for index := range lines {
		lines[index] = "line"
	}
	result := textResult(strings.Join(lines, "\n"))
	component := bashRenderers.RenderResult(result, ToolRenderResultOptions{Expanded: false}, theme,
		&ToolRenderContext{Cwd: "/tmp/proj"})
	rendered := coding.StripAnsi(strings.Join(component.Render(120), "\n"))
	if !strings.Contains(rendered, "earlier lines") {
		t.Fatalf("bash preview = %q", rendered)
	}
	if !strings.Contains(rendered, "line") {
		t.Fatalf("bash preview missing output: %q", rendered)
	}
	_ = tui.NewSpacer(1)
}

// TestShellElapsedDuration covers the running-time display for shell tools: a
// partial result keeps the timer running and the label keeps ticking (upstream
// arms a 1s invalidate), and the final result freezes it as "Took". The port
// set endedAtMS on the *partial* update (inverting upstream's
// `!isPartial || isError`), so the timer froze at the first update, and it
// never armed a redraw, so the elapsed value never advanced.
func TestShellElapsedDuration(t *testing.T) {
	theme := newRendererTestTheme(t)
	ctx := &ToolRenderContext{Cwd: "/tmp/proj", Args: map[string]any{"command": "sleep 5"}, ExecutionStarted: true}
	bashRenderers.RenderCall(ctx.Args, theme, ctx)
	state, ok := ctx.State.(*shellCallState)
	if !ok || state.startedAtMS == 0 {
		t.Fatalf("execution start did not start the timer: %+v", ctx.State)
	}

	result := textResult("partial output")
	component := bashRenderers.RenderResult(result, ToolRenderResultOptions{IsPartial: true}, theme, ctx)
	if state.endedAtMS != 0 {
		t.Fatal("a partial result must not stop the timer")
	}

	// The running label recomputes from the clock on every render.
	state.startedAtMS = time.Now().Add(-5 * time.Second).UnixMilli()
	rendered := coding.StripAnsi(strings.Join(component.Render(120), "\n"))
	if !strings.Contains(rendered, "Elapsed 5.") {
		t.Fatalf("running duration = %q, want an Elapsed 5.x", rendered)
	}

	// The component animates while the call runs so the label keeps ticking.
	container, ok := component.(*tui.Container)
	if !ok {
		t.Fatalf("result component = %T", component)
	}
	animated := false
	for _, child := range container.Children {
		if animator, ok := child.(tui.Animator); ok {
			want, delay := animator.AnimationFrame(time.Now())
			if !want || delay <= 0 || delay > time.Second {
				t.Fatalf("running animator = want %v delay %v", want, delay)
			}
			animated = true
		}
	}
	if !animated {
		t.Fatal("running tool has no animator driving the elapsed label")
	}

	// The final result freezes the label as "Took".
	component = bashRenderers.RenderResult(result, ToolRenderResultOptions{}, theme, ctx)
	if state.endedAtMS == 0 {
		t.Fatal("the final result must stop the timer")
	}
	rendered = coding.StripAnsi(strings.Join(component.Render(120), "\n"))
	if !strings.Contains(rendered, "Took ") {
		t.Fatalf("final duration = %q, want a Took line", rendered)
	}
	for _, child := range component.(*tui.Container).Children {
		if animator, ok := child.(tui.Animator); ok {
			if want, _ := animator.AnimationFrame(time.Now()); want {
				t.Fatal("a finished tool must not keep animating")
			}
		}
	}
}
