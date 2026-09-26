package coding

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"math/rand"
	"testing"
)

// noisyPNG builds a PNG with incompressible content, so the byte budget is what
// the resize loop has to satisfy rather than PNG's own compression.
func noisyPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	random := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = uint8(random.Intn(256))
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func solidPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := range img.Pix {
		img.Pix[i] = 0x7f
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// TestResizeInlineImageFitsTheMaxDimensions covers the resizer D26 recorded as
// missing: upstream's resizeImageInProcess fits the image to maxWidth/maxHeight
// and re-encodes it, where the port used to return nil (and the caller then
// reported an omission).
func TestResizeInlineImageFitsTheMaxDimensions(t *testing.T) {
	resized := resizeInlineImage(solidPNG(t, 3000, 100), "image/png", DefaultImageResizeOptions)
	if resized == nil {
		t.Fatal("a 3000x100 image was not resized")
	}
	if !resized.WasResized {
		t.Error("WasResized = false for an image that had to be scaled")
	}
	if resized.Width != 2000 || resized.Height != 67 {
		t.Fatalf("resized to %dx%d, want 2000x67 (aspect kept, upstream rounding)", resized.Width, resized.Height)
	}
	if resized.OriginalWidth != 3000 || resized.OriginalHeight != 100 {
		t.Errorf("original reported as %dx%d", resized.OriginalWidth, resized.OriginalHeight)
	}
	data, err := base64.StdEncoding.DecodeString(resized.Data)
	if err != nil {
		t.Fatalf("data is not base64: %v", err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("resized bytes do not decode: %v", err)
	}
	if config.Width != resized.Width || config.Height != resized.Height {
		t.Errorf("encoded image is %dx%d, want %dx%d", config.Width, config.Height, resized.Width, resized.Height)
	}
	if note := FormatDimensionNote(resized); note == "" {
		t.Error("a resized image should carry the dimension note hint")
	}
}

// TestResizeInlineImagePassesThroughWithinLimits pins the early return: bytes
// inside every limit stay byte-identical.
func TestResizeInlineImagePassesThroughWithinLimits(t *testing.T) {
	data := solidPNG(t, 40, 30)
	resized := resizeInlineImage(data, "image/png", DefaultImageResizeOptions)
	if resized == nil {
		t.Fatal("an image within the limits should pass through")
	}
	if resized.WasResized {
		t.Error("WasResized = true for an image within the limits")
	}
	if resized.Data != base64.StdEncoding.EncodeToString(data) {
		t.Error("pass-through did not keep the bytes")
	}
	if note := FormatDimensionNote(resized); note != "" {
		t.Errorf("an unresized image should carry no dimension note, got %q", note)
	}
}

// TestResizeInlineImageShrinksToFitTheByteBudget covers the second half of the
// strategy: when the scaled first candidate is still too large, the loop reduces
// the dimensions until something fits.
func TestResizeInlineImageShrinksToFitTheByteBudget(t *testing.T) {
	options := DefaultImageResizeOptions
	options.MaxBytes = 20_000
	resized := resizeInlineImage(noisyPNG(t, 800, 600), "image/png", options)
	if resized == nil {
		t.Fatal("a noisy 800x600 image could not be fitted into 20kB")
	}
	if int64(len(resized.Data)) >= options.MaxBytes {
		t.Fatalf("result is %d bytes, not below the %d budget", len(resized.Data), options.MaxBytes)
	}
	if resized.Width > 800 || resized.Width == 800 && resized.Height == 600 {
		t.Errorf("dimensions %dx%d did not shrink", resized.Width, resized.Height)
	}
}

func TestResizeInlineImageRejectsUndecodableBytes(t *testing.T) {
	if resized := resizeInlineImage([]byte("not an image"), "image/png", DefaultImageResizeOptions); resized != nil {
		t.Fatalf("undecodable bytes produced %#v", resized)
	}
}
