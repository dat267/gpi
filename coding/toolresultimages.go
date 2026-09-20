package coding

import (
	"encoding/base64"

	"github.com/dat267/gpi/ai"
)

// Port of utils/tool-result-images.ts: normalize image blocks returned by tool
// results as they enter session history.
//
// The `read` tool and `@file` CLI attachments run their images through
// ProcessImage, but tools that produce images themselves (extensions, MCP
// bridges, screenshot tools) hand back arbitrary base64 payloads that go
// straight into session history and every subsequent provider request.
// Oversized images make the provider reject the whole conversation, not just
// the offending turn, so normalize them once on arrival.
//
// D26: upstream resizes with Photon; the Go image pipeline reports the upstream
// omission message when an image needs resizing, and NormalizedToolResultImages
// keeps the original block in that case so a tool's output is never silently
// deleted (matching upstream's processImage-failure handling).

// NormalizeToolResultImagesOptions configure the normalization.
type NormalizeToolResultImagesOptions struct {
	// AutoResizeImages resizes oversized images to the inline provider limits
	// (default true).
	AutoResizeImages *bool
}

// NormalizeToolResultImages normalizes image blocks in a tool result's content.
// The original slice is returned when nothing changed so callers can skip
// rewriting the result.
func NormalizeToolResultImages(content ai.UserContentList, options *NormalizeToolResultImagesOptions) (ai.UserContentList, bool) {
	hasImage := false
	for _, block := range content {
		if _, ok := block.(ai.ImageContent); ok {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return content, false
	}

	autoResize := true
	if options != nil && options.AutoResizeImages != nil {
		autoResize = *options.AutoResizeImages
	}

	normalized := make(ai.UserContentList, 0, len(content))
	changed := false
	for _, block := range content {
		image, ok := block.(ai.ImageContent)
		if !ok {
			normalized = append(normalized, block)
			continue
		}
		data, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil {
			// Not decodable base64: keep the original block.
			normalized = append(normalized, block)
			continue
		}
		processed := ProcessImage(data, image.MimeType, &ProcessImageOptions{AutoResizeImages: &autoResize})
		if !processed.OK {
			// Unlike `read`, keep the original block: the tool already produced
			// this image and the failure may just be an unavailable image
			// backend, so passing it through preserves the behavior tools have
			// today instead of silently deleting their output.
			normalized = append(normalized, block)
			continue
		}
		if processed.Data == image.Data && processed.MimeType == image.MimeType && len(processed.Hints) == 0 {
			normalized = append(normalized, block)
			continue
		}
		normalized = append(normalized, ai.ImageContent{Data: processed.Data, MimeType: processed.MimeType})
		if len(processed.Hints) > 0 {
			hints := ""
			for index, hint := range processed.Hints {
				if index > 0 {
					hints += "\n"
				}
				hints += hint
			}
			normalized = append(normalized, ai.TextContent{Text: hints})
		}
		changed = true
	}
	if !changed {
		return content, false
	}
	return normalized, true
}
