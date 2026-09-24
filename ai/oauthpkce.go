package ai

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

// Port of auth/oauth/pkce.ts, device-code.ts, and oauth-page.ts.

// PKCEPair is one PKCE verifier/challenge pair.
type PKCEPair struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE generates a PKCE verifier and its S256 challenge (upstream
// generatePKCE).
func GeneratePKCE() (PKCEPair, error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return PKCEPair{}, err
	}
	verifier := base64URLEncode(verifierBytes)
	sum := sha256.Sum256([]byte(verifier))
	return PKCEPair{Verifier: verifier, Challenge: base64URLEncode(sum[:])}, nil
}

func base64URLEncode(bytes []byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes)
}

// Device flow messages and intervals (upstream device-code.ts).
const (
	DeviceCodeCancelMessage   = "Login cancelled"
	DeviceCodeTimeoutMessage  = "Device flow timed out"
	DeviceCodeSlowDownMessage = "Device flow timed out after one or more slow_down responses. " +
		"This is often caused by clock drift in WSL or VM environments. " +
		"Please sync or restart the VM clock and try again."
	deviceCodeMinimumIntervalMS         = 1000
	deviceCodeDefaultIntervalSeconds    = 5
	deviceCodeSlowDownIntervalIncrement = 5 * time.Second
)

// OAuthDeviceCodePollResult is one poll outcome.
type OAuthDeviceCodePollResult[T any] struct {
	Status string // "pending" | "slow_down" | "failed" | "complete"
	// Message is set on "failed".
	Message string
	// IntervalSeconds is set on "slow_down" when the server provided one.
	IntervalSeconds *int
	// Value is set on "complete".
	Value T
}

// deviceCodeSleep is the poll loop's sleep. RFC 8628's interval is in whole
// seconds and is floored at one, so a flow that polls a few times costs real
// seconds; the OAuth tests assert which polls happen, never how long they take,
// and they swap this for a millisecond sleep (fastDeviceCodeFlows) instead of
// spending ~25 s of the suite asleep.
var deviceCodeSleep = AbortableSleep

// OAuthDeviceCodePollOptions configure PollOAuthDeviceCodeFlow.
type OAuthDeviceCodePollOptions[T any] struct {
	IntervalSeconds     *int
	ExpiresInSeconds    *int
	WaitBeforeFirstPoll bool
	Poll                func() (OAuthDeviceCodePollResult[T], error)
	Ctx                 context.Context
}

// AbortableSleep sleeps until the duration elapses or the context is done
// (upstream abortableSleep).
func AbortableSleep(ctx context.Context, duration time.Duration, cancelMessage string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s", cancelMessage)
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	if ctx == nil {
		<-timer.C
		return nil
	}
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s", cancelMessage)
	}
}

// PollOAuthDeviceCodeFlow polls a device-code flow until it completes
// (upstream pollOAuthDeviceCodeFlow), applying RFC 8628 slow_down semantics.
func PollOAuthDeviceCodeFlow[T any](options OAuthDeviceCodePollOptions[T]) (T, error) {
	var zero T
	deadline := time.Time{}
	if options.ExpiresInSeconds != nil {
		deadline = time.Now().Add(time.Duration(*options.ExpiresInSeconds) * time.Second)
	}
	intervalSeconds := deviceCodeDefaultIntervalSeconds
	if options.IntervalSeconds != nil {
		intervalSeconds = *options.IntervalSeconds
	}
	interval := time.Duration(intervalSeconds) * time.Second
	if interval < deviceCodeMinimumIntervalMS*time.Millisecond {
		interval = deviceCodeMinimumIntervalMS * time.Millisecond
	}

	slowDownResponses := 0
	remaining := func() (time.Duration, bool) {
		if deadline.IsZero() {
			return time.Duration(1) << 62, true
		}
		left := time.Until(deadline)
		return left, left > 0
	}
	if options.WaitBeforeFirstPoll {
		if left, ok := remaining(); ok {
			wait := interval
			if left < wait {
				wait = left
			}
			if err := deviceCodeSleep(options.Ctx, wait, DeviceCodeCancelMessage); err != nil {
				return zero, err
			}
		}
	}

	for {
		if left, ok := remaining(); !ok || left <= 0 {
			break
		}
		if options.Ctx != nil && options.Ctx.Err() != nil {
			return zero, fmt.Errorf("%s", DeviceCodeCancelMessage)
		}
		result, err := options.Poll()
		if err != nil {
			return zero, err
		}
		switch result.Status {
		case "complete":
			return result.Value, nil
		case "failed":
			return zero, fmt.Errorf("%s", result.Message)
		case "slow_down":
			slowDownResponses++
			if result.IntervalSeconds != nil && *result.IntervalSeconds > 0 {
				interval = time.Duration(*result.IntervalSeconds) * time.Second
				if interval < deviceCodeMinimumIntervalMS*time.Millisecond {
					interval = deviceCodeMinimumIntervalMS * time.Millisecond
				}
			} else {
				interval += deviceCodeSlowDownIntervalIncrement
			}
		}

		left, ok := remaining()
		if !ok || left <= 0 {
			break
		}
		wait := interval
		if left < wait {
			wait = left
		}
		if err := deviceCodeSleep(options.Ctx, wait, DeviceCodeCancelMessage); err != nil {
			return zero, err
		}
	}
	if slowDownResponses > 0 {
		return zero, fmt.Errorf("%s", DeviceCodeSlowDownMessage)
	}
	return zero, fmt.Errorf("%s", DeviceCodeTimeoutMessage)
}

