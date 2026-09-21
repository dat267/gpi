package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dat267/pier/ai"
)

// Ports the compaction extension's tests (~/.pi/agent/extensions/compaction,
// summary.test.ts + index.test.ts): the pure summarizer helpers and the
// request routing (D142).

const previousWithLists = `## Goal
- Ship the compaction extension

## Key Decisions
- **Session model default**: no hardcoded chain.

<read-files>
/tmp/a.ts
/tmp/b.ts
</read-files>

<modified-files>
/tmp/c.ts
</modified-files>`

// TestBuildSummarizerPrompt ports summary.test.ts buildSummarizerPrompt.
func TestBuildSummarizerPrompt(t *testing.T) {
	prompt := BuildSummarizerPrompt("[User]: hi\n[Assistant]: hello", SummaryModeHistory, "", "")
	if !strings.HasPrefix(prompt, "<conversation>\n[User]: hi\n[Assistant]: hello\n</conversation>") {
		t.Fatalf("prompt prefix = %q", prompt[:60])
	}
	if !strings.Contains(prompt, BetterSummarizationPrompt) {
		t.Fatal("history prompt missing")
	}

	prompt = BuildSummarizerPrompt("conv", SummaryModeUpdate, "earlier summary", "")
	if !strings.Contains(prompt, "<previous-summary>\nearlier summary\n</previous-summary>") {
		t.Fatalf("previous summary = %q", prompt)
	}
	if !strings.Contains(prompt, BetterUpdateSummarizationPrompt) {
		t.Fatal("update prompt missing")
	}
	if strings.Index(prompt, "<conversation>") > strings.Index(prompt, "<previous-summary>") {
		t.Fatal("conversation must come first")
	}

	prompt = BuildSummarizerPrompt("conv", SummaryModeUpdate, "", "")
	if strings.Contains(prompt, "<previous-summary>\n") {
		t.Fatal("no previous summary expected")
	}
	if !strings.Contains(prompt, BetterSummarizationPrompt) {
		t.Fatal("update without a previous summary falls back to the history prompt")
	}

	prompt = BuildSummarizerPrompt("conv", SummaryModeTurnPrefix, "", "")
	if !strings.Contains(prompt, "PREFIX of a turn") {
		t.Fatalf("turn prefix prompt = %q", prompt)
	}
	if strings.Contains(prompt, "<previous-summary>") {
		t.Fatal("turn prefix must not carry a previous summary")
	}

	prompt = BuildSummarizerPrompt("conv", SummaryModeHistory, "", "keep the test plan")
	if !strings.HasSuffix(prompt, "Additional focus: keep the test plan") {
		t.Fatalf("custom instructions last = %q", prompt[len(prompt)-60:])
	}

	prompt = BuildSummarizerPrompt("conv", SummaryModeUpdate, previousWithLists, "")
	if strings.Contains(prompt, "<read-files>") {
		t.Fatal("stale read list must not be fed back")
	}
	if strings.Contains(prompt, "<modified-files>") {
		t.Fatal("stale modified list must not be fed back")
	}
	if !strings.Contains(prompt, "Ship the compaction extension") || !strings.Contains(prompt, "Session model default") {
		t.Fatal("the prose of the previous summary must survive")
	}
}

// TestSummaryPromptBudgets ports summary.test.ts "summary prompt budgets".
func TestSummaryPromptBudgets(t *testing.T) {
	if strings.Contains(BetterUpdateSummarizationPrompt, "PRESERVE all existing information") {
		t.Fatal("the append-everything rule is the ratchet; it must be gone")
	}
	for name, prompt := range map[string]string{
		"update":  BetterUpdateSummarizationPrompt,
		"initial": BetterSummarizationPrompt,
	} {
		if !strings.Contains(prompt, "HARD BUDGET") || !strings.Contains(prompt, "8,000 tokens") {
			t.Errorf("%s prompt missing the hard budget", name)
		}
	}
	if !strings.Contains(BetterUpdateSummarizationPrompt, "NEVER restate") {
		t.Error("update prompt must forbid restating the generated file lists")
	}
	if !strings.Contains(BetterUpdateSummarizationPrompt, `"## Goal" lists ONLY objectives still open`) {
		t.Error("update prompt must keep only open objectives")
	}
	if !strings.Contains(strings.ToLower(BetterUpdateSummarizationPrompt), "supersede") {
		t.Error("update prompt must drop superseded items")
	}
	if !strings.Contains(SummarizationSystemPrompt, "context summarization assistant") {
		t.Error("system prompt changed")
	}
}

