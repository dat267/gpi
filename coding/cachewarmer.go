package coding

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of core/cache-warmer.ts: keeping one prompt cache entry alive by
// re-sending its request with a one-token output cap before the entry expires.

// Streaming warming never continues past this long after the real request that
// started it.
const maxCacheWarmingAgeMS int64 = 60 * 60_000

// Idle warming uses a shorter horizon because continuation estimates become
// less reliable with age.
const maxIdleCacheWarmingAgeMS int64 = 30 * 60_000

// A refresh is sent only when it is expected to save at least this many
// dollars.
const cacheWarmingMinimumExpectedSavings = 0.05

// Chance that a real request arrives before the cache entry expires while the
// agent sits idle. Measured from pi's own usage; per-session estimates were not
// better than this constant.
const idleCacheWarmingContinuationProbability = 0.15

// GetCacheWarmingDelayMS refreshes at 90% of the TTL while preserving at least
// ten seconds of margin.
func GetCacheWarmingDelayMS(ttlMS int64) (int64, bool) {
	if ttlMS <= 10_000 {
		return 0, false
	}
	delay := int64(1)
	ninety := int64(float64(ttlMS) * 0.9)
	margin := ttlMS - 10_000
	if ninety < margin {
		delay = ninety
	} else {
		delay = margin
	}
	return delay, true
}

// GetPromptCacheTTLMs returns the lifetime of the prompt cache entry a request
// writes, from the model's promptCache tier for the retention the request used.
// ok is false when the model has no lifetime for that tier or caching is off.
func GetPromptCacheTTLMs(model *ai.Model, options *ai.SimpleStreamOptions) (int64, bool) {
	retention := ai.CacheRetentionShort
	if options != nil && options.CacheRetention != "" {
		retention = options.CacheRetention
	} else if value := ai.GetProviderEnvValueOr("PI_CACHE_RETENTION", envOf(options)); value == "long" {
		retention = ai.CacheRetentionLong
	}
	if retention == ai.CacheRetentionNone {
		return 0, false
	}
	seconds, ok := model.PromptCache[retention]
	if !ok {
		return 0, false
	}
	return int64(seconds) * 1000, true
}

func envOf(options *ai.SimpleStreamOptions) ai.ProviderEnv {
	if options == nil {
		return nil
	}
	return options.Env
}

// IsCacheWarmingReplayable reports whether replaying the request with a
// one-token output cap leaves its cache entry untouched. Anthropic's
// budget-based thinking derives budget_tokens from max_tokens; the replay would
// get a different budget, which Anthropic keys the message cache on.
func IsCacheWarmingReplayable(model *ai.Model, options *ai.SimpleStreamOptions) bool {
	if options == nil || options.Reasoning == "" || options.Reasoning == ai.ThinkOff || model.API != ai.APIAnthropicMessages {
		return true
	}
	if model.Compat == nil || model.Compat.AnthropicMessages == nil {
		return false
	}
	return model.Compat.AnthropicMessages.ForceAdaptiveThinking != nil &&
		*model.Compat.AnthropicMessages.ForceAdaptiveThinking
}

// lastCacheWarmingPromptTokens is the prompt size of the most recent real
// request on the branch, as reported by the provider.
func lastCacheWarmingPromptTokens(entries []SessionEntry) int64 {
	for index := len(entries) - 1; index >= 0; index-- {
		entry := &entries[index]
		if entry.Type != "message" {
			continue
		}
		message, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			continue
		}
		if assistant, ok := message.(*ai.AssistantMessage); ok {
			return assistant.Usage.Input + assistant.Usage.CacheRead + assistant.Usage.CacheWrite
		}
	}
	return 0
}

// cacheWarmingPrice prices a partial usage against the model.
func cacheWarmingPrice(model *ai.Model, input, output, cacheRead, cacheWrite int64) float64 {
	usage := ai.Usage{
		Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite,
		TotalTokens: input + output + cacheRead + cacheWrite,
	}
	return ai.CalculateCost(model, &usage).Total
}

// CacheWarmingAction is the warmer's decision.
type CacheWarmingAction = string

const (
	CacheWarmingActionWarm CacheWarmingAction = "warm"
	CacheWarmingActionStop CacheWarmingAction = "stop"
)

