package coding

import (
	ctxpkg "context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dat267/gpi/ai"
)

// Round 94 unit tests: event bus, usage totals, cache stats, session cwd,
// bash executor and the ANSI stripper.

func TestEventBusDispatchAndErrors(t *testing.T) {
	bus := CreateEventBus()
	var received []string
	off := bus.On("channel", func(data any) { received = append(received, data.(string)) })
	bus.On("other", func(data any) { t.Fatal("wrong channel") })
	bus.Emit("channel", "first")
	bus.Emit("channel", "second")
	off()
	bus.Emit("channel", "ignored")
	if strings.Join(received, ",") != "first,second" {
		t.Fatalf("received = %v", received)
	}

	// A panicking handler must not break emit or reach the caller.
	var errors []string
	bus.SetHandlerErrorReporter(func(channel string, err error) { errors = append(errors, channel+": "+err.Error()) })
	after := false
	bus.On("boom", func(data any) { panic("handler exploded") })
	bus.On("boom", func(data any) { after = true })
	bus.Emit("boom", nil)
	if !after || len(errors) != 1 || !strings.Contains(errors[0], "boom: handler exploded") {
		t.Fatalf("after = %v errors = %v", after, errors)
	}

	// Clear drops everything.
	cleared := false
	bus.On("channel", func(data any) { cleared = true })
	bus.Clear()
	bus.Emit("channel", nil)
	if cleared {
		t.Fatal("handler survived Clear")
	}
}

func TestEventBusConcurrent(t *testing.T) {
	bus := CreateEventBus()
	var count atomic.Int64
	for index := 0; index < 8; index++ {
		bus.On("load", func(data any) { count.Add(1) })
	}
	var waitGroup sync.WaitGroup
	for index := 0; index < 4; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for event := 0; event < 25; event++ {
				bus.Emit("load", nil)
			}
		}()
	}
	waitGroup.Wait()
	if count.Load() != 8*100 {
		t.Fatalf("count = %d", count.Load())
	}
}

func TestUsageTotalsBreakdown(t *testing.T) {
	assistant := func(provider, model string, input, output int64, cost float64) SessionEntry {
		message := ai.AssistantMessage{
			Provider: provider, Model: model,
			Usage: ai.Usage{Input: input, Output: output, TotalTokens: input + output, Cost: ai.UsageCost{Total: cost}},
		}
		encoded, _ := ai.MarshalMessage(&message)
		return SessionEntry{Type: "message", Message: encoded}
	}
	entries := []SessionEntry{
		assistant("anthropic", "sonnet", 100, 50, 0.01),
		assistant("anthropic", "sonnet", 200, 100, 0.02),
		assistant("openai", "gpt", 10, 5, 0.05),
		{Type: "branch_summary", Usage: &ai.Usage{Input: 1, Output: 2, Cost: ai.UsageCost{Total: 0.001}}},
		{Type: "compaction", Usage: &ai.Usage{Input: 3, Output: 4, Cost: ai.UsageCost{Total: 0.002}}},
		assistant("anthropic", "zero", 0, 0, 0),
	}
	breakdown := GetUsageCostBreakdown(entries)
	if len(breakdown) != 3 {
		t.Fatalf("breakdown = %+v", breakdown)
	}
	// Sorted by descending cost.
	if breakdown[0].Key != "openai/gpt" || breakdown[0].Cost != 0.05 || breakdown[0].Tokens != 15 {
		t.Fatalf("first = %+v", breakdown[0])
	}
	if breakdown[1].Key != "anthropic/sonnet" || breakdown[1].Tokens != 450 {
		t.Fatalf("second = %+v", breakdown[1])
	}
	if breakdown[2].Key != "Tools/summaries" || breakdown[2].Tokens != 10 {
		t.Fatalf("third = %+v", breakdown[2])
	}
}

type fixedPrices map[string]float64

func (p fixedPrices) GetCachePrice(provider, modelID string) *ModelCachePrice {
	price, ok := p[provider+"/"+modelID]
	if !ok {
		return nil
	}
	return &ModelCachePrice{CacheRead: price}
}

func cacheEntry(t *testing.T, timestamp int64, input, cacheRead, cacheWrite int64, cost ai.UsageCost) SessionEntry {
	t.Helper()
	message := ai.AssistantMessage{
		Provider: "anthropic", Model: "sonnet", Timestamp: timestamp,
		Usage: ai.Usage{
			Input: input, CacheRead: cacheRead, CacheWrite: cacheWrite,
			TotalTokens: input + cacheRead + cacheWrite, Cost: cost,
		},
	}
	encoded, err := ai.MarshalMessage(&message)
	if err != nil {
		t.Fatal(err)
	}
	return SessionEntry{Type: "message", Message: encoded}
}

