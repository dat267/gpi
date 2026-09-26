package coding

import (
	"bytes"
	"encoding/base64"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
)

// A wide image is resized on the way to the model, with the dimension note in the
// text (D8: the resize used to be left for later, and the tool sent the original
// bytes at whatever size they were).
func TestReadToolResizesWideImages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wide.png")
	if err := os.WriteFile(path, solidPNG(t, 2100, 4), 0o644); err != nil {
		t.Fatal(err)
	}

	result := execTool(t, CreateReadTool(dir, nil), `{"path":"wide.png"}`)
	if len(result.Content) != 2 {
		t.Fatalf("content = %#v, want text and image", result.Content)
	}
	text := result.Content[0].(ai.TextContent).Text
	if !strings.HasPrefix(text, "Read image file [image/png]") {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(text, "[Image: original 2100x4, displayed at 2000x4.") {
		t.Errorf("the dimension note is missing from %q", text)
	}
	imageContent := result.Content[1].(ai.ImageContent)
	data, err := base64.StdEncoding.DecodeString(imageContent.Data)
	if err != nil {
		t.Fatalf("image data is not base64: %v", err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the attached image does not decode: %v", err)
	}
	if config.Width != 2000 || config.Height != 4 {
		t.Errorf("attached image is %dx%d, want 2000x4", config.Width, config.Height)
	}
}

// A model that cannot accept images gets the note saying so, and still receives
// the image content: the note tells the provider-facing truth, it does not strip
// the attachment (upstream getNonVisionImageNote).
func TestReadToolNotesNonVisionModels(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.png"), solidPNG(t, 20, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	textOnly := &ai.Model{Input: []string{"text"}}
	tool := CreateReadTool(dir, &ReadToolOptions{Model: func() *ai.Model { return textOnly }})

	result := execTool(t, tool, `{"path":"small.png"}`)
	if len(result.Content) != 2 {
		t.Fatalf("content = %#v", result.Content)
	}
	text := result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "[Current model does not support images. The image will be omitted from this request.]") {
		t.Errorf("the non-vision note is missing from %q", text)
	}
}

// A format the sniffer accepts but the standard library cannot decode degrades to
// a text-only result carrying the omission message, rather than attaching bytes
// the model cannot read (D26).
func TestReadToolReportsUndecodableImages(t *testing.T) {
	dir := t.TempDir()
	// A WebP header is enough for the sniffer; image/webp has no standard-library
	// decoder, which is exactly the case being pinned.
	webp := append([]byte("RIFF"), make([]byte, 4)...)
	webp = append(webp, []byte("WEBPVP8 ")...)
	webp = append(webp, make([]byte, 32)...)
	if err := os.WriteFile(filepath.Join(dir, "shot.webp"), webp, 0o644); err != nil {
		t.Fatal(err)
	}

	result := execTool(t, CreateReadTool(dir, nil), `{"path":"shot.webp"}`)
	if len(result.Content) != 1 {
		t.Fatalf("content = %#v, want the text only", result.Content)
	}
	text := result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "Read image file [image/webp]") {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(text, "[Image omitted:") {
		t.Errorf("the omission message is missing from %q", text)
	}
}

// The model is read per call, so a session that switches models mid-run does not
// keep the previous model's capabilities (upstream reads ctx.model each time).
func TestReadToolReadsTheModelPerCall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.png"), solidPNG(t, 20, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	current := &ai.Model{Input: []string{"image"}}
	tool := CreateReadTool(dir, &ReadToolOptions{Model: func() *ai.Model { return current }})

	text := execTool(t, tool, `{"path":"small.png"}`).Content[0].(ai.TextContent).Text
	if strings.Contains(text, "does not support images") {
		t.Fatalf("a vision model got the non-vision note: %q", text)
	}
	current = &ai.Model{Input: []string{"text"}}
	text = execTool(t, tool, `{"path":"small.png"}`).Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "does not support images") {
		t.Fatalf("the switched-to model did not take effect: %q", text)
	}
}

// A filename with a space is not special: the path travels as one JSON string,
// nothing in the pipeline splits on spaces, and ResolveReadPath only folds the
// unicode variants (macOS screenshots) on top of the plain form.
func TestReadToolHandlesSpacedImageNames(t *testing.T) {
	dir := t.TempDir()
	name := "my screenshot 2026.png"
	if err := os.WriteFile(filepath.Join(dir, name), solidPNG(t, 24, 16), 0o644); err != nil {
		t.Fatal(err)
	}
	result := execTool(t, CreateReadTool(dir, nil), `{"path":"my screenshot 2026.png"}`)
	if len(result.Content) != 2 {
		t.Fatalf("content = %#v, want text and image", result.Content)
	}
	if text := result.Content[0].(ai.TextContent).Text; !strings.HasPrefix(text, "Read image file [image/png]") {
		t.Errorf("text = %q", text)
	}
	if _, ok := result.Content[1].(ai.ImageContent); !ok {
		t.Errorf("content[1] = %T, want an image", result.Content[1])
	}

	// The narrow-no-break-space form macOS screenshots use resolves to the same
	// file, which is what the AM/PM variant is for.
	narrow := "my screenshot\u202f2026.png"
	result = execTool(t, CreateReadTool(dir, nil), `{"path":"`+narrow+`"}`)
	if len(result.Content) != 2 {
		t.Fatalf("the narrow-space variant did not resolve: %#v", result.Content)
	}
}
