package coding

import (
	ctxpkg "context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dat267/gpi/ai"
)

// Round 113 tests: the cache warmer (delay/TTL/replayability, the warm-or-stop
// economics, the safety windows, and the persisted usage).

func warmerModel() *ai.Model {
	return &ai.Model{
		ID: "m", Name: "M", API: ai.APIAnthropicMessages, Provider: "anthropic",
		Reasoning: false, ContextWindow: 100000, MaxTokens: 8192,
		Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
		// A short TTL keeps the warm delay at one second for tests.
		PromptCache: ai.ModelPromptCache{ai.CacheRetentionShort: 11, ai.CacheRetentionLong: 3600},
	}
}

func TestGetCacheWarmingDelayMS(t *testing.T) {
	if delay, ok := GetCacheWarmingDelayMS(300_000); !ok || delay != 270_000 {
		t.Fatalf("delay = %d ok = %v", delay, ok)
	}
	if delay, ok := GetCacheWarmingDelayMS(11_000); !ok || delay != 1_000 {
		t.Fatalf("delay = %d ok = %v", delay, ok)
	}
	// Tiny TTLs cannot preserve the ten-second margin.
	if _, ok := GetCacheWarmingDelayMS(10_000); ok {
		t.Fatal("10s TTL must not be warmable")
	}
	if _, ok := GetCacheWarmingDelayMS(5_000); ok {
		t.Fatal("5s TTL must not be warmable")
	}
	// A TTL just above the margin keeps the capped delay.
	if delay, ok := GetCacheWarmingDelayMS(10_500); !ok || delay != 500 {
		t.Fatalf("delay = %d ok = %v", delay, ok)
	}
}

func TestGetPromptCacheTTLMs(t *testing.T) {
	model := warmerModel()
	// The retention comes from the request options, defaulting to short.
	if ttl, ok := GetPromptCacheTTLMs(model, nil); !ok || ttl != 11_000 {
		t.Fatalf("ttl = %d ok = %v", ttl, ok)
	}
	long := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{CacheRetention: ai.CacheRetentionLong}}
	if ttl, ok := GetPromptCacheTTLMs(model, long); !ok || ttl != 3_600_000 {
		t.Fatalf("ttl = %d ok = %v", ttl, ok)
	}
	// The PI_CACHE_RETENTION env selects the tier.
	t.Setenv("PI_CACHE_RETENTION", "long")
	if ttl, ok := GetPromptCacheTTLMs(model, nil); !ok || ttl != 3_600_000 {
		t.Fatalf("ttl = %d ok = %v", ttl, ok)
	}
	// Caching off disables warming.
	none := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{CacheRetention: ai.CacheRetentionNone}}
	if _, ok := GetPromptCacheTTLMs(model, none); ok {
		t.Fatal("retention none must disable warming")
	}
	// A model without cache lifetimes cannot be warmed.
	bare := &ai.Model{ID: "b", API: ai.APIAnthropicMessages, Provider: "anthropic"}
	if _, ok := GetPromptCacheTTLMs(bare, nil); ok {
		t.Fatal("missing lifetimes must disable warming")
	}
}

func TestIsCacheWarmingReplayable(t *testing.T) {
	// Non-Anthropic models and non-reasoning requests replay fine.
	openai := &ai.Model{ID: "o", API: ai.APIOpenAICompletions, Provider: "openai"}
	if !IsCacheWarmingReplayable(openai, &ai.SimpleStreamOptions{Reasoning: ai.ThinkHigh}) {
		t.Fatal("non-anthropic must be replayable")
	}
	model := warmerModel()
	if !IsCacheWarmingReplayable(model, nil) {
		t.Fatal("no options must be replayable")
	}
	// Anthropic reasoning with budget-based thinking cannot replay.
	reasoning := &ai.SimpleStreamOptions{Reasoning: ai.ThinkHigh}
	if IsCacheWarmingReplayable(model, reasoning) {
		t.Fatal("budget-based thinking must not replay")
	}
	// Adaptive thinking is forced: the replay is safe.
	adaptive := warmerModel()
	adaptive.Compat = &ai.ModelCompat{AnthropicMessages: &ai.AnthropicMessagesCompat{ForceAdaptiveThinking: boolPtr(true)}}
	if !IsCacheWarmingReplayable(adaptive, reasoning) {
		t.Fatal("adaptive thinking must replay")
	}
}

