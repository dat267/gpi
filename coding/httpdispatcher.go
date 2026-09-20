package coding

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Port of core/http-dispatcher.ts. The undici-specific dispatcher (connection
// pooling, H2, CONNECT tunnels) has no Go counterpart; Go's transport already
// pools connections and reads the proxy environment.
//
// D40: upstream's dispatcher sets both headersTimeout and bodyTimeout to the
// idle timeout. Go's http.Transport exposes ResponseHeaderTimeout (the
// headersTimeout equivalent) and IdleConnTimeout; there is no per-read body
// timeout, so a stalled body is still bounded by the caller's context instead.

// autoSelectFamilyAttemptTimeoutMS is kept for parity with the upstream
// dispatcher's connect tuning (Go's dialer handles this internally).
const autoSelectFamilyAttemptTimeoutMS int64 = 2_000

// HTTPIdleTimeoutChoice is one selectable idle timeout.
type HTTPIdleTimeoutChoice struct {
	Label     string
	TimeoutMS int64
}

// HTTPIdleTimeoutChoices are the offered idle timeouts.
var HTTPIdleTimeoutChoices = []HTTPIdleTimeoutChoice{
	{Label: "30 sec", TimeoutMS: 30_000},
	{Label: "1 min", TimeoutMS: 60_000},
	{Label: "2 min", TimeoutMS: 120_000},
	{Label: "5 min", TimeoutMS: 300_000},
	{Label: "disabled", TimeoutMS: 0},
}

// FormatHTTPIdleTimeoutMS renders a timeout with its choice label.
func FormatHTTPIdleTimeoutMS(timeoutMS int64) string {
	for _, choice := range HTTPIdleTimeoutChoices {
		if choice.TimeoutMS == timeoutMS {
			return choice.Label
		}
	}
	return fmt.Sprintf("%d sec", timeoutMS/1000)
}

// ApplyHTTPProxySettings seeds the proxy environment from a configured value.
func ApplyHTTPProxySettings(httpProxy string) {
	proxy := strings.TrimSpace(httpProxy)
	if proxy == "" {
		return
	}
	if os.Getenv("HTTP_PROXY") == "" {
		_ = os.Setenv("HTTP_PROXY", proxy)
	}
	if os.Getenv("HTTPS_PROXY") == "" {
		_ = os.Setenv("HTTPS_PROXY", proxy)
	}
}

// ConfigureHTTPDispatcher installs the shared HTTP transport settings (the Go
// equivalent of configuring the global dispatcher).
func ConfigureHTTPDispatcher(timeoutMS int64) error {
	normalized, ok := ParseHTTPIdleTimeoutMS(timeoutMS)
	if !ok {
		return fmt.Errorf("Invalid HTTP idle timeout: %v", timeoutMS)
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil
	}
	if normalized == 0 {
		transport.ResponseHeaderTimeout = 0
		transport.IdleConnTimeout = 0
	} else {
		duration := time.Duration(normalized) * time.Millisecond
		transport.ResponseHeaderTimeout = duration
		transport.IdleConnTimeout = duration
	}
	transport.Proxy = proxyFromEnvironmentOrDefault(transport.Proxy)
	http.DefaultTransport = transport
	return nil
}

// proxyFromEnvironmentOrDefault preserves an explicit Proxy function.
func proxyFromEnvironmentOrDefault(existing func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	if existing != nil {
		return existing
	}
	return http.ProxyFromEnvironment
}
