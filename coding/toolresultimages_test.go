package coding

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/dat267/gpi/ai"
)

// Round 116 tests: HTML entity decoding and tool-result image normalization.

func TestDecodeHtmlEntity(t *testing.T) {
	named := map[string]string{
		"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'",
	}
	for entity, want := range named {
		if got, ok := DecodeHtmlEntity(entity); !ok || got != want {
			t.Errorf("DecodeHtmlEntity(%q) = %q, %v", entity, got, ok)
		}
	}
	numeric := map[string]string{
		"#65":     "A",
		"#x41":    "A",
		"#X263A":  "☺",
		"#128512": "\U0001F600",
	}
	for entity, want := range numeric {
		if got, ok := DecodeHtmlEntity(entity); !ok || got != want {
			t.Errorf("DecodeHtmlEntity(%q) = %q, %v", entity, got, ok)
		}
	}
	invalid := []string{"", "nope", "#", "#x", "#xZZ", "#999999999999999", "#-1"}
	for _, entity := range invalid {
		if _, ok := DecodeHtmlEntity(entity); ok {
			t.Errorf("DecodeHtmlEntity(%q) must fail", entity)
		}
	}
	// The code point bound: 0x10FFFF is the last valid scalar value.
	if got, ok := DecodeHtmlEntity("#1114111"); !ok || got != "\U0010FFFF" {
		t.Fatalf("max code point = %q, %v", got, ok)
	}
	if _, ok := DecodeHtmlEntity("#1114112"); ok {
		t.Fatal("beyond the max code point must fail")
	}
}

func TestDecodeHtmlEntityAt(t *testing.T) {
	html := "a&amp;b&#65;c&#x42;d&euro;"
	cases := []struct {
		index  int
		text   string
		length int
		ok     bool
	}{
		{1, "&", 5, true},  // &amp;
		{7, "A", 5, true},  // &#65;
		{13, "B", 6, true}, // &#x42;
		{21, "", 0, false}, // unknown entity
		{27, "", 0, false}, // no semicolon
	}
	for _, testCase := range cases {
		decoded, ok := DecodeHtmlEntityAt(html, testCase.index)
		if ok != testCase.ok || (ok && (decoded.Text != testCase.text || decoded.Length != testCase.length)) {
			t.Errorf("DecodeHtmlEntityAt(%q, %d) = %+v, %v; want %q, %d, %v",
				html, testCase.index, decoded, ok, testCase.text, testCase.length, testCase.ok)
		}
	}
	// Entities longer than sixteen characters are not decoded.
	long := "&" + strings.Repeat("#", 20) + ";"
	if _, ok := DecodeHtmlEntityAt(long, 0); ok {
		t.Fatal("overlong entities must not decode")
	}
}

func TestNormalizeToolResultImagesPassthrough(t *testing.T) {
	// Content without images is returned unchanged.
	textOnly := ai.UserContentList{ai.TextContent{Text: "plain"}}
	normalized, changed := NormalizeToolResultImages(textOnly, nil)
	if changed || len(normalized) != 1 {
		t.Fatalf("changed = %v normalized = %+v", changed, normalized)
	}

	// A valid inline image within the limits passes through byte-identically.
	png := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	content := ai.UserContentList{ai.TextContent{Text: "shot"}, ai.ImageContent{Data: png, MimeType: "image/png"}}
	normalized, changed = NormalizeToolResultImages(content, nil)
	if changed || len(normalized) != 2 {
		t.Fatalf("changed = %v normalized = %+v", changed, normalized)
	}
	if image, ok := normalized[1].(ai.ImageContent); !ok || image.Data != png {
		t.Fatalf("normalized = %+v", normalized)
	}
}

func TestNormalizeToolResultImagesMimeNormalization(t *testing.T) {
	// A non-canonical mime (image/jpg) normalizes to image/jpeg: the block is
	// rewritten without a conversion hint (upstream only hints when the Photon
	// converter runs, D26). A real (decodable) image is required so the
	// normalization path succeeds.
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	png := base64.StdEncoding.EncodeToString(buffer.Bytes())
	content := ai.UserContentList{ai.ImageContent{Data: png, MimeType: "image/jpg"}}
	normalized, changed := NormalizeToolResultImages(content, nil)
	if !changed {
		t.Fatal("mime normalization must change the content")
	}
	if len(normalized) != 1 {
		t.Fatalf("normalized = %+v", normalized)
	}
	image, ok := normalized[0].(ai.ImageContent)
	if !ok || image.MimeType != "image/jpeg" || image.Data != png {
		t.Fatalf("normalized image = %+v", normalized[0])
	}
}
