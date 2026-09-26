package interactive

import (
	"context"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
)

// TestInitialImagesRideTheFirstPrompt pins upstream's @file image attachment.
//
// buildInitialMessage puts an image's content on the initial message, so it rides
// the first prompt — and only that one: a later initial message is its own prompt
// with no images. Before this, the images were dropped with a warning because the
// interactive mode had no path to attach them (D153).
func TestInitialImagesRideTheFirstPrompt(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	images := []ai.ImageContent{{Data: "AAAA", MimeType: "image/png"}}

	type call struct {
		text   string
		images []ai.ImageContent
	}
	calls := make(chan call, 4)
	wiring.Prompt = func(_ context.Context, text string, imgs []ai.ImageContent) error {
		calls <- call{text: text, images: imgs}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		wiring.runLoop(ctx, []string{"what is this", "and this"}, images)
	}()

	got := make([]call, 0, 2)
	for len(got) < 2 {
		select {
		case c := <-calls:
			got = append(got, c)
		case <-time.After(6 * time.Second):
			cancel()
			t.Fatalf("only %d of 2 prompts ran", len(got))
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the loop did not exit")
	}

	if got[0].text != "what is this" {
		t.Errorf("first prompt = %q", got[0].text)
	}
	if len(got[0].images) != 1 || got[0].images[0].MimeType != "image/png" {
		t.Fatalf("the first prompt carried %#v, want the @file image", got[0].images)
	}
	if len(got[1].images) != 0 {
		t.Errorf("a later initial message carried images: %#v", got[1].images)
	}
}
