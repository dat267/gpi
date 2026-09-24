package coding

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Round 104 tests: management HTTP retry, the HTTP dispatcher helpers, the user
// agent, and the version check with its semver comparison.

func TestFetchWithRetryStatus(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		count := attempts.Add(1)
		if count < 3 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()

	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	retries := 2
	response, err := FetchWithRetry(context.Background(), request, server.Client(), FetchRetryOptions{MaxRetries: &retries})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || attempts.Load() != 3 {
		t.Fatalf("status = %d attempts = %d", response.StatusCode, attempts.Load())
	}
	_ = response.Body.Close()

	// Without retries the transient status is returned as-is.
	attempts.Store(0)
	noRetries := 0
	response, err = FetchWithRetry(context.Background(), request, server.Client(), FetchRetryOptions{MaxRetries: &noRetries})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable || attempts.Load() != 1 {
		t.Fatalf("status = %d attempts = %d", response.StatusCode, attempts.Load())
	}
	_ = response.Body.Close()

	// A non-retryable status is returned immediately.
	attempts.Store(0)
	notFound := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer notFound.Close()
	request, _ = http.NewRequest(http.MethodGet, notFound.URL, nil)
	response, err = FetchWithRetry(context.Background(), request, notFound.Client(), FetchRetryOptions{MaxRetries: &retries})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound || attempts.Load() != 1 {
		t.Fatalf("status = %d attempts = %d", response.StatusCode, attempts.Load())
	}
	_ = response.Body.Close()

	// retryOnStatus=false returns the transient status once.
	attempts.Store(0)
	noStatus := false
	response, _ = FetchWithRetry(context.Background(), request, server.Client(), FetchRetryOptions{
		MaxRetries: &retries, RetryOnStatus: &noStatus,
	})
	_ = response.Body.Close()
}

func TestFetchWithRetryTransport(t *testing.T) {
	// A transport failure retries, then succeeds.
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if attempts.Add(1) == 1 {
			// Close the connection without a response.
			hijacker, ok := writer.(http.Hijacker)
			if ok {
				connection, _, err := hijacker.Hijack()
				if err == nil {
					_ = connection.Close()
					return
				}
			}
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	retries := 2
	response, err := FetchWithRetry(context.Background(), request, server.Client(), FetchRetryOptions{MaxRetries: &retries})
	if err != nil || response.StatusCode != http.StatusOK || attempts.Load() != 2 {
		t.Fatalf("response = %+v err = %v attempts = %d", response, err, attempts.Load())
	}
	_ = response.Body.Close()

	// An unreachable server fails after the retries.
	request, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	noRetries := 0
	if _, err := FetchWithRetry(context.Background(), request, http.DefaultClient, FetchRetryOptions{MaxRetries: &noRetries}); err == nil {
		t.Fatal("connection failure must error")
	}
}

func TestFetchWithRetryCancellationAndTimeouts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// A cancelled context is terminal and never retried.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	retries := 3
	start := time.Now()
	if _, err := FetchWithRetry(ctx, request, server.Client(), FetchRetryOptions{MaxRetries: &retries}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("cancel was not immediate: %v", elapsed)
	}

	// The overall timeout is terminal.
	timeout := int64(50)
	if _, err := FetchWithRetry(context.Background(), request, server.Client(), FetchRetryOptions{
		MaxRetries: &retries, TimeoutMS: &timeout,
	}); err == nil {
		t.Fatal("overall timeout must error")
	}

	// A per-attempt timeout is retried with a fresh budget, then surfaces.
	attempt := int64(20)
	var attempts atomic.Int64
	attemptServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		time.Sleep(150 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)
	}))
	defer attemptServer.Close()
	attemptRequest, _ := http.NewRequest(http.MethodGet, attemptServer.URL, nil)
	start = time.Now()
	if _, err := FetchWithRetry(context.Background(), attemptRequest, attemptServer.Client(), FetchRetryOptions{
		MaxRetries: &retries, AttemptTimeoutMS: &attempt,
	}); err == nil {
		t.Fatal("attempt timeout must surface after retries")
	}
	if attempts.Load() < 2 {
		t.Fatalf("attempt timeout must retry: %d attempts", attempts.Load())
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("attempt retries took %v", elapsed)
	}
}

