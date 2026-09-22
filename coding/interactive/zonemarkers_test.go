package interactive

import (
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
)

// TestMessageComponentsDoNotStackZoneMarkers pins that rendering a message
// twice does not stack OSC 133 markers. Both components used to write the
// markers into the lines their container returned; those lines are the
// container's render cache now shared with the parent, so writing into them
// grew the markers on every paint (and made every frame miss the caches).
func TestMessageComponentsDoNotStackZoneMarkers(t *testing.T) {
	newRendererTestTheme(t)

	user := NewUserMessageComponent("hello there", nil, 0, nil)
	first := strings.Join(user.Render(40), "\n")
	second := strings.Join(user.Render(40), "\n")
	if first != second {
		t.Fatalf("second render differs:\n%q\n%q", first, second)
	}
	if got := strings.Count(second, osc133ZoneStart); got != 1 {
		t.Fatalf("zone-start markers = %d, want 1:\n%q", got, second)
	}
	if got := strings.Count(second, osc133ZoneEnd); got != 1 {
		t.Fatalf("zone-end markers = %d, want 1:\n%q", got, second)
	}

	assistantMessage := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "test", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "hi"}},
	}
	assistant := NewAssistantMessageComponent(assistantMessage, false, nil, "Thinking...", 0, nil)
	firstA := strings.Join(assistant.Render(40), "\n")
	secondA := strings.Join(assistant.Render(40), "\n")
	if firstA != secondA {
		t.Fatalf("assistant second render differs:\n%q\n%q", firstA, secondA)
	}
	if got := strings.Count(secondA, osc133ZoneStart); got != 1 {
		t.Fatalf("assistant zone-start markers = %d, want 1:\n%q", got, secondA)
	}
}