// CacheWarmingDecision is one warm-or-stop decision, as shown by /session.
type CacheWarmingDecision struct {
	// Phase is "streaming" while the agent run that sent the request is active.
	Phase string
	// WarmCost is the price of this refresh: a cache read plus one output token.
	WarmCost float64
	// MissCost is the extra price of the next real request if the entry is lost.
	MissCost float64
	// ContinuationProbability estimates a real request arriving before expiry.
	ContinuationProbability float64
	// ExpectedSavings is continuationProbability * missCost - warmCost.
	ExpectedSavings float64
	// EconomicsAvailable is false when the prompt size or prices are unknown.
	EconomicsAvailable bool
	// Action is "warm" when expected savings reach the threshold.
	Action CacheWarmingAction
}

// CacheWarmingDecisionEvent is fired before each refresh with pi's decision.
type CacheWarmingDecisionEvent struct {
	Type                    string
	WarmCost                float64
	MissCost                float64
	ContinuationProbability float64
	Action                  CacheWarmingAction
}

// CacheWarmingStatus is the warmer's current state.
type CacheWarmingStatus struct {
	// State is "inactive", "scheduled", or "refreshing".
	State string
	// Reason explains why nothing is scheduled.
	Reason string
	// NextWarmAt is the scheduled refresh time (unix ms).
	NextWarmAt int64
	// Decision is the pending decision, or the one that stopped warming.
	Decision *CacheWarmingDecision
	// ExtensionOverride is true when an extension changed decision.action.
	ExtensionOverride bool
}

// CacheWarmRequest is the request whose prompt cache entry should be kept warm,
// exactly as it was sent.
type CacheWarmRequest struct {
	Model   *ai.Model
	Context ai.Context
	Options *ai.SimpleStreamOptions
}

// cacheWarmingRun is an active warming run.
type cacheWarmingRun struct {
	request           CacheWarmRequest
	isCurrent         func() bool
	delayMS           int64
	startedAt         int64
	ctx               context.Context
	cancel            context.CancelFunc
	phase             string
	nextWarmAt        int64
	extensionOverride bool
	timer             *time.Timer
}

// CacheWarmer keeps one prompt cache entry alive. Start replaces any previous
// run; warm requests never extend the fixed safety windows.
type CacheWarmer struct {
	mu  sync.Mutex
	run *cacheWarmingRun
	// inactive is the status reported while nothing is scheduled.
	inactive CacheWarmingStatus
	// StreamSimple issues the warm requests.
	StreamSimple func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream
	// SessionManager persists the warmed usage.
	SessionManager *SessionManager
	// GetMode returns the warming mode.
	GetMode func() CacheWarmingMode
	// Decide lets a host override the decision; failures fall back to pi's own.
	Decide func(event CacheWarmingDecisionEvent) CacheWarmingAction
	// OnWarmed receives the persisted usage entry after each successful refresh.
	OnWarmed func(entry *SessionEntry)

	// modeHolder is test wiring that lets tests change the mode a fixed closure
	// returns.
	modeHolder interface{ Store(any) }
}

// NewCacheWarmer builds a warmer.
func NewCacheWarmer(streamSimple func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream,
	sessionManager *SessionManager, getMode func() CacheWarmingMode) *CacheWarmer {
	warmer := &CacheWarmer{
		StreamSimple: streamSimple, SessionManager: sessionManager, GetMode: getMode,
		inactive: CacheWarmingStatus{State: "inactive", Reason: "waiting for first request"},
	}
	return warmer
}

// Status reports the current warming state.
func (w *CacheWarmer) Status() CacheWarmingStatus {
	w.mu.Lock()
	run := w.run
	inactive := w.inactive
	var (
		runTimer             *time.Timer
		runNextWarmAt        int64
		runExtensionOverride bool
	)
	if run != nil {
		runTimer = run.timer
		runNextWarmAt = run.nextWarmAt
		runExtensionOverride = run.extensionOverride
	}
	w.mu.Unlock()
	if w.GetMode() == CacheWarmingOff {
		return CacheWarmingStatus{State: "inactive", Reason: "cache warming disabled"}
	}
	if run == nil {
		return inactive
	}
	// The currency check runs outside the lock: it inspects the live transcript.
	if !run.isCurrent() {
		return CacheWarmingStatus{State: "inactive", Reason: "conversation context changed"}
	}
	decision := w.evaluate(run)
	refreshing := runTimer == nil
	if !decision.EconomicsAvailable && !refreshing {
		return CacheWarmingStatus{State: "inactive", Reason: "cache economics unavailable"}
	}
	return CacheWarmingStatus{
		State: cacheWarmingStateOf(refreshing), NextWarmAt: runNextWarmAt,
		Decision: &decision, ExtensionOverride: runExtensionOverride,
	}
}