// TestIsUsableSummary ports summary.test.ts isUsableSummary.
func TestIsUsableSummary(t *testing.T) {
	if IsUsableSummary("") || IsUsableSummary("   \n\t ") {
		t.Fatal("empty output must be rejected")
	}
	if IsUsableSummary("Summary.") {
		t.Fatal("implausibly short output must be rejected")
	}
	if !IsUsableSummary("## Session summary\n\nThe user refactored the goal extension...") {
		t.Fatal("substantive summaries must be accepted")
	}
}

// TestStripFileListSections ports summary.test.ts stripFileListSections.
func TestStripFileListSections(t *testing.T) {
	out := StripFileListSections(previousWithLists)
	if strings.Contains(out, "read-files") || strings.Contains(out, "modified-files") || strings.Contains(out, "/tmp/a.ts") {
		t.Fatalf("blocks not removed: %q", out)
	}
	if !strings.HasPrefix(out, "## Goal\n- Ship the compaction extension") || !strings.Contains(out, "no hardcoded chain") {
		t.Fatalf("prose damaged: %q", out)
	}
	plain := "## Goal\n- plain"
	if StripFileListSections(plain) != plain {
		t.Fatal("not a no-op for summaries without lists")
	}
	once := StripFileListSections(previousWithLists)
	if StripFileListSections(once) != once {
		t.Fatal("not idempotent")
	}
	emitted := FormatFileOperationsCapped(FileLists{
		ReadFiles: []string{"a.ts"}, ModifiedFiles: []string{"m.ts"},
		OmittedRead: 7, OmittedModified: 0,
	})
	if got := StripFileListSections("## Goal\n- x\n" + emitted); got != "## Goal\n- x" {
		t.Fatalf("strip must match the emitted block shape: got %q", got)
	}
}

// TestComputeFileListsCapped ports summary.test.ts computeFileLists.
func TestComputeFileListsCapped(t *testing.T) {
	fileOps := CreateFileOps()
	for _, f := range []string{"a.ts", "b.ts"} {
		fileOps.markRead(f)
	}
	fileOps.markWritten("b.ts")
	fileOps.markEdited("c.ts")
	lists := ComputeFileListsCapped(fileOps, MaxListedFiles)
	if len(lists.ReadFiles) != 1 || lists.ReadFiles[0] != "a.ts" {
		t.Fatalf("readFiles = %v", lists.ReadFiles)
	}
	if len(lists.ModifiedFiles) != 2 || lists.ModifiedFiles[0] != "b.ts" || lists.ModifiedFiles[1] != "c.ts" {
		t.Fatalf("modifiedFiles = %v", lists.ModifiedFiles)
	}
	if lists.OmittedRead != 0 || lists.OmittedModified != 0 {
		t.Fatalf("omitted = %d/%d", lists.OmittedRead, lists.OmittedModified)
	}

	// Caps each list to the most recent entries and counts the overflow.
	manyReads := CreateFileOps()
	for i := 0; i <= MaxListedFiles+4; i++ {
		manyReads.markRead(fmt.Sprintf("r%03d.ts", i))
	}
	for i := 0; i <= MaxListedFiles+2; i++ {
		manyReads.markEdited(fmt.Sprintf("e%03d.ts", i))
	}
	lists = ComputeFileListsCapped(manyReads, MaxListedFiles)
	if len(lists.ReadFiles) != MaxListedFiles || lists.OmittedRead != 5 {
		t.Fatalf("read cap = %d omitted %d", len(lists.ReadFiles), lists.OmittedRead)
	}
	for _, f := range lists.ReadFiles {
		if f == "r004.ts" {
			t.Error("the 5 oldest read paths must be dropped")
		}
	}
	if !hasString(lists.ReadFiles, "r005.ts") || !hasString(lists.ReadFiles, "r044.ts") {
		t.Error("oldest survivor and newest read path must survive")
	}
	if len(lists.ModifiedFiles) != MaxListedFiles || lists.OmittedModified != 3 {
		t.Fatalf("modified cap = %d omitted %d", len(lists.ModifiedFiles), lists.OmittedModified)
	}
	if hasString(lists.ModifiedFiles, "e000.ts") || !hasString(lists.ModifiedFiles, "e042.ts") {
		t.Error("modified cap kept the wrong tail")
	}

	// Honors a smaller explicit cap.
	small := CreateFileOps()
	for _, f := range []string{"a", "b", "c", "d"} {
		small.markRead(f)
	}
	lists = ComputeFileListsCapped(small, 2)
	if len(lists.ReadFiles) != 2 || lists.ReadFiles[0] != "c" || lists.ReadFiles[1] != "d" || lists.OmittedRead != 2 {
		t.Fatalf("small cap = %v omitted %d", lists.ReadFiles, lists.OmittedRead)
	}

	// Keeps modified output sorted for stable diffs.
	sorted := CreateFileOps()
	sorted.markWritten("z.ts")
	sorted.markEdited("a.ts")
	lists = ComputeFileListsCapped(sorted, MaxListedFiles)
	if len(lists.ModifiedFiles) != 2 || lists.ModifiedFiles[0] != "a.ts" || lists.ModifiedFiles[1] != "z.ts" {
		t.Fatalf("sorted modified = %v", lists.ModifiedFiles)
	}
}