func TestCacheWaste(t *testing.T) {
	// A full hit costs nothing; the next turn re-bills 10k tokens at 2x.
	entries := []SessionEntry{
		cacheEntry(t, 1000, 0, 10000, 0, ai.UsageCost{CacheRead: 0.001}),
		cacheEntry(t, 2000, 10000, 0, 0, ai.UsageCost{Input: 0.01}),
	}
	totals := ComputeCacheWaste(entries, fixedPrices{"anthropic/sonnet": 0.1})
	if totals.MissCount != 1 || totals.MissedTokens != 10000 {
		t.Fatalf("totals = %+v", totals)
	}
	// paid rate 0.01/10000 = 1e-6; read rate falls back to the price source
	// (0.1 per million = 1e-7 per token).
	want := 10000 * (1e-6 - 1e-7)
	if diff := totals.MissedCost - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v want %v", totals.MissedCost, want)
	}
	misses := CollectCacheMisses(entries, fixedPrices{"anthropic/sonnet": 0.1})
	if len(misses) != 1 || misses[0].EntryIndex != 1 || misses[0].Miss.MissedTokens != 10000 {
		t.Fatalf("misses = %+v", misses)
	}
	if misses[0].Miss.IdleMs != 1000 || misses[0].Miss.ModelChanged {
		t.Fatalf("miss = %+v", misses[0].Miss)
	}
}

func TestCacheWasteRules(t *testing.T) {
	// The first turn never counts, and a miss under the noise floor is ignored.
	small := []SessionEntry{
		cacheEntry(t, 1000, 0, 5000, 0, ai.UsageCost{CacheRead: 0.0005}),
		cacheEntry(t, 2000, 500, 0, 0, ai.UsageCost{Input: 0.0005}),
	}
	if totals := ComputeCacheWaste(small, nil); totals.MissCount != 0 {
		t.Fatalf("noise floor: %+v", totals)
	}

	// A provider that never reports cache activity never counts.
	noCache := []SessionEntry{
		cacheEntry(t, 1000, 5000, 0, 0, ai.UsageCost{Input: 0.005}),
		cacheEntry(t, 2000, 6000, 0, 0, ai.UsageCost{Input: 0.006}),
	}
	if totals := ComputeCacheWaste(noCache, nil); totals.MissCount != 0 {
		t.Fatalf("no cache: %+v", totals)
	}

	// After a compaction the context legitimately changed, so the next turn
	// does not count. A model switch does count.
	afterCompaction := []SessionEntry{
		cacheEntry(t, 1000, 0, 10000, 0, ai.UsageCost{CacheRead: 0.001}),
		{Type: "compaction"},
		cacheEntry(t, 2000, 10000, 0, 0, ai.UsageCost{Input: 0.01}),
	}
	if totals := ComputeCacheWaste(afterCompaction, nil); totals.MissCount != 0 {
		t.Fatalf("after compaction: %+v", totals)
	}

	switchModel := []SessionEntry{
		cacheEntry(t, 1000, 0, 10000, 0, ai.UsageCost{CacheRead: 0.001}),
		func() SessionEntry {
			entry := cacheEntry(t, 2000, 10000, 0, 0, ai.UsageCost{Input: 0.01})
			entry.Message = []byte(strings.Replace(string(entry.Message), `"sonnet"`, `"opus"`, 1))
			return entry
		}(),
	}
	totals := ComputeCacheWaste(switchModel, nil)
	if totals.MissCount != 1 {
		t.Fatalf("model switch: %+v", totals)
	}
	misses := CollectCacheMisses(switchModel, nil)
	if len(misses) != 1 || !misses[0].Miss.ModelChanged {
		t.Fatalf("misses = %+v", misses)
	}

	// DetectCacheMiss looks at the trailing request without the new message.
	entries := []SessionEntry{cacheEntry(t, 1000, 0, 10000, 0, ai.UsageCost{CacheRead: 0.001})}
	next := &ai.AssistantMessage{
		Provider: "anthropic", Model: "sonnet", Timestamp: 3000,
		Usage: ai.Usage{Input: 10000, TotalTokens: 10000, Cost: ai.UsageCost{Input: 0.01}},
	}
	miss, ok := DetectCacheMiss(entries, next, nil)
	if !ok || miss.MissedTokens != 10000 || miss.IdleMs != 2000 {
		t.Fatalf("miss = %+v ok = %v", miss, ok)
	}
	if _, ok := DetectCacheMiss(nil, next, nil); ok {
		t.Fatal("first turn must not count")
	}
}