func cacheWarmingStateOf(refreshing bool) string {
	if refreshing {
		return "refreshing"
	}
	return "scheduled"
}

// Start keeps the prompt cache entry written by request warm while isCurrent
// holds.
func (w *CacheWarmer) Start(request CacheWarmRequest, isCurrent func() bool) {
	w.mu.Lock()
	w.clearRunLocked()
	w.mu.Unlock()
	mode := w.GetMode()
	if mode == CacheWarmingOff {
		w.stop("cache warming disabled", nil)
		return
	}
	if !IsCacheWarmingReplayable(request.Model, request.Options) {
		w.stop("request cannot be replayed safely", nil)
		return
	}
	ttlMS, ok := GetPromptCacheTTLMs(request.Model, request.Options)
	if !ok {
		reason := "cache lifetime unavailable"
		if request.Options != nil && request.Options.CacheRetention == ai.CacheRetentionNone {
			reason = "request disabled prompt caching"
		}
		w.stop(reason, nil)
		return
	}
	delayMS, ok := GetCacheWarmingDelayMS(ttlMS)
	if !ok {
		w.stop("cache lifetime unavailable", nil)
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	run := &cacheWarmingRun{
		request: request, isCurrent: isCurrent, delayMS: delayMS,
		startedAt: time.Now().UnixMilli(), ctx: runCtx, cancel: cancel, phase: "streaming",
	}
	w.mu.Lock()
	w.run = run
	w.mu.Unlock()
	w.schedule(run)
}

// OnAgentSettled reconciles the run when the agent run settles.
func (w *CacheWarmer) OnAgentSettled() {
	w.mu.Lock()
	run := w.run
	w.mu.Unlock()
	if run == nil {
		return
	}
	if w.GetMode() == CacheWarmingStreaming {
		w.stop("agent run settled", nil)
		return
	}
	w.mu.Lock()
	run.phase = "idle"
	deadline := run.startedAt + maxIdleCacheWarmingAgeMS
	overdue := run.nextWarmAt > deadline || time.Now().UnixMilli() >= deadline
	w.mu.Unlock()
	if overdue {
		w.stop("30-minute idle safety limit reached", nil)
	}
}

// OnModeChanged reconciles an active run after the persisted mode changes.
func (w *CacheWarmer) OnModeChanged() {
	w.mu.Lock()
	run := w.run
	w.mu.Unlock()
	if run == nil {
		return
	}
	if reason := w.modeStopReason(run); reason != "" {
		w.stop(reason, nil)
	}
}

// Cancel stops warming.
func (w *CacheWarmer) Cancel() {
	w.stop("inactive", nil)
}

func (w *CacheWarmer) clearRunLocked() {
	run := w.run
	if run == nil {
		return
	}
	w.run = nil
	if run.timer != nil {
		run.timer.Stop()
	}
	run.cancel()
}

func (w *CacheWarmer) stop(reason string, stopped *CacheWarmingStatus) {
	w.mu.Lock()
	w.clearRunLocked()
	inactive := CacheWarmingStatus{State: "inactive", Reason: reason}
	if stopped != nil {
		inactive.Decision = stopped.Decision
		inactive.ExtensionOverride = stopped.ExtensionOverride
	}
	w.inactive = inactive
	w.mu.Unlock()
}

func (w *CacheWarmer) schedule(run *cacheWarmingRun) {
	w.mu.Lock()
	run.extensionOverride = false
	run.nextWarmAt = time.Now().UnixMilli() + run.delayMS
	deadline := run.startedAt + maxCacheWarmingAgeMS
	if run.phase == "idle" {
		deadline = run.startedAt + maxIdleCacheWarmingAgeMS
	}
	overdue := run.nextWarmAt > deadline || time.Now().UnixMilli() >= deadline
	w.mu.Unlock()
	if overdue {
		reason := "one-hour safety limit reached"
		if run.phase == "idle" {
			reason = "30-minute idle safety limit reached"
		}
		w.stop(reason, nil)
		return
	}
	timer := time.AfterFunc(time.Duration(max64(0, run.nextWarmAt-time.Now().UnixMilli()))*time.Millisecond, func() {
		w.refresh(run)
	})
	w.mu.Lock()
	run.timer = timer
	w.mu.Unlock()
}

func (w *CacheWarmer) refresh(run *cacheWarmingRun) {
	w.mu.Lock()
	run.timer = nil
	decide := w.Decide
	w.mu.Unlock()
	if !w.validateRun(run) {
		return
	}
	decision := w.evaluate(run)
	action := decision.Action
	if decide != nil {
		action = decide(CacheWarmingDecisionEvent{
			Type: "cache_warming_decision", WarmCost: decision.WarmCost,
			MissCost: decision.MissCost, ContinuationProbability: decision.ContinuationProbability,
			Action: decision.Action,
		})
	}
	if !w.validateRun(run) {
		return
	}
	extensionOverride := action != decision.Action
	if action == CacheWarmingActionStop {
		reason := "expected savings below threshold"
		if extensionOverride {
			reason = "stopped by extension"
		} else if !decision.EconomicsAvailable {
			reason = "cache economics unavailable"
		}
		w.stop(reason, &CacheWarmingStatus{Decision: &decision, ExtensionOverride: extensionOverride})
		return
	}

	w.mu.Lock()
	run.extensionOverride = extensionOverride
	w.mu.Unlock()

	options := ai.SimpleStreamOptions{}
	if run.request.Options != nil {
		options = *run.request.Options
	}
	one := 1
	zeroRetries := 0
	options.MaxTokens = &one
	options.MaxRetries = &zeroRetries
	options.Ctx = run.ctx
	message, err := w.StreamSimple(run.request.Model, ai.NormalizeContext(run.request.Context), &options).Result(run.ctx)
	if err != nil {
		// Cache warming is best-effort and must not affect the active agent run.
		if w.currentIs(run) {
			w.schedule(run)
		}
		return
	}
	if !w.validateRun(run) {
		return
	}
	if message.StopReason != ai.StopError && message.StopReason != ai.StopAborted {
		responseModel := message.Model
		if message.ResponseModel != nil {
			responseModel = *message.ResponseModel
		}
		note := ""
		if extensionOverride {
			note = "extension override"
		}
		entry := w.SessionManager.AppendUsage("cache_warm", string(message.Provider), responseModel, message.Usage, note)
		if w.OnWarmed != nil {
			w.OnWarmed(entry)
		}
	}
	if w.currentIs(run) {
		w.schedule(run)
	}
}

func (w *CacheWarmer) currentIs(run *cacheWarmingRun) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.run == run
}