func newTestWarmer(t *testing.T, model *ai.Model, mode CacheWarmingMode) (*CacheWarmer, *SessionManager, *atomic.Int64) {
	t.Helper()
	manager := NewSessionManager(t.TempDir(), nil)
	var runs atomic.Int64
	streamFn := func(streamModel *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		runs.Add(1)
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: streamModel.Provider, Model: streamModel.ID,
				Content:       ai.ContentList{ai.TextContent{Text: "warm"}},
				StopReason:    ai.StopStop,
				Usage:         ai.Usage{Input: 1000, CacheRead: 900, Output: 1, TotalTokens: 1001, Cost: ai.UsageCost{Total: 0.0003}},
				ResponseModel: strPtr(streamModel.ID),
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}
	modeHolder := atomic.Value{}
	modeHolder.Store(mode)
	warmer := NewCacheWarmer(streamFn, manager, func() CacheWarmingMode {
		return modeHolder.Load().(CacheWarmingMode)
	})
	warmer.modeHolder = &modeHolder
	return warmer, manager, &runs
}

func TestCacheWarmerStartStopReasons(t *testing.T) {
	model := warmerModel()
	warmer, _, runs := newTestWarmer(t, model, CacheWarmingIdle)

	// Waiting for the first request.
	if status := warmer.Status(); status.State != "inactive" || status.Reason != "waiting for first request" {
		t.Fatalf("status = %+v", status)
	}

	// Mode off stops immediately.
	warmer.modeHolder.Store(CacheWarmingOff)
	warmer.Start(CacheWarmRequest{Model: model}, func() bool { return true })
	if status := warmer.Status(); status.State != "inactive" || status.Reason != "cache warming disabled" {
		t.Fatalf("status = %+v", status)
	}
	warmer.modeHolder.Store(CacheWarmingIdle)

	// A model without cache lifetimes cannot be warmed.
	bare := &ai.Model{ID: "b", API: ai.APIAnthropicMessages, Provider: "anthropic"}
	warmer.Start(CacheWarmRequest{Model: bare}, func() bool { return true })
	if status := warmer.Status(); status.Reason != "cache lifetime unavailable" {
		t.Fatalf("status = %+v", status)
	}

	// Retention none is reported distinctly.
	none := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{CacheRetention: ai.CacheRetentionNone}}
	warmer.Start(CacheWarmRequest{Model: model, Options: none}, func() bool { return true })
	if status := warmer.Status(); status.Reason != "request disabled prompt caching" {
		t.Fatalf("status = %+v", status)
	}

	// Budget-based Anthropic thinking cannot replay.
	reasoning := &ai.SimpleStreamOptions{Reasoning: ai.ThinkHigh}
	warmer.Start(CacheWarmRequest{Model: model, Options: reasoning}, func() bool { return true })
	if status := warmer.Status(); status.Reason != "request cannot be replayed safely" {
		t.Fatalf("status = %+v", status)
	}
	if runs.Load() != 0 {
		t.Fatalf("runs = %d", runs.Load())
	}
	// A scheduled run with an empty session has no economics to decide on.
	warmer.Start(CacheWarmRequest{Model: model}, func() bool { return true })
	if status := warmer.Status(); status.State != "inactive" || status.Reason != "cache economics unavailable" {
		t.Fatalf("status = %+v", status)
	}
}