func hasString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// TestFormatFileOperationsCapped ports summary.test.ts formatFileOperations.
func TestFormatFileOperationsCapped(t *testing.T) {
	if FormatFileOperationsCapped(FileLists{}) != "" {
		t.Fatal("no file ops must render empty")
	}
	out := FormatFileOperationsCapped(FileLists{
		ReadFiles: []string{"z.ts", "a.ts"}, ModifiedFiles: []string{"m.ts"},
	})
	if !strings.Contains(out, "<read-files>\na.ts\nz.ts\n</read-files>") {
		t.Fatalf("read section = %q", out)
	}
	if !strings.Contains(out, "<modified-files>\nm.ts\n</modified-files>") {
		t.Fatalf("modified section = %q", out)
	}
	if strings.Contains(out, "omitted") {
		t.Fatal("no omission marker when nothing was dropped")
	}
	out = FormatFileOperationsCapped(FileLists{
		ReadFiles: []string{"a.ts"}, ModifiedFiles: []string{"m.ts"},
		OmittedRead: 7,
	})
	if !strings.Contains(out, "7 older path(s) omitted") {
		t.Fatalf("omission marker = %q", out)
	}
	if strings.Contains(out[strings.Index(out, "modified-files"):], "omitted") {
		t.Fatal("the modified block must not carry a marker")
	}
}

// TestParseCompactModelOverride ports index.test.ts pickSummarizer override
// parsing.
func TestParseCompactModelOverride(t *testing.T) {
	if _, _, ok := ParseCompactModelOverride(""); ok {
		t.Fatal("empty override must be ignored")
	}
	if _, _, ok := ParseCompactModelOverride("hyper"); ok {
		t.Fatal("override without a slash must be ignored")
	}
	if _, _, ok := ParseCompactModelOverride("hyper/"); ok {
		t.Fatal("override with an empty model must be ignored")
	}
	if _, _, ok := ParseCompactModelOverride("/deepseek-v4-flash"); ok {
		t.Fatal("override with an empty provider must be ignored")
	}
	provider, modelID, ok := ParseCompactModelOverride("hyper/deepseek-v4-flash")
	if !ok || provider != "hyper" || modelID != "deepseek-v4-flash" {
		t.Fatalf("parsed = %q/%q ok=%v", provider, modelID, ok)
	}
}

// TestIsOpenCodeModel ports the isOpenCodeModel host/provider check.
func TestIsOpenCodeModel(t *testing.T) {
	if !IsOpenCodeModel(&ai.Model{Provider: "opencode-go", BaseURL: "https://x.example/v1"}) {
		t.Fatal("opencode-go provider must match")
	}
	if !IsOpenCodeModel(&ai.Model{Provider: "other", BaseURL: "https://opencode.ai/zen/go/v1"}) {
		t.Fatal("opencode.ai host must match")
	}
	if IsOpenCodeModel(&ai.Model{Provider: "commandcode", BaseURL: "https://api.commandcode.ai/v1"}) {
		t.Fatal("unrelated providers must not match")
	}
	if IsOpenCodeModel(nil) {
		t.Fatal("nil model must not match")
	}
}