func (w *CacheWarmer) validateRun(run *cacheWarmingRun) bool {
	w.mu.Lock()
	current := w.run == run
	w.mu.Unlock()
	if !current {
		return false
	}
	if reason := w.modeStopReason(run); reason != "" {
		w.stop(reason, nil)
		return false
	}
	if !run.isCurrent() {
		w.stop("conversation context changed", nil)
		return false
	}
	return true
}

func (w *CacheWarmer) modeStopReason(run *cacheWarmingRun) string {
	mode := w.GetMode()
	if mode == CacheWarmingOff {
		return "cache warming disabled"
	}
	if mode == CacheWarmingStreaming && run.phase == "idle" {
		return "agent run settled"
	}
	return ""
}

func (w *CacheWarmer) evaluate(run *cacheWarmingRun) CacheWarmingDecision {
	model := run.request.Model
	promptTokens := lastCacheWarmingPromptTokens(w.SessionManager.GetBranch(""))
	cacheHitCost := cacheWarmingPrice(model, 0, 0, promptTokens, 0)
	cacheMissCost := cacheWarmingPrice(model, promptTokens, 0, 0, 0)
	if model.Cost.CacheWrite > 0 {
		cacheMissCost = cacheWarmingPrice(model, 0, 0, 0, promptTokens)
	}
	warmCost := cacheWarmingPrice(model, 0, 1, promptTokens, 0)
	missCost := cacheMissCost - cacheHitCost
	if missCost < 0 {
		missCost = 0
	}
	continuationProbability := 1.0
	if run.phase == "idle" {
		continuationProbability = idleCacheWarmingContinuationProbability
	}
	economicsAvailable := promptTokens > 0 && (cacheHitCost > 0 || cacheMissCost > 0)
	expectedSavings := continuationProbability*missCost - warmCost
	action := CacheWarmingActionStop
	if expectedSavings >= cacheWarmingMinimumExpectedSavings {
		action = CacheWarmingActionWarm
	}
	return CacheWarmingDecision{
		Phase: run.phase, WarmCost: warmCost, MissCost: missCost,
		ContinuationProbability: continuationProbability, ExpectedSavings: expectedSavings,
		EconomicsAvailable: economicsAvailable, Action: action,
	}
}

