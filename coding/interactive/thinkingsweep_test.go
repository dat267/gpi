package interactive

import (
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/tui"
)

func thinkingSweepChat(t *testing.T, count int) (*QueueController, []*AssistantMessageComponent) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	chat := &tui.Container{}
	components := make([]*AssistantMessageComponent, 0, count)
	for i := 0; i < count; i++ {
		message := &ai.AssistantMessage{
			API: ai.APIAnthropicMessages, Provider: "test", Model: "m",
			Content: ai.ContentList{
				ai.ThinkingContent{Thinking: "a thought worth hiding"},
				ai.TextContent{Text: "the answer"},
			},
		}
		component := NewAssistantMessageComponent(message, false, nil, "", 0, nil)
		components = append(components, component)
		chat.AddChild(component)
	}
	return NewQueueController(nil, nil, nil, nil, chat, nil), components
}

func showsThinking(component *AssistantMessageComponent) bool {
	return strings.Contains(strings.Join(component.Render(60), "\n"), "a thought worth hiding")
}

// The sweep rebuilds the tail synchronously — what is on screen has to be right
// on this frame — and defers the rest, because each rebuilt message is re-rendered
// by the next paint (the cost that froze ctrl+t on a long transcript).
func TestThinkingSweepIsChunked(t *testing.T) {
	controller, components := thinkingSweepChat(t, 20)

	controller.UpdateThinkingBlockVisibility(true)

	// The tail — what is on screen — is right on this frame.
	windowStart := len(components) - thinkingSweepWindowComponents
	for index := windowStart; index < len(components); index++ {
		if showsThinking(components[index]) {
			t.Errorf("component %d in the window still shows thinking", index)
		}
	}
	// The rest is deferred. A synchronous sweep would have hidden everything
	// already, which is exactly the freeze this exists to avoid — so this is the
	// assertion that fails if the chunking goes away.
	deferred := 0
	for index := 0; index < windowStart; index++ {
		if showsThinking(components[index]) {
			deferred++
		}
	}
	if deferred == 0 {
		t.Error("nothing was deferred: the sweep rebuilt the whole transcript")
	}

	// Draining converges on every message and then reports no work left.
	beats := 0
	for controller.MaterializeThinkingChunk() {
		beats++
		if beats > len(components) {
			t.Fatalf("drain did not terminate after %d beats", beats)
		}
	}
	for index, component := range components {
		if showsThinking(component) {
			t.Errorf("component %d still shows thinking after the drain", index)
		}
	}
	if controller.MaterializeThinkingChunk() {
		t.Error("the drain reported work with nothing deferred")
	}
}

// A second toggle while a sweep is in flight has to converge on the new value
// rather than finish the old one.
func TestThinkingSweepRestartsOnToggle(t *testing.T) {
	controller, components := thinkingSweepChat(t, 20)

	controller.UpdateThinkingBlockVisibility(true)
	controller.MaterializeThinkingChunk() // partially drained
	controller.UpdateThinkingBlockVisibility(false)

	for controller.MaterializeThinkingChunk() {
	}
	for index, component := range components {
		if !showsThinking(component) {
			t.Errorf("component %d stayed hidden after the second toggle", index)
		}
	}
}
