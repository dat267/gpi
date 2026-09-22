package coding

import (
	"testing"

	"github.com/dat267/pier/ai"
)

func assistantTestMessage() *ai.AssistantMessage {
	return &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "p", Model: "m", StopReason: ai.StopStop,
		Content: ai.ContentList{ai.TextContent{Text: "hello"}},
	}
}

// TestSessionManagerPersistStopsAtAssistant pins the flush-on-first-assistant
// check to upstream's `fileEntries.some(...)`: once an assistant message is
// known to exist the scan must stop, otherwise every append on a long session
// re-unmarshals every entry (2.6 ms/append at 5k entries, 8 ms at 15.5k).
func TestSessionManagerPersistStopsAtAssistant(t *testing.T) {
	build := func(filler int) *SessionManager {
		m, _ := newTestSession(t)
		m.AppendMessage(createUserMessage("one"))
		m.AppendMessage(assistantTestMessage())
		for i := 0; i < filler; i++ {
			m.AppendMessage(createUserMessage("filler"))
		}
		return m
	}

	small, large := build(0), build(2000)
	smallAllocs := testing.AllocsPerRun(10, func() { small.AppendMessage(createUserMessage("x")) })
	largeAllocs := testing.AllocsPerRun(10, func() { large.AppendMessage(createUserMessage("x")) })

	// Appending must not get more expensive as the session grows.
	if largeAllocs > smallAllocs+200 {
		t.Fatalf("append allocated %.0f times on a 2k-entry session vs %.0f on a fresh one; "+
			"the assistant scan did not short-circuit", largeAllocs, smallAllocs)
	}
}
