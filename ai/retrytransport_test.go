package ai

import "testing"

// These all mean the same thing: the socket died mid-request. The classifier
// matched provider-shaped text and several fetch/websocket phrasings, but none of
// the OS-level abort strings, so a call that hit one was not retried at all. The
// reported case is Termux/Windows "software caused connection abort"
// (ECONNABORTED) — upstream does not retry it either, so covering it is a
// deliberate divergence (D171).
func TestTransportAbortsAreRetryable(t *testing.T) {
	messages := []string{
		`Post "https://hyper.charm.land/v1/chat/completions": read tcp 192.168.1.12:45272->142.250.197.147:443: read: software caused connection abort`,
		"read tcp 10.0.0.2:443: read: connection reset by peer",
		"write tcp 10.0.0.2:443: write: broken pipe",
		"unexpected EOF",
	}
	for _, message := range messages {
		message := message
		response := &AssistantMessage{StopReason: StopError, ErrorMessage: &message}
		if !IsRetryableAssistantError(response) {
			t.Errorf("a dead socket was not retried: %q", message)
		}
	}
}

// The account/quota guard is checked first and must keep winning, even when the
// message also carries transport text.
func TestTransportPatternsDoNotOverrideTheLimitGuard(t *testing.T) {
	message := "connection reset by peer while reading insufficient_quota response"
	response := &AssistantMessage{StopReason: StopError, ErrorMessage: &message}
	if IsRetryableAssistantError(response) {
		t.Error("a usage-limit error was retried because its text mentioned a reset")
	}
}