// compactCaptureStream records the stream options of every summarizer call.
type compactCaptureStream struct {
	calls    []capturedCompactCall
	respones []string // summary text per call, in order
}

type capturedCompactCall struct {
	maxTokens int
	reasoning ai.ThinkingLevel
	headers   ai.ProviderHeaders
	sessionID string
	prompt    string
}

func (c *compactCaptureStream) streamFn(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	captured := capturedCompactCall{
		reasoning: options.Reasoning, headers: options.Headers, sessionID: options.SessionID,
	}
	if options.MaxTokens != nil {
		captured.maxTokens = *options.MaxTokens
	}
	for _, message := range context.Messages {
		if user, ok := message.(*ai.UserMessage); ok {
			captured.prompt = user.Content.Text
			if captured.prompt == "" && len(user.Content.Blocks) > 0 {
				if tc, ok := user.Content.Blocks[0].(ai.TextContent); ok {
					captured.prompt = tc.Text
				}
			}
		}
	}
	text := "none"
	if len(c.respones) > len(c.calls) {
		text = c.respones[len(c.calls)]
	}
	c.calls = append(c.calls, captured)
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		message := &ai.AssistantMessage{
			API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
			Content:    ai.ContentList{ai.TextContent{Text: text}},
			StopReason: ai.StopStop,
		}
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
	}()
	return stream
}

func betterCompactPreparation() *CompactionPreparation {
	fileOps := CreateFileOps()
	fileOps.markRead("/tmp/a.ts")
	fileOps.markWritten("/tmp/b.ts")
	return &CompactionPreparation{
		FirstKeptEntryID:    "entry-1",
		MessagesToSummarize: []ai.Message{&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}, Timestamp: 1}},
		TokensBefore:        1000,
		FileOps:             fileOps,
	}
}

// TestCompactBetterPath covers the extension's summarizer call shape: 32k
// budget, reasoning off, session id, opencode routing headers, and the capped
// file lists appended.
func TestCompactBetterPath(t *testing.T) {
	capture := &compactCaptureStream{respones: []string{
		strings.Repeat("## Goal\n- ship it with a reasonably long summary body. ", 3),
	}}
	options := CompactionOptions{
		Model: &ai.Model{ID: "deepseek-v4.1-flash", Provider: "opencode-go", BaseURL: "https://opencode.ai/zen/go/v1", MaxTokens: 65536},
		Ctx:   context.Background(), StreamFn: capture.streamFn, SessionID: "sess-123",
	}
	result, err := Compact(betterCompactPreparation(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.calls) != 1 {
		t.Fatalf("calls = %d", len(capture.calls))
	}
	call := capture.calls[0]
	if call.maxTokens != 32768 {
		t.Fatalf("maxTokens = %d, want 32768", call.maxTokens)
	}
	if call.reasoning != "" {
		t.Fatalf("reasoning = %q, want off", call.reasoning)
	}
	if call.sessionID != "sess-123" {
		t.Fatalf("session id = %q", call.sessionID)
	}
	if call.headers == nil || call.headers["x-opencode-session"] == nil || *call.headers["x-opencode-session"] != "sess-123" ||
		call.headers["x-opencode-client"] == nil || *call.headers["x-opencode-client"] != "pi" {
		t.Fatalf("opencode headers = %v", call.headers)
	}
	if !strings.Contains(result.Summary, "## Goal") {
		t.Fatalf("summary = %q", result.Summary[:60])
	}
	if !strings.Contains(result.Summary, "<read-files>\n/tmp/a.ts\n</read-files>") ||
		!strings.Contains(result.Summary, "<modified-files>\n/tmp/b.ts\n</modified-files>") {
		t.Fatalf("file lists missing: %q", result.Summary)
	}
	var details CompactionDetails
	if err := json.Unmarshal(result.Details, &details); err != nil {
		t.Fatal(err)
	}
	if len(details.ReadFiles) != 1 || details.ReadFiles[0] != "/tmp/a.ts" {
		t.Fatalf("details read = %v", details.ReadFiles)
	}
	if result.Usage == nil {
		t.Fatal("usage missing")
	}
}

// TestCompactFallsBackOnUnusableSummary ports the extension's fallback: an
// unusable better-compact summary hands over to the stock summarizer.
func TestCompactFallsBackOnUnusableSummary(t *testing.T) {
	capture := &compactCaptureStream{respones: []string{
		"too short",
		strings.Repeat("stock summary text that is long enough to be usable. ", 5),
	}}
	fallbacks := []string{}
	options := CompactionOptions{
		Model: &ai.Model{ID: "m", Provider: "p", MaxTokens: 65536},
		Ctx:   context.Background(), StreamFn: capture.streamFn, SessionID: "sess-1",
		OnFallback: func(message string) { fallbacks = append(fallbacks, message) },
	}
	result, err := Compact(betterCompactPreparation(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.calls) != 2 {
		t.Fatalf("calls = %d, want the stock retry", len(capture.calls))
	}
	if len(fallbacks) != 1 || !strings.Contains(fallbacks[0], "better-compact") {
		t.Fatalf("fallbacks = %v", fallbacks)
	}
	if !strings.Contains(result.Summary, "stock summary text") {
		t.Fatalf("summary = %q", result.Summary[:80])
	}
}

// TestCompactFallsBackOnSummarizerError covers the error fallback.
func TestCompactFallsBackOnSummarizerError(t *testing.T) {
	var calls atomic.Int64
	streamFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		call := calls.Add(1)
		go func() {
			if call == 1 {
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: &ai.AssistantMessage{StopReason: ai.StopError, ErrorMessage: strPtr("boom")}})
				return
			}
			stream.Push(ai.AssistantMessageEvent{
				Type: ai.EventDone, Reason: ai.StopStop,
				Message: &ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: strings.Repeat("stock fallback summary. ", 10)}}, StopReason: ai.StopStop},
			})
		}()
		return stream
	}
	options := CompactionOptions{
		Model: &ai.Model{ID: "m", Provider: "p", MaxTokens: 65536},
		Ctx:   context.Background(), StreamFn: streamFn,
	}
	result, err := Compact(betterCompactPreparation(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "stock fallback summary") {
		t.Fatalf("summary = %q", result.Summary[:60])
	}
}

