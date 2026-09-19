package ai

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Port of utils/provider-retry.ts (SDK-style provider request retry) and
// utils/retry.ts (agent-level assistant retry policy).

// ProviderError is an HTTP failure from a provider request, carrying the
// status and response headers the retry policy inspects.
type ProviderError struct {
	Status  int
	Headers http.Header
	Message string
	// Body carries the raw response body for error classification.
	Body string
}

func (e *ProviderError) Error() string { return e.Message }

const defaultMaxRetryDelayMS = 60_000

// isRetryableProviderError mirrors the pinned OpenAI/Anthropic SDK retry
// policy; review when either SDK is upgraded.
func isRetryableProviderError(err error) bool {
	pe, ok := err.(*ProviderError)
	if !ok {
		return false
	}
	if pe.Headers != nil {
		switch pe.Headers.Get("x-should-retry") {
		case "true":
			return true
		case "false":
			return false
		}
	}
	if pe.Status == 0 {
		return true
	}
	return pe.Status == 408 || pe.Status == 409 || pe.Status == 429 || pe.Status >= 500
}

func validateServerRetryDelayMS(delayMS int64, maxRetryDelayMS *int, providerErrorMessage string) (int64, error) {
	maxDelay := int64(defaultMaxRetryDelayMS)
	if maxRetryDelayMS != nil {
		maxDelay = int64(*maxRetryDelayMS)
	}
	if maxDelay > 0 && delayMS > maxDelay {
		return 0, fmt.Errorf(
			"Server requested %ds retry delay (max: %ds). %s",
			int64(math.Ceil(float64(delayMS)/1000)), int64(math.Ceil(float64(maxDelay)/1000)),
			providerErrorMessage,
		)
	}
	return delayMS, nil
}

// GetRetryDelayMS resolves the delay before the next retry attempt
// (port of getRetryDelayMs).
func GetRetryDelayMS(err *ProviderError, retryIndex int, maxRetryDelayMS *int) (int64, error) {
	if err.Headers != nil {
		if v := err.Headers.Get("retry-after-ms"); v != "" {
			if f, parseErr := strconv.ParseFloat(v, 64); parseErr == nil {
				return validateServerRetryDelayMS(int64(f), maxRetryDelayMS, err.Message)
			}
		}
		if v := err.Headers.Get("retry-after"); v != "" {
			if seconds, parseErr := strconv.ParseFloat(v, 64); parseErr == nil {
				return validateServerRetryDelayMS(int64(seconds*1000), maxRetryDelayMS, err.Message)
			}
			if at, parseErr := http.ParseTime(v); parseErr == nil {
				delay := at.Sub(time.Now()).Milliseconds()
				return validateServerRetryDelayMS(delay, maxRetryDelayMS, err.Message)
			}
		}
	}
	// min(0.5 * 2^retryIndex, 8) * 1000 * (1 - rand*0.25)
	exponential := math.Min(0.5*math.Pow(2, float64(retryIndex)), 8) * 1000
	return int64(exponential * (1 - pseudoRand()*0.25)), nil
}