func TestCacheWarmerEconomicsAndWarm(t *testing.T) {
	model := warmerModel()
	warmer, manager, runs := newTestWarmer(t, model, CacheWarmingIdle)

	// Seed usage so the economics are available: 1000 prompt tokens.
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content:    ai.ContentList{ai.TextContent{Text: "reply"}},
		StopReason: ai.StopStop,
		Usage:      ai.Usage{Input: 1000, CacheRead: 0, TotalTokens: 1000},
	})

	warmed := make(chan *SessionEntry, 1)
	warmer.OnWarmed = func(entry *SessionEntry) { warmed <- entry }
	// The decision override is installed before Start so the timer goroutine
	// observes it without a race.
	warmer.Decide = func(event CacheWarmingDecisionEvent) CacheWarmingAction { return CacheWarmingActionWarm }
	warmer.Start(CacheWarmRequest{Model: model, Options: &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{MaxTokens: intPtr(100)}}}, func() bool { return true })

	// Right after Start the run is in the streaming phase (probability 1);
	// settling the agent moves it to idle without stopping (mode idle).
	warmer.OnAgentSettled()

	status := warmer.Status()
	if status.State != "scheduled" {
		t.Fatalf("status = %+v", status)
	}
	if status.Decision == nil || !status.Decision.EconomicsAvailable {
		t.Fatalf("decision = %+v", status.Decision)
	}
	// Warm cost: 1000 cache reads at $0.3/M ($0.0003) plus one output token at
	// $15/M ($0.000015).
	if status.Decision.WarmCost < 0.00031 || status.Decision.WarmCost > 0.00032 {
		t.Fatalf("warmCost = %v", status.Decision.WarmCost)
	}
	// Miss cost: 1000 tokens at the cache-write rate minus the cache-read rate.
	if status.Decision.MissCost < 0.0034 || status.Decision.MissCost > 0.0035 {
		t.Fatalf("missCost = %v", status.Decision.MissCost)
	}
	if status.Decision.ContinuationProbability != idleCacheWarmingContinuationProbability {
		t.Fatalf("probability = %v", status.Decision.ContinuationProbability)
	}
	// 0.15 * 0.00345 - 0.0003 is below $0.05, so pi stops warming.
	if status.Decision.Action != CacheWarmingActionStop {
		t.Fatalf("action = %q", status.Decision.Action)
	}

	// The forced "warm" decision runs the refresh and persists usage.
	select {
	case entry := <-warmed:
		if entry.Type != "usage" || entry.Kind != "cache_warm" || entry.Provider != "anthropic" {
			t.Fatalf("entry = %+v", entry)
		}
		if entry.Usage.Input != 1000 || entry.Usage.Output != 1 {
			t.Fatalf("usage = %+v", entry.Usage)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("warm request did not complete")
	}
	if runs.Load() != 1 {
		t.Fatalf("runs = %d", runs.Load())
	}

	// The usage text renders the trimmed cost.
	entries := manager.GetEntries()
	text := FormatCacheWarmingUsage(&entries[len(entries)-1])
	// The forced decision is an extension override, which the usage note carries.
	if !strings.HasPrefix(text, "Cache warmed (extension override): $0.0003") {
		t.Fatalf("text = %q", text)
	}
}

func TestCacheWarmerStreamingModeStopsAtSettle(t *testing.T) {
	model := warmerModel()
	warmer, manager, runs := newTestWarmer(t, model, CacheWarmingStreaming)
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "reply"}}, StopReason: ai.StopStop,
		Usage: ai.Usage{Input: 1000, TotalTokens: 1000},
	})
	warmer.Start(CacheWarmRequest{Model: model}, func() bool { return true })
	if status := warmer.Status(); status.State != "scheduled" {
		t.Fatalf("status = %+v", status)
	}
	// In streaming mode the agent settling stops warming.
	warmer.OnAgentSettled()
	status := warmer.Status()
	if status.State != "inactive" || status.Reason != "agent run settled" {
		t.Fatalf("status = %+v", status)
	}
	if runs.Load() != 0 {
		t.Fatalf("runs = %d", runs.Load())
	}
}

func TestCacheWarmerStaleContext(t *testing.T) {
	model := warmerModel()
	warmer, manager, _ := newTestWarmer(t, model, CacheWarmingIdle)
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "reply"}}, StopReason: ai.StopStop,
		Usage: ai.Usage{Input: 1000, TotalTokens: 1000},
	})
	current := true
	warmer.Start(CacheWarmRequest{Model: model}, func() bool { return current })
	if status := warmer.Status(); status.State != "scheduled" {
		t.Fatalf("status = %+v", status)
	}
	// The conversation changed: the run is invalidated.
	current = false
	status := warmer.Status()
	if status.State != "inactive" || status.Reason != "conversation context changed" {
		t.Fatalf("status = %+v", status)
	}
}

