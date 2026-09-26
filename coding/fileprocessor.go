package coding

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"strings"

	"github.com/dat267/pier/ai"
)

// Port of cli/file-processor.ts and the resizable-image contract in
// utils/image-resize-core.ts.

// ImageResizeOptions bound an inline image.
type ImageResizeOptions struct {
	MaxWidth    int
	MaxHeight   int
	MaxBytes    int64
	JPEGQuality int
}

// DefaultImageResizeOptions are the upstream resize defaults.
var DefaultImageResizeOptions = ImageResizeOptions{
	MaxWidth:    2000,
	MaxHeight:   2000,
	MaxBytes:    4_500_000, // 4.5MB of base64 payload, below Anthropic's 5MB limit
	JPEGQuality: 80,
}

// ResizedImage is one prepared inline image.
type ResizedImage struct {
	Data           string // base64
	MimeType       string
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
	WasResized     bool
}

// FormatDimensionNote describes a resized image (upstream formatDimensionNote).
func FormatDimensionNote(result *ResizedImage) string {
	if result == nil || !result.WasResized {
		return ""
	}
	scale := float64(result.OriginalWidth) / float64(result.Width)
	return fmt.Sprintf("[Image: original %dx%d, displayed at %dx%d. Multiply coordinates by %.2f to map to original image.]",
		result.OriginalWidth, result.OriginalHeight, result.Width, result.Height, scale)
}

// ProcessImageOptions configure ProcessImage.
type ProcessImageOptions struct {
	// AutoResizeImages defaults to true.
	AutoResizeImages *bool
	ResizeOptions    *ImageResizeOptions
}

// ProcessImageResult is either a prepared image or an omission message.
type ProcessImageResult struct {
	OK       bool
	Data     string
	MimeType string
	Hints    []string
	Message  string
}

// ConvertImageBytesToPNG converts an unsupported image format to PNG.
//
// D26: upstream converts through Photon (Rust/WASM), which also handles formats
// the Go standard library cannot decode (notably BMP). The Go port decodes what
// image/png, image/jpeg, and image/gif provide and reports failure otherwise.
type imageBytesConverter func(bytes []byte) ([]byte, bool)

var convertImageBytesToPNG imageBytesConverter = func(bytes []byte) ([]byte, bool) {
	return nil, false
}

// ProcessImage normalizes and (optionally) resizes one inline image
// (upstream processImage).
//
// D26: oversize images are resized with a pure-Go bilinear resampler rather than
// Photon's Lanczos3 (Rust/WASM, which the port does not have), and re-encoding
// produces PNG or JPEG. Formats the standard library cannot decode at all —
// webp, and bmp outside the sniffing path — still report the upstream omission
// message, since converting them also goes through Photon. Images that already
// fit within the limits pass through byte-identically, which is the common case.
func ProcessImage(bytes []byte, mimeType string, options *ProcessImageOptions) ProcessImageResult {
	autoResize := true
	if options != nil && options.AutoResizeImages != nil {
		autoResize = *options.AutoResizeImages
	}
	resizeOptions := DefaultImageResizeOptions
	if options != nil && options.ResizeOptions != nil {
		resizeOptions = *options.ResizeOptions
	}

	normalizedBytes, normalizedMime, convertedFrom, ok := normalizeImage(bytes, mimeType)
	if !ok {
		return ProcessImageResult{Message: "[Image omitted: could not be converted to a supported inline image format.]"}
	}

	if autoResize {
		resized := resizeInlineImage(normalizedBytes, normalizedMime, resizeOptions)
		if resized == nil {
			return ProcessImageResult{Message: "[Image omitted: could not be resized below the inline image size limit.]"}
		}
		var hints []string
		if hint := conversionHint(convertedFrom, resized.MimeType); hint != "" {
			hints = append(hints, hint)
		}
		if note := FormatDimensionNote(resized); note != "" {
			hints = append(hints, note)
		}
		return ProcessImageResult{OK: true, Data: resized.Data, MimeType: resized.MimeType, Hints: hints}
	}

	var hints []string
	if hint := conversionHint(convertedFrom, normalizedMime); hint != "" {
		hints = append(hints, hint)
	}
	return ProcessImageResult{
		OK:       true,
		Data:     base64.StdEncoding.EncodeToString(normalizedBytes),
		MimeType: normalizedMime,
		Hints:    hints,
	}
}