// RetryProviderRequest reproduces the retry behavior used by the OpenAI and
// Anthropic SDKs while making the backoff sleep interruptible via ctx.
// Provider-requested delays above MaxRetryDelayMS fail immediately (60
// seconds by default); zero disables the cap.
func RetryProviderRequest(ctx context.Context, request func() (*http.Response, error), options *ProviderRetryOptions) (*http.Response, error) {
	maxRetries := 0
	if options != nil && options.MaxRetries != nil {
		maxRetries = *options.MaxRetries
	}
	retriesRemaining := maxRetries
	for {
		resp, err := request()
		if err == nil {
			return resp, nil
		}
		if ctxErr(ctx) != nil {
			return nil, ctxErr(ctx)
		}
		if retriesRemaining <= 0 || !isRetryableProviderError(err) {
			return nil, err
		}
		retryIndex := maxRetries - retriesRemaining
		retriesRemaining--
		delay, delayErr := GetRetryDelayMS(err.(*ProviderError), retryIndex, options.maxRetryDelayOrNil())
		if delayErr != nil {
			return nil, delayErr
		}
		select {
		case <-time.After(time.Duration(max(0, delay)) * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ProviderRetryOptions tune RetryProviderRequest.
type ProviderRetryOptions struct {
	MaxRetries      *int
	MaxRetryDelayMS *int
}

func (o *ProviderRetryOptions) maxRetryDelayOrNil() *int {
	if o == nil {
		return nil
	}
	return o.MaxRetryDelayMS
}

// --- utils/retry.ts: agent-level assistant retry policy ---

// RetryPolicy is the bounded-retry policy for assistant calls.
type RetryPolicy struct {
	Enabled bool
	// MaxRetries is the max retry attempts (0 = no retries). The initial
	// call never counts as a retry.
	MaxRetries int
	// BaseDelayMS: per-attempt delay is BaseDelayMS * 2^(attempt-1) before jitter.
	BaseDelayMS int
	// MaxAgentDelayMS caps each computed delay; defaults to 60 seconds.
	MaxAgentDelayMS int64
}

const DefaultMaxAgentRetryDelayMS = 60_000

// RetryDelayMS computes the delay for an attempt (1-indexed).
func RetryDelayMS(policy RetryPolicy, attempt int) int64 {
	delay := float64(policy.BaseDelayMS) * math.Pow(2, float64(max(0, attempt-1)))
	safeDelay := delay
	if safeDelay > math.MaxInt64 {
		safeDelay = math.MaxInt64
	}
	capDelay := policy.MaxAgentDelayMS
	if capDelay == 0 {
		capDelay = DefaultMaxAgentRetryDelayMS
	}
	return int64(math.Min(safeDelay, float64(capDelay)))
}

// RetryCallbacks are emitted by RetryAssistantCall around each retry.
type RetryCallbacks struct {
	// OnRetryScheduled fires before the backoff sleep of each retry (1-indexed).
	OnRetryScheduled func(attempt, maxAttempts int, delayMS int64, errorMessage string) error
	// OnRetryAttemptStart fires after the sleep, before the retried call.
	OnRetryAttemptStart func() error
	// OnRetryFinished fires once when the loop ends.
	OnRetryFinished func(success bool, attempt int, finalError string) error
}

// RetryAssistantCall runs a single assistant-producing call with bounded
// retry on transient errors (port of retryAssistantCall).
func RetryAssistantCall(
	produce func() (*AssistantMessage, error),
	policy *RetryPolicy,
	ctx context.Context,
	callbacks *RetryCallbacks,
) (*AssistantMessage, error) {
	maxAttempts := 0
	if policy != nil && policy.Enabled {
		maxAttempts = policy.MaxRetries
	}
	notify := func(f func() error) {
		if f == nil {
			return
		}
		_ = f()
	}

	attempt := 0
	lastRetry := -1
	lastErrorMessage := ""
	for {
		response, err := produce()
		if err != nil {
			return nil, err
		}

		// Abort: terminal but not successful. Never retried.
		if response.StopReason == StopAborted {
			if lastRetry > 0 && callbacks != nil {
				notify(func() error { return callbacks.OnRetryFinished(false, lastRetry, "") })
			}
			return response, nil
		}

		// Success: non-error responses return as-is.
		if response.StopReason != StopError {
			if lastRetry > 0 && callbacks != nil {
				notify(func() error { return callbacks.OnRetryFinished(true, lastRetry, "") })
			}
			return response, nil
		}

		finalMessage := ""
		if response.ErrorMessage != nil {
			finalMessage = *response.ErrorMessage
		}
		// Non-retryable, or budget exhausted.
		if attempt >= maxAttempts || !IsRetryableAssistantError(response) {
			if lastRetry > 0 && callbacks != nil {
				notify(func() error { return callbacks.OnRetryFinished(false, lastRetry, finalMessage) })
			}
			return response, nil
		}

		attempt++
		lastRetry = attempt
		lastErrorMessage = finalMessage
		if lastErrorMessage == "" {
			lastErrorMessage = "Unknown error"
		}
		delayMS := RetryDelayMS(*policy, attempt)
		if callbacks != nil && callbacks.OnRetryScheduled != nil {
			notify(func() error { return callbacks.OnRetryScheduled(attempt, maxAttempts, delayMS, lastErrorMessage) })
		}

		select {
		case <-time.After(time.Duration(delayMS) * time.Millisecond):
		case <-ctx.Done():
			// Aborts during backoff normalize to an aborted AssistantMessage.
			if callbacks != nil && callbacks.OnRetryFinished != nil {
				notify(func() error { return callbacks.OnRetryFinished(false, attempt, lastErrorMessage) })
			}
			aborted := *response
			aborted.ErrorMessage = nil
			aborted.StopReason = StopAborted
			return &aborted, nil
		}
		if callbacks != nil && callbacks.OnRetryAttemptStart != nil {
			notify(callbacks.OnRetryAttemptStart)
		}
	}
}

// Non-retryable provider limit errors (subscription/account limits, billing).
var nonRetryableProviderLimitErrorPattern = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`GoUsageLimitError`, `FreeUsageLimitError`,
	`Monthly usage limit reached`, `available balance`,
	`insufficient_quota`, `out of budget`, `quota exceeded`, `billing`,
}, "|"))

// Retryable provider/transport errors.
var retryableProviderErrorPattern = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`overloaded`, `currently experiencing high demand`, `rate.?limit`, `too many requests`,
	`429`, `500`, `502`, `503`, `504`, `520`, `524`,
	`service.?unavailable`, `server.?error`, `internal.?error`,
	`provider.?returned.?error`, `exceeded request buffer limit while retrying upstream`,
	`network.?error`, `connection.?error`, `connection.?refused`, `connection.?lost`,
	`other side closed`, `fetch failed`, `getaddrinfo`, `ENOTFOUND`, `EAI_AGAIN`,
	`upstream.?connect`, `reset before headers`, `socket hang up`, `socket connection was closed`,
	`timed? out`, `timeout`, `terminated`,
	`websocket.?closed`, `websocket.?error`,
	`ended without`, `stream ended before message_stop`,
	`stream ended before a terminal response event`, `http2 request did not get a response`,
	`retry delay`,
	`you can retry your request`, `try your request again`, `please retry your request`,
	`ResourceExhausted`,
}, "|"))

// IsRetryableAssistantError classifies whether a failed assistant message
// looks like a transient provider or transport error.
func IsRetryableAssistantError(message *AssistantMessage) bool {
	if message.StopReason != StopError || message.ErrorMessage == nil {
		return false
	}
	errorMessage := *message.ErrorMessage
	if nonRetryableProviderLimitErrorPattern.MatchString(errorMessage) {
		return false
	}
	return retryableProviderErrorPattern.MatchString(errorMessage)
}

// pseudoRand is math/rand-backed jitter source for retry backoff.
func pseudoRand() float64 { return rand.Float64() }