type stubSessionCwd struct {
	cwd         string
	sessionFile string
}

func (s stubSessionCwd) GetCwd() string         { return s.cwd }
func (s stubSessionCwd) GetSessionFile() string { return s.sessionFile }

func TestMissingSessionCwd(t *testing.T) {
	dir := t.TempDir()
	// A session without a file has no issue.
	if issue := GetMissingSessionCwdIssue(stubSessionCwd{cwd: "/nope"}, ""); issue != nil {
		t.Fatalf("issue = %+v", issue)
	}
	// An existing cwd has no issue.
	if issue := GetMissingSessionCwdIssue(stubSessionCwd{cwd: dir, sessionFile: "/s.json"}, dir); issue != nil {
		t.Fatalf("issue = %+v", issue)
	}
	// An empty cwd is treated as fine.
	if issue := GetMissingSessionCwdIssue(stubSessionCwd{cwd: "", sessionFile: "/s.json"}, dir); issue != nil {
		t.Fatalf("issue = %+v", issue)
	}

	missing := filepath.Join(dir, "gone")
	issue := GetMissingSessionCwdIssue(stubSessionCwd{cwd: missing, sessionFile: "/s.json"}, dir)
	if issue == nil || issue.SessionCwd != missing {
		t.Fatalf("issue = %+v", issue)
	}
	text := FormatMissingSessionCwdError(*issue)
	if !strings.Contains(text, "Stored session working directory does not exist: "+missing) ||
		!strings.Contains(text, "Session file: /s.json") ||
		!strings.Contains(text, "Current working directory: "+dir) {
		t.Fatalf("error = %q", text)
	}
	if prompt := FormatMissingSessionCwdPrompt(*issue); !strings.Contains(prompt, "continue in current cwd\n"+dir) {
		t.Fatalf("prompt = %q", prompt)
	}

	err := AssertSessionCwdExists(stubSessionCwd{cwd: missing, sessionFile: "/s.json"}, dir)
	var missingErr *MissingSessionCwdError
	if !errors.As(err, &missingErr) || missingErr.Issue.SessionCwd != missing {
		t.Fatalf("err = %v", err)
	}
	if err := AssertSessionCwdExists(stubSessionCwd{cwd: dir, sessionFile: "/s.json"}, dir); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestStripAnsi(t *testing.T) {
	cases := map[string]string{
		"plain":                          "plain",
		"\x1b[31mred\x1b[0m":             "red",
		"\x1b[1;32mbold green\x1b[m":     "bold green",
		"a\x1b]0;title\x07b":             "ab",
		"a\x1b]8;;https://x\x1b\\b":      "ab",
		"no escape \x9b":                 "no escape \x9b",
		"cursor\x1b[2Kgone":              "cursorgone",
		"\x1b[38:5:196mtruecolor\x1b[0m": "truecolor",
	}
	for input, want := range cases {
		if got := StripAnsi(input); got != want {
			t.Errorf("StripAnsi(%q) = %q, want %q", input, got, want)
		}
	}
}

// scriptedOps is a fake BashOperations backend.
type scriptedOps struct {
	chunks   []string
	exitCode *int
	err      error
	seen     BashExecOptions
	command  string
	cwd      string
	onRun    func(options BashExecOptions)
}

func (o *scriptedOps) Exec(ctx ctxpkg.Context, command, cwd string, options BashExecOptions) (*int, error) {
	o.command = command
	o.cwd = cwd
	o.seen = options
	if o.onRun != nil {
		o.onRun(options)
	}
	for _, chunk := range o.chunks {
		if options.OnData != nil {
			options.OnData([]byte(chunk))
		}
	}
	return o.exitCode, o.err
}

func TestExecuteBashWithOperations(t *testing.T) {
	code := 0
	ops := &scriptedOps{chunks: []string{"\x1b[31mhello\x1b[0m", " world\r\n"}, exitCode: &code}
	var streamed []string
	result, err := ExecuteBashWithOperations(ctxpkg.Background(), "echo hi", "/tmp", ops, &BashExecutorOptions{
		OnChunk: func(chunk string) { streamed = append(streamed, chunk) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// ANSI is stripped and \r removed from the graded output.
	if result.Output != "hello world\n" {
		t.Fatalf("output = %q", result.Output)
	}
	if len(streamed) != 2 || streamed[0] != "hello" {
		t.Fatalf("streamed = %q", streamed)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Truncated || result.Cancelled {
		t.Fatalf("result = %+v", result)
	}
	if ops.command != "echo hi" || ops.cwd != "/tmp" {
		t.Fatalf("ops = %+v", ops)
	}

	// A nil exit code from the backend is preserved.
	ops = &scriptedOps{chunks: []string{"partial"}, exitCode: nil}
	result, err = ExecuteBashWithOperations(ctxpkg.Background(), "cmd", "/tmp", ops, nil)
	if err != nil || result.ExitCode != nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}

	// A backend error propagates.
	boom := errors.New("spawn failed")
	ops = &scriptedOps{err: boom}
	if _, err := ExecuteBashWithOperations(ctxpkg.Background(), "cmd", "/tmp", ops, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	// A closed signal channel cancels the result.
	signal := make(chan struct{})
	close(signal)
	ops = &scriptedOps{chunks: []string{"before cancel"}, exitCode: &code}
	result, err = ExecuteBashWithOperations(ctxpkg.Background(), "cmd", "/tmp", ops, &BashExecutorOptions{Signal: signal})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Cancelled || result.ExitCode != nil {
		t.Fatalf("result = %+v", result)
	}
	// A cancelled backend error is not an error.
	ops = &scriptedOps{err: errors.New("aborted"), chunks: []string{"x"}}
	result, err = ExecuteBashWithOperations(ctxpkg.Background(), "cmd", "/tmp", ops, &BashExecutorOptions{Signal: signal})
	if err != nil || !result.Cancelled {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if _, err := ExecuteBashWithOperations(ctxpkg.Background(), "cmd", "/tmp", nil, nil); err == nil {
		t.Fatal("missing operations must error")
	}
}

func TestExecuteBashTruncationAndTempFile(t *testing.T) {
	code := 0
	// One oversized line exceeding the 50KB limit forces tail truncation and a
	// temp file with the full output.
	big := strings.Repeat("x", DefaultMaxBytes+5000)
	ops := &scriptedOps{chunks: []string{big}, exitCode: &code}
	result, err := ExecuteBashWithOperations(ctxpkg.Background(), "cmd", "/tmp", ops, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.FullOutputPath == "" {
		t.Fatalf("result = %+v", result)
	}
	defer os.Remove(result.FullOutputPath)
	written, err := os.ReadFile(result.FullOutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != big {
		t.Fatalf("temp file length = %d want %d", len(written), len(big))
	}
	if len(result.Output) >= len(big) {
		t.Fatalf("output not truncated: %d", len(result.Output))
	}
}

func TestExecuteBashLocal(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no shell available")
	}
	result, err := ExecuteBash(ctxpkg.Background(), "printf 'out'; printf 'err' >&2", t.TempDir(), &BashExecutorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.Output != "outerr" && result.Output != "errout" {
		t.Fatalf("output = %q", result.Output)
	}

	// A failing command reports its code.
	result, err = ExecuteBash(ctxpkg.Background(), "exit 3", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil || *result.ExitCode != 3 {
		t.Fatalf("result = %+v", result)
	}

	// A missing working directory fails like upstream.
	if _, err := ExecuteBash(ctxpkg.Background(), "true", filepath.Join(t.TempDir(), "gone"), nil); err == nil ||
		!strings.Contains(err.Error(), "Working directory does not exist:") {
		t.Fatalf("err = %v", err)
	}

	// Cancellation kills the command mid-flight.
	stop := make(chan struct{})
	cancelTimer := time.AfterFunc(200*time.Millisecond, func() { close(stop) })
	start := time.Now()
	result, err = ExecuteBash(ctxpkg.Background(), "sleep 10", t.TempDir(), &BashExecutorOptions{Signal: stop})
	cancelTimer.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if !result.Cancelled || result.ExitCode != nil {
		t.Fatalf("result = %+v", result)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func TestLocalShellOperationsTimeout(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no shell available")
	}
	// A cancelled context produces the aborted error.
	ctx, cancel := ctxpkg.WithCancel(ctxpkg.Background())
	cancel()
	ops := CreateLocalBashOperations("", nil)
	timeout := 50 * time.Millisecond
	if _, err := ops.Exec(ctx, "true", t.TempDir(), BashExecOptions{Timeout: &timeout}); err == nil {
		t.Fatal("cancelled context must error")
	}

	// A timeout reports the upstream timeout:<ms> error.
	start := time.Now()
	_, err := ops.Exec(ctxpkg.Background(), "sleep 5", t.TempDir(), BashExecOptions{Timeout: &timeout})
	if err == nil || !strings.HasPrefix(err.Error(), "timeout:50") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestTempFileID(t *testing.T) {
	seen := map[string]bool{}
	for index := 0; index < 64; index++ {
		id := tempFileID()
		if len(id) != 16 {
			t.Fatalf("id = %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
