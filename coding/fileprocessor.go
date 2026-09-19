package coding

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"

	"github.com/dat267/gpi/ai"
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
// D26: when an image needs resizing the Go port reports the upstream omission
// message, because upstream resizes with Photon (Rust/WASM) and the Go port has
// no resizer yet. Images that already fit within the limits pass through
// byte-identically, which is the common case.
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
func resizeInlineImage(data []byte, mimeType string, options ImageResizeOptions) *ResizedImage {
	inputBase64Size := int64(len(data)+2) / 3 * 4

	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	originalWidth, originalHeight := config.Width, config.Height

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
	return nil
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
				text.WriteString(fmt.Sprintf("<file name=%q>%s</file>\n", absolutePath, processed.Message))
				continue
			}
			images = append(images, ai.ImageContent{Data: processed.Data, MimeType: processed.MimeType})
			if len(processed.Hints) > 0 {
				text.WriteString(fmt.Sprintf("<file name=%q>%s</file>\n", absolutePath, strings.Join(processed.Hints, "\n")))
			} else {
				text.WriteString(fmt.Sprintf("<file name=%q></file>\n", absolutePath))
			}
			continue
		}

		content, err := os.ReadFile(absolutePath)
		if err != nil {
			return ProcessedFiles{}, fmt.Errorf("Error: Could not read file %s: %v", absolutePath, err)
		}
		text.WriteString(fmt.Sprintf("<file name=%q>\n%s\n</file>\n", absolutePath, StripBom(string(content))))
	}

	return ProcessedFiles{Text: text.String(), Images: images}, nil
}