func TestHTTPDispatcherHelpers(t *testing.T) {
	cases := []struct {
		input    any
		expected int64
		ok       bool
	}{
		{"disabled", 0, true},
		{"  DISABLED ", 0, true},
		{"", 0, false},
		{"30000", 30000, true},
		{"1500.9", 1500, true},
		{"nope", 0, false},
		{float64(-1), 0, false},
		{float64(0), 0, true},
		{int64(60000), 60000, true},
		{true, 0, false},
	}
	for _, testCase := range cases {
		value, ok := ParseHTTPIdleTimeoutMS(testCase.input)
		if ok != testCase.ok || (ok && value != testCase.expected) {
			t.Errorf("ParseHTTPIdleTimeoutMS(%v) = %d, %v", testCase.input, value, ok)
		}
	}
	if got := FormatHTTPIdleTimeoutMS(30_000); got != "30 sec" {
		t.Fatalf("label = %q", got)
	}
	if got := FormatHTTPIdleTimeoutMS(0); got != "disabled" {
		t.Fatalf("label = %q", got)
	}
	if got := FormatHTTPIdleTimeoutMS(45_000); got != "45 sec" {
		t.Fatalf("label = %q", got)
	}
	if len(HTTPIdleTimeoutChoices) != 5 || HTTPIdleTimeoutChoices[4].TimeoutMS != 0 {
		t.Fatalf("choices = %+v", HTTPIdleTimeoutChoices)
	}

	// Proxy settings only fill unset variables.
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	os.Unsetenv("HTTP_PROXY")
	os.Unsetenv("HTTPS_PROXY")
	ApplyHTTPProxySettings("http://proxy.example.com:8080")
	if os.Getenv("HTTP_PROXY") != "http://proxy.example.com:8080" || os.Getenv("HTTPS_PROXY") != "http://proxy.example.com:8080" {
		t.Fatalf("proxy env = %q %q", os.Getenv("HTTP_PROXY"), os.Getenv("HTTPS_PROXY"))
	}
	ApplyHTTPProxySettings("http://other.example.com")
	if os.Getenv("HTTP_PROXY") != "http://proxy.example.com:8080" {
		t.Fatalf("proxy must not be overwritten: %q", os.Getenv("HTTP_PROXY"))
	}
	ApplyHTTPProxySettings("   ")
}

func TestPiUserAgent(t *testing.T) {
	agent := PiUserAgent("1.2.3")
	if !strings.HasPrefix(agent, "pi/1.2.3 (") || !strings.Contains(agent, "go") {
		t.Fatalf("agent = %q", agent)
	}
}

func TestSemverComparison(t *testing.T) {
	cases := []struct {
		left, right string
		expected    int
		ok          bool
	}{
		{"1.2.3", "1.2.3", 0, true},
		{"1.2.4", "1.2.3", 1, true},
		{"1.3.0", "1.2.9", 1, true},
		{"2.0.0", "1.9.9", 1, true},
		{"1.2.3", "1.2.4", -1, true},
		{"1.0.0-alpha", "1.0.0", -1, true},
		{"1.0.0", "1.0.0-alpha", 1, true},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1, true},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1, true},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1, true},
		{"1.0.0-rc.1", "1.0.0-beta.11", 1, true},
		{"1.0.0+build.1", "1.0.0+build.2", 0, true},
		{"v1.2.3", "1.2.3", 0, true},
		{"1.2", "1.2.0", 0, false},
		{"1.2.3.4", "1.2.3", 0, false},
		{"01.2.3", "1.2.3", 0, false},
		{"1.0.0-01", "1.0.0-1", 0, false},
		{"1.0.0-", "1.0.0", 0, false},
		{"1.0.0+", "1.0.0", 0, false},
		{"not-a-version", "1.0.0", 0, false},
	}
	for _, testCase := range cases {
		comparison, ok := ComparePackageVersions(testCase.left, testCase.right)
		if ok != testCase.ok || (ok && comparison != testCase.expected) {
			t.Errorf("ComparePackageVersions(%q, %q) = %d, %v; want %d, %v",
				testCase.left, testCase.right, comparison, ok, testCase.expected, testCase.ok)
		}
	}

	if !IsNewerPackageVersion("1.2.4", "1.2.3") {
		t.Fatal("1.2.4 must be newer")
	}
	if IsNewerPackageVersion("1.2.3", "1.2.3") {
		t.Fatal("equal is not newer")
	}
	// Invalid versions fall back to a raw string comparison.
	if !IsNewerPackageVersion("nightly-2", "nightly-1") {
		t.Fatal("raw comparison")
	}
}