func baseMimeType(mimeType string) string {
	if index := strings.Index(mimeType, ";"); index != -1 {
		mimeType = mimeType[:index]
	}
	return strings.ToLower(strings.TrimSpace(mimeType))
}

func normalizeSupportedImageMimeType(mimeType string) string {
	switch baseMimeType(mimeType) {
	case "image/png":
		return "image/png"
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/gif":
		return "image/gif"
	case "image/webp":
		return "image/webp"
	default:
		return ""
	}
}

func normalizeImage(bytes []byte, mimeType string) ([]byte, string, string, bool) {
	if normalized := normalizeSupportedImageMimeType(mimeType); normalized != "" {
		return bytes, normalized, "", true
	}
	pngBytes, ok := convertImageBytesToPNG(bytes)
	if !ok {
		return nil, "", "", false
	}
	return pngBytes, "image/png", baseMimeType(mimeType), true
}

func conversionHint(from, to string) string {
	if from == "" || from == to {
		return ""
	}
	return fmt.Sprintf("[Image converted from %s to %s.]", from, to)
}

// resizeInlineImage returns the encoded image, or nil when it cannot be fitted.
//
// The strategy is upstream resizeImageInProcess's: fit to the max dimensions,
// prefer the first candidate below maxBytes in the order PNG-then-JPEG, and when
// nothing fits reduce both dimensions by 0.75 until 1x1. The resampling is
// bilinear rather than upstream's Lanczos3 (D26: Photon is Rust/WASM and the port
// has no equivalent), and re-encoding always produces PNG or JPEG, as upstream's
// candidate list does.
func resizeInlineImage(data []byte, mimeType string, options ImageResizeOptions) *ResizedImage {
	inputBase64Size := int64(len(data)+2) / 3 * 4

	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	bounds := source.Bounds()
	originalWidth, originalHeight := bounds.Dx(), bounds.Dy()

	// Already within every limit: pass the original bytes through, exactly like
	// upstream's early return.
	if originalWidth <= options.MaxWidth && originalHeight <= options.MaxHeight && inputBase64Size < options.MaxBytes {
		return &ResizedImage{
			Data:           base64.StdEncoding.EncodeToString(data),
			MimeType:       mimeType,
			OriginalWidth:  originalWidth,
			OriginalHeight: originalHeight,
			Width:          originalWidth,
			Height:         originalHeight,
			WasResized:     false,
		}
	}

	width, height := inlineImageTargetSize(originalWidth, originalHeight, options)
	for {
		scaled := scaleImageBilinear(source, width, height)
		for _, candidate := range encodeImageCandidates(scaled, options.JPEGQuality) {
			if candidate.EncodedSize < options.MaxBytes {
				return &ResizedImage{
					Data:           candidate.Data,
					MimeType:       candidate.MimeType,
					OriginalWidth:  originalWidth,
					OriginalHeight: originalHeight,
					Width:          width,
					Height:         height,
					WasResized:     true,
				}
			}
		}
		if width == 1 && height == 1 {
			break
		}
		nextWidth := 1
		if width != 1 {
			nextWidth = max(1, int(math.Floor(float64(width)*0.75)))
		}
		nextHeight := 1
		if height != 1 {
			nextHeight = max(1, int(math.Floor(float64(height)*0.75)))
		}
		if nextWidth == width && nextHeight == height {
			break
		}
		width, height = nextWidth, nextHeight
	}
	return nil
}