func TestFormatCacheWarmingStatus(t *testing.T) {
	now := int64(1_000_000)
	// Inactive without a decision.
	if got := FormatCacheWarmingStatus(CacheWarmingStatus{State: "inactive", Reason: "waiting for first request"}, now); got != "Inactive (waiting for first request)" {
		t.Fatalf("got = %q", got)
	}
	// Scheduled with economics.
	decision := &CacheWarmingDecision{
		Phase: "idle", WarmCost: 0.0003, MissCost: 0.00345,
		ContinuationProbability: 0.15, ExpectedSavings: -0.00015,
		EconomicsAvailable: true, Action: CacheWarmingActionStop,
	}
	scheduled := CacheWarmingStatus{State: "scheduled", NextWarmAt: now + 90_000, Decision: decision}
	got := FormatCacheWarmingStatus(scheduled, now)
	if !strings.HasPrefix(got, "Decision in 1m 30s (") || !strings.Contains(got, "15% continuation probability") ||
		!strings.Contains(got, "expected savings -$0.000") || !strings.Contains(got, "< $0.050") {
		t.Fatalf("got = %q", got)
	}
	// Stopped and warming variants.
	if got := FormatCacheWarmingStatus(CacheWarmingStatus{State: "inactive", Decision: decision}, now); !strings.HasPrefix(got, "Stopped (") {
		t.Fatalf("got = %q", got)
	}
	if got := FormatCacheWarmingStatus(CacheWarmingStatus{State: "refreshing", Decision: decision}, now); !strings.HasPrefix(got, "Warming cache (") {
		t.Fatalf("got = %q", got)
	}
	// An extension override is called out.
	if got := FormatCacheWarmingStatus(CacheWarmingStatus{State: "inactive", Decision: decision, ExtensionOverride: true}, now); !strings.Contains(got, "extension override") {
		t.Fatalf("got = %q", got)
	}
	// Decision now for an immediate refresh.
	if got := FormatCacheWarmingStatus(CacheWarmingStatus{State: "scheduled", NextWarmAt: now, Decision: decision}, now); !strings.HasPrefix(got, "Decision now") {
		t.Fatalf("got = %q", got)
	}
}

func TestSessionCacheWarmingWiring(t *testing.T) {
	model := warmerModel()
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	session := newControlSession(t, model, settings)
	warmer, manager, runs := newTestWarmer(t, model, session.control.Settings.GetCacheWarmingMode())
	session.CacheWarmer = warmer
	settings.SetCacheWarmingMode(CacheWarmingIdle)
	// The test warmer reads its mode from the holder; keep it in sync with the
	// settings the session writes.
	defer warmer.modeHolder.Store(CacheWarmingOff)
	warmer.modeHolder.Store(CacheWarmingIdle)
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "reply"}}, StopReason: ai.StopStop,
		Usage: ai.Usage{Input: 1000, TotalTokens: 1000},
	})

	// The status accessor surfaces the warmer state.
	if status := session.GetCacheWarmingStatus(); status == nil {
		t.Fatal("status missing")
	} else if status.State != "inactive" {
		t.Fatalf("status = %+v", status)
	}

	// Prompting with the session id restarts warming.
	if err := session.Prompt(ctxpkg.Background(), "hello", &PromptOptions{SessionID: session.SessionID()}); err != nil {
		t.Fatal(err)
	}
	if status := session.GetCacheWarmingStatus(); status == nil || (status.State != "scheduled" && status.State != "refreshing" && status.State != "inactive") {
		t.Fatalf("status = %+v", status)
	}

	// Settling the run stops warming in streaming mode; here the mode is idle so
	// the run stays scheduled until the context changes.
	session.CacheWarmer.OnAgentSettled()

	// The mode setter reconciles (the warmer reads the settings-backed mode,
	// which the settings write updates before the reconcile).
	warmer.modeHolder.Store(CacheWarmingOff)
	session.SetCacheWarmingMode(CacheWarmingOff)
	if status := session.GetCacheWarmingStatus(); status.State != "inactive" || status.Reason != "cache warming disabled" {
		t.Fatalf("status = %+v", status)
	}
	if session.control.Settings.GetCacheWarmingMode() != CacheWarmingOff {
		t.Fatal("mode must be persisted")
	}
	_ = runs
}