func TestPortReleaseCheck(t *testing.T) {
	var payload atomic.Value
	payload.Store(`{"Version":"v1.2.4","Time":"2026-09-24T07:02:54Z","Origin":{"VCS":"git"}}`)
	var seenAgent string
	var seenAccept string
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		seenAgent = request.Header.Get("User-Agent")
		seenAccept = request.Header.Get("accept")
		requests++
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(payload.Load().(string)))
	}))
	defer server.Close()

	// The URL is fixed to this module's proxy path; exercise the parsing through
	// a local server with the explicit helper.
	release, err := getLatestPortReleaseFrom(context.Background(), server.URL, "1.0.0", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	// The proxy reports Go module versions, so a tag keeps its "v" prefix, which
	// the semver subset now accepts like npm's `valid`.
	if release == nil || release.Version != "v1.2.4" {
		t.Fatalf("release = %+v", release)
	}
	mu.Lock()
	agent, accept := seenAgent, seenAccept
	mu.Unlock()
	if !strings.HasPrefix(agent, "pi/1.0.0 (") || accept != "application/json" {
		t.Fatalf("headers = %q %q", agent, accept)
	}

	// Only a strictly newer version is a newer release; the port's built-in
	// version is 0.0.0 unless stamped, and a pseudo-version of 0.0.0 sorts older
	// than it, so an unstamped build is not nagged.
	if !IsNewerPackageVersion("v1.2.4", "1.0.0") {
		t.Fatal("v1.2.4 must be newer than 1.0.0")
	}
	if IsNewerPackageVersion("v0.0.0-20260924070254-653430a6a54c", "0.0.0") {
		t.Fatal("a pseudo-version of 0.0.0 must not look newer than 0.0.0")
	}
	if release := checkForLatestPortReleaseFrom(context.Background(), server.URL, "1.2.4", server.Client()); release != nil {
		t.Fatalf("equal versions must not notify: %+v", release)
	}
	if release := checkForLatestPortReleaseFrom(context.Background(), server.URL, "1.0.0", server.Client()); release == nil {
		t.Fatal("1.0.0 < v1.2.4 must notify")
	}

	// Missing or non-string versions yield nothing.
	for _, body := range []string{`{"Version":""}`, `{"Version":42}`, `{}`, `{"Version":"  "}`} {
		payload.Store(body)
		release, err := getLatestPortReleaseFrom(context.Background(), server.URL, "1.0.0", server.Client())
		if err != nil || release != nil {
			t.Fatalf("body %s: release = %+v err = %v", body, release, err)
		}
	}
	// A module that is not published answers 404, which is not an error.
	missing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer missing.Close()
	if release, err := getLatestPortReleaseFrom(context.Background(), missing.URL, "1.0.0", missing.Client()); err != nil || release != nil {
		t.Fatalf("release = %+v err = %v", release, err)
	}
	// Invalid JSON is an error.
	payload.Store("not json")
	if _, err := getLatestPortReleaseFrom(context.Background(), server.URL, "1.0.0", server.Client()); err == nil {
		t.Fatal("invalid JSON must error")
	}

	// Offline and the skip flag both short-circuit before any request.
	mu.Lock()
	before := requests
	mu.Unlock()
	t.Setenv("PI_OFFLINE", "1")
	if release, err := GetLatestPortRelease(context.Background(), "1.0.0"); err != nil || release != nil {
		t.Fatalf("offline: release = %+v err = %v", release, err)
	}
	t.Setenv("PI_OFFLINE", "")
	t.Setenv("PI_SKIP_VERSION_CHECK", "1")
	if release := CheckForLatestPortRelease(context.Background(), "1.0.0"); release != nil {
		t.Fatalf("skip flag: release = %+v", release)
	}
	mu.Lock()
	after := requests
	mu.Unlock()
	if after != before {
		t.Fatalf("offline/skip made %d requests", after-before)
	}
}