// inlineImageTargetSize fits the image inside the max dimensions, keeping the
// aspect ratio with upstream's rounding.
func inlineImageTargetSize(width, height int, options ImageResizeOptions) (int, int) {
	if width > options.MaxWidth {
		height = int(math.Round(float64(height) * float64(options.MaxWidth) / float64(width)))
		width = options.MaxWidth
	}
	if height > options.MaxHeight {
		width = int(math.Round(float64(width) * float64(options.MaxHeight) / float64(height)))
		height = options.MaxHeight
	}
	return width, height
}

// jpegQualitySteps is upstream's candidate list: the configured quality first,
// then the falling steps, with duplicates dropped in order.
func jpegQualitySteps(configured int) []int {
	steps := make([]int, 0, 5)
	seen := map[int]bool{}
	for _, quality := range []int{configured, 85, 70, 55, 40} {
		if seen[quality] {
			continue
		}
		seen[quality] = true
		steps = append(steps, quality)
	}
	return steps
}

// encodedImageCandidate is one encoded form of the scaled image.
type encodedImageCandidate struct {
	Data        string
	EncodedSize int64
	MimeType    string
}

func newEncodedImageCandidate(data []byte, mimeType string) encodedImageCandidate {
	encoded := base64.StdEncoding.EncodeToString(data)
	return encodedImageCandidate{Data: encoded, EncodedSize: int64(len(encoded)), MimeType: mimeType}
}

// encodeImageCandidates encodes the scaled image the way upstream tries formats:
// PNG first, then JPEG at each quality step, so the first candidate below the
// byte budget wins.
func encodeImageCandidates(img image.Image, jpegQuality int) []encodedImageCandidate {
	candidates := make([]encodedImageCandidate, 0, 6)
	var pngBuffer bytes.Buffer
	if err := png.Encode(&pngBuffer, img); err == nil {
		candidates = append(candidates, newEncodedImageCandidate(pngBuffer.Bytes(), "image/png"))
	}
	for _, quality := range jpegQualitySteps(jpegQuality) {
		var jpegBuffer bytes.Buffer
		if err := jpeg.Encode(&jpegBuffer, img, &jpeg.Options{Quality: quality}); err == nil {
			candidates = append(candidates, newEncodedImageCandidate(jpegBuffer.Bytes(), "image/jpeg"))
		}
	}
	return candidates
}

// scaleImageBilinear resamples to width x height with bilinear interpolation.
// Channel values stay premultiplied, which is what image.RGBA stores, so
// translucent pixels do not bleed toward black at the edges.
func scaleImageBilinear(source image.Image, width, height int) *image.RGBA {
	bounds := source.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return destination
	}
	scaleX := float64(sourceWidth) / float64(width)
	scaleY := float64(sourceHeight) / float64(height)
	for y := 0; y < height; y++ {
		sourceY := (float64(y)+0.5)*scaleY - 0.5
		y0 := int(math.Floor(sourceY))
		weightY := sourceY - float64(y0)
		if y0 < 0 {
			y0, weightY = 0, 0
		}
		y1 := min(y0+1, sourceHeight-1)
		for x := 0; x < width; x++ {
			sourceX := (float64(x)+0.5)*scaleX - 0.5
			x0 := int(math.Floor(sourceX))
			weightX := sourceX - float64(x0)
			if x0 < 0 {
				x0, weightX = 0, 0
			}
			x1 := min(x0+1, sourceWidth-1)
			r00, g00, b00, a00 := source.At(bounds.Min.X+x0, bounds.Min.Y+y0).RGBA()
			r10, g10, b10, a10 := source.At(bounds.Min.X+x1, bounds.Min.Y+y0).RGBA()
			r01, g01, b01, a01 := source.At(bounds.Min.X+x0, bounds.Min.Y+y1).RGBA()
			r11, g11, b11, a11 := source.At(bounds.Min.X+x1, bounds.Min.Y+y1).RGBA()
			blend := func(v00, v10, v01, v11 uint32) uint32 {
				top := float64(v00) + (float64(v10)-float64(v00))*weightX
				bottom := float64(v01) + (float64(v11)-float64(v01))*weightX
				return uint32(math.Round(top + (bottom-top)*weightY))
			}
			offset := destination.PixOffset(x, y)
			// RGBA() returns 16-bit premultiplied values; RGBA stores 8-bit.
			destination.Pix[offset+0] = uint8(blend(r00, r10, r01, r11) >> 8)
			destination.Pix[offset+1] = uint8(blend(g00, g10, g01, g11) >> 8)
			destination.Pix[offset+2] = uint8(blend(b00, b10, b01, b11) >> 8)
			destination.Pix[offset+3] = uint8(blend(a00, a10, a01, a11) >> 8)
		}
	}
	return destination
}