// FormatCacheWarmingStatus renders the one-line status for /session.
func FormatCacheWarmingStatus(status CacheWarmingStatus, now int64) string {
	decision := status.Decision
	// A decision is attached once pi (or a host override) acted on it;
	// "inactive" without one never got that far.
	if decision == nil || (status.State == "inactive" && !decision.EconomicsAvailable && !status.ExtensionOverride) {
		reason := status.Reason
		if reason == "" {
			reason = "unknown reason"
		}
		return fmt.Sprintf("Inactive (%s)", reason)
	}
	details := formatCacheWarmingEconomics(*decision)
	if status.ExtensionOverride {
		details = "extension override, " + details
	}
	if status.State == "inactive" {
		return fmt.Sprintf("Stopped (%s)", details)
	}
	if status.State == "refreshing" {
		return fmt.Sprintf("Warming cache (%s)", details)
	}
	return fmt.Sprintf("%s (%s)", formatCacheWarmingDecisionTime(status.NextWarmAt, now), details)
}

func formatCacheWarmingEconomics(decision CacheWarmingDecision) string {
	if !decision.EconomicsAvailable {
		return "cache economics unavailable"
	}
	probability := int(decision.ContinuationProbability*100 + 0.5)
	probabilityText := fmt.Sprintf("%d%% continuation probability", probability)
	if decision.Phase == "streaming" {
		probabilityText = fmt.Sprintf("%d%% continuation probability while agent is running", probability)
	}
	comparison := "<"
	if decision.Action == CacheWarmingActionWarm {
		comparison = ">="
	}
	return fmt.Sprintf("%s, expected savings %s %s $%.3f", probabilityText,
		formatDollars(decision.ExpectedSavings), comparison, cacheWarmingMinimumExpectedSavings)
}

func formatDollars(value float64) string {
	if value < 0 {
		return fmt.Sprintf("-$%.3f", -value)
	}
	return fmt.Sprintf("$%.3f", value)
}

func formatCacheWarmingDecisionTime(nextWarmAt int64, now int64) string {
	if nextWarmAt <= 0 || nextWarmAt <= now {
		return "Decision now"
	}
	remainingSeconds := int((nextWarmAt - now + 999) / 1000)
	hours := remainingSeconds / 3600
	remainingSeconds %= 3600
	minutes := remainingSeconds / 60
	seconds := remainingSeconds % 60
	var parts []string
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	return fmt.Sprintf("Decision in %s", strings.Join(parts, " "))
}

// FormatCacheWarmingUsage renders the one-line transcript text for persisted
// cache-warming usage.
func FormatCacheWarmingUsage(entry *SessionEntry) string {
	note := ""
	if entry.Note != nil && *entry.Note != "" {
		note = fmt.Sprintf(" (%s)", *entry.Note)
	}
	if entry.Usage == nil {
		return fmt.Sprintf("Cache warmed%s: $0", note)
	}
	cost := fmt.Sprintf("%.6f", entry.Usage.Cost.Total)
	// Trim trailing zeroes in the tail while keeping at least three decimals.
	if dot := strings.Index(cost, "."); dot != -1 {
		tail := strings.TrimRight(cost[dot+1:], "0")
		if len(tail) < 3 {
			tail += strings.Repeat("0", 3-len(tail))
		}
		cost = cost[:dot+1] + tail
	}
	return fmt.Sprintf("Cache warmed%s: $%s", note, cost)
}