// TestCompactUsesUpdatePromptOnPreviousSummary verifies the better path picks
// the update prompt when a previous summary exists and strips its lists.
func TestCompactUsesUpdatePromptOnPreviousSummary(t *testing.T) {
	capture := &compactCaptureStream{respones: []string{
		strings.Repeat("updated structured summary content. ", 5),
	}}
	options := CompactionOptions{
		Model: &ai.Model{ID: "m", Provider: "p", MaxTokens: 65536},
		Ctx:   context.Background(), StreamFn: capture.streamFn,
	}
	preparation := betterCompactPreparation()
	preparation.PreviousSummary = "## Goal\n- earlier work\n\n<read-files>\n/tmp/old.ts\n</read-files>"
	result, err := Compact(preparation, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.calls) != 1 {
		t.Fatalf("calls = %d", len(capture.calls))
	}
	if !strings.Contains(capture.calls[0].prompt, BetterUpdateSummarizationPrompt) {
		t.Fatal("update prompt not used")
	}
	if strings.Contains(capture.calls[0].prompt, "<read-files>") {
		t.Fatal("stale file lists fed back")
	}
	if !strings.Contains(result.Summary, "updated structured summary content") {
		t.Fatalf("summary = %q", result.Summary[:80])
	}
}

// TestCompactNothingToSummarizeFallsBack covers the no-op guard: neither
// history nor prefix messages means the stock path decides.
func TestCompactNothingToSummarizeFallsBack(t *testing.T) {
	capture := &compactCaptureStream{respones: []string{
		strings.Repeat("stock summary for the empty case. ", 5),
	}}
	options := CompactionOptions{
		Model: &ai.Model{ID: "m", Provider: "p", MaxTokens: 65536},
		Ctx:   context.Background(), StreamFn: capture.streamFn,
	}
	preparation := betterCompactPreparation()
	preparation.MessagesToSummarize = nil
	result, err := Compact(preparation, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.calls) != 1 {
		t.Fatalf("calls = %d", len(capture.calls))
	}
	if !strings.Contains(result.Summary, "stock summary") {
		t.Fatalf("summary = %q", result.Summary[:60])
	}
}