// ProcessedFiles is the outcome of expanding @file arguments.
type ProcessedFiles struct {
	Text   string
	Images []ai.ImageContent
}

// ProcessFileOptions configure ProcessFileArguments.
type ProcessFileOptions struct {
	// AutoResizeImages defaults to true.
	AutoResizeImages *bool
	// Cwd is the base directory for relative @file paths (default: process cwd).
	Cwd string
}

// ProcessFileArguments expands @file arguments into prompt text and image
// attachments (upstream processFileArguments). Missing files and unreadable
// files are errors; empty files are skipped.
func ProcessFileArguments(fileArgs []string, options *ProcessFileOptions) (ProcessedFiles, error) {
	autoResize := true
	cwd := ""
	if options != nil {
		if options.AutoResizeImages != nil {
			autoResize = *options.AutoResizeImages
		}
		cwd = options.Cwd
	}
	if cwd == "" {
		if workingDir, err := os.Getwd(); err == nil {
			cwd = workingDir
		}
	}

	var text strings.Builder
	var images []ai.ImageContent

	for _, fileArg := range fileArgs {
		absolutePath := ResolvePath(ResolveReadPath(fileArg, cwd), ".", PathInputOptions{})

		info, err := os.Stat(absolutePath)
		if err != nil {
			return ProcessedFiles{}, fmt.Errorf("Error: File not found: %s", absolutePath)
		}
		if info.Size() == 0 {
			continue
		}

		mimeType, err := DetectSupportedImageMimeTypeFromFile(absolutePath)
		if err != nil {
			return ProcessedFiles{}, fmt.Errorf("Error: Could not read file %s: %v", absolutePath, err)
		}

		if mimeType != "" {
			content, err := os.ReadFile(absolutePath)
			if err != nil {
				return ProcessedFiles{}, fmt.Errorf("Error: Could not read file %s: %v", absolutePath, err)
			}
			autoResizeFlag := autoResize
			processed := ProcessImage(content, mimeType, &ProcessImageOptions{AutoResizeImages: &autoResizeFlag})
			if !processed.OK {
				text.WriteString(fmt.Sprintf("<file name=\"%s\">%s</file>\n", absolutePath, processed.Message))
				continue
			}
			images = append(images, ai.ImageContent{Data: processed.Data, MimeType: processed.MimeType})
			if len(processed.Hints) > 0 {
				text.WriteString(fmt.Sprintf("<file name=\"%s\">%s</file>\n", absolutePath, strings.Join(processed.Hints, "\n")))
			} else {
				text.WriteString(fmt.Sprintf("<file name=\"%s\"></file>\n", absolutePath))
			}
			continue
		}

		content, err := os.ReadFile(absolutePath)
		if err != nil {
			return ProcessedFiles{}, fmt.Errorf("Error: Could not read file %s: %v", absolutePath, err)
		}
		text.WriteString(fmt.Sprintf("<file name=\"%s\">\n%s\n</file>\n", absolutePath, StripBom(string(content))))
	}

	return ProcessedFiles{Text: text.String(), Images: images}, nil
}