// oauthLogoSVG is the OAuth callback page logo (upstream LOGO_SVG).
const oauthLogoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 800" aria-hidden="true"><path fill="#fff" fill-rule="evenodd" d="M165.29 165.29 H517.36 V400 H400 V517.36 H282.65 V634.72 H165.29 Z M282.65 282.65 V400 H400 V282.65 Z"/><path fill="#fff" d="M517.36 400 H634.72 V634.72 H517.36 Z"/></svg>`

// OAuthSuccessHTML renders the success page (upstream oauthSuccessHtml).
func OAuthSuccessHTML(message string) string {
	return renderOAuthPage("Authentication successful", "Authentication successful", message, "")
}

// OAuthErrorHTML renders the failure page (upstream oauthErrorHtml).
func OAuthErrorHTML(message, details string) string {
	return renderOAuthPage("Authentication failed", "Authentication failed", message, details)
}

func renderOAuthPage(title, heading, message, details string) string {
	detailBlock := ""
	if details != "" {
		detailBlock = `<div class="details">` + escapeOAuthHTML(details) + `</div>`
	}
	return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>` + escapeOAuthHTML(title) + `</title>
  <style>
    :root {
      --text: #fafafa;
      --text-dim: #a1a1aa;
      --page-bg: #09090b;
      --font-sans: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, "Noto Sans", sans-serif, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol", "Noto Color Emoji";
      --font-mono: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
    }
    * { box-sizing: border-box; }
    html { color-scheme: dark; }
    body {
      margin: 0;
      min-height: 100vh;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 24px;
      background: var(--page-bg);
      color: var(--text);
      font-family: var(--font-sans);
      text-align: center;
    }
    main {
      width: 100%;
      max-width: 560px;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
    }
    .logo {
      width: 72px;
      height: 72px;
      display: block;
      margin-bottom: 24px;
    }
    h1 {
      margin: 0 0 10px;
      font-size: 28px;
      line-height: 1.15;
      font-weight: 650;
      color: var(--text);
    }
    p {
      margin: 0;
      line-height: 1.7;
      color: var(--text-dim);
      font-size: 15px;
    }
    .details {
      margin-top: 16px;
      font-family: var(--font-mono);
      font-size: 13px;
      color: var(--text-dim);
      white-space: pre-wrap;
      word-break: break-word;
    }
  </style>
</head>
<body>
  <main>
    <div class="logo">` + oauthLogoSVG + `</div>
    <h1>` + escapeOAuthHTML(heading) + `</h1>
    <p>` + escapeOAuthHTML(message) + `</p>
    ` + detailBlock + `
  </main>
</body>
</html>`
}

func escapeOAuthHTML(value string) string {
	replacer := []struct{ from, to string }{
		{"&", "&amp;"}, {"<", "&lt;"}, {">", "&gt;"}, {"\"", "&quot;"}, {"'", "&#39;"},
	}
	out := value
	for _, entry := range replacer {
		out = replaceAll(out, entry.from, entry.to)
	}
	return out
}

// replaceAll is a small local helper (strings.ReplaceAll without the import
// cycle risk in this file's scope).
func replaceAll(input, from, to string) string {
	if from == "" {
		return input
	}
	var builder []byte
	for index := 0; index < len(input); {
		if index+len(from) <= len(input) && input[index:index+len(from)] == from {
			builder = append(builder, to...)
			index += len(from)
			continue
		}
		builder = append(builder, input[index])
		index++
	}
	return string(builder)
}

// randomHex returns n random bytes as a hex string (upstream createState).
func randomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	const digits = "0123456789abcdef"
	out := make([]byte, 0, n*2)
	for _, value := range bytes {
		out = append(out, digits[value>>4], digits[value&0x0f])
	}
	return string(out), nil
}
