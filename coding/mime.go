package coding

import (
	"os"
)

// Port of utils/mime.ts and the inline-image normalization in
// utils/image-process.ts / utils/image-resize-core.ts.

// imageTypeSniffBytes bounds the file sniff.
const imageTypeSniffBytes = 4100

var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// DetectSupportedImageMimeType sniffs an image MIME type from a byte prefix,
// returning "" for unsupported or undecodable content.
func DetectSupportedImageMimeType(buffer []byte) string {
	if startsWithBytes(buffer, []byte{0xff, 0xd8, 0xff}) {
		// 0xf7 marks JPEG 2000 codestreams, which are not JPEG images.
		if len(buffer) > 3 && buffer[3] == 0xf7 {
			return ""
		}
		return "image/jpeg"
	}
	if startsWithBytes(buffer, pngSignature) {
		if isPNG(buffer) && !isAnimatedPNG(buffer) {
			return "image/png"
		}
		return ""
	}
	if startsWithASCII(buffer, 0, "GIF") {
		return "image/gif"
	}
	if startsWithASCII(buffer, 0, "RIFF") && startsWithASCII(buffer, 8, "WEBP") {
		return "image/webp"
	}
	if startsWithASCII(buffer, 0, "BM") && isBMP(buffer) {
		return "image/bmp"
	}
	return ""
}

// DetectSupportedImageMimeTypeFromFile sniffs the first bytes of a file.
func DetectSupportedImageMimeTypeFromFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	buffer := make([]byte, imageTypeSniffBytes)
	read, err := file.Read(buffer)
	if err != nil && read == 0 {
		return "", err
	}
	return DetectSupportedImageMimeType(buffer[:read]), nil
}

func isPNG(buffer []byte) bool {
	return len(buffer) >= 16 && readUint32BE(buffer, len(pngSignature)) == 13 && startsWithASCII(buffer, 12, "IHDR")
}

// isAnimatedPNG walks the PNG chunks looking for an animation control chunk
// before the image data.
func isAnimatedPNG(buffer []byte) bool {
	offset := len(pngSignature)
	for offset+8 <= len(buffer) {
		chunkLength := int(readUint32BE(buffer, offset))
		chunkTypeOffset := offset + 4
		if startsWithASCII(buffer, chunkTypeOffset, "acTL") {
			return true
		}
		if startsWithASCII(buffer, chunkTypeOffset, "IDAT") {
			return false
		}
		nextOffset := offset + 8 + chunkLength + 4
		if nextOffset <= offset || nextOffset > len(buffer) {
			return false
		}
		offset = nextOffset
	}
	return false
}

// isBMP validates the BMP header structure upstream accepts.
func isBMP(buffer []byte) bool {
	if len(buffer) < 26 {
		return false
	}
	declaredFileSize := int(readUint32LE(buffer, 2))
	pixelDataOffset := int(readUint32LE(buffer, 10))
	dibHeaderSize := int(readUint32LE(buffer, 14))
	if declaredFileSize != 0 && declaredFileSize < 26 {
		return false
	}
	if pixelDataOffset < 14+dibHeaderSize {
		return false
	}
	if declaredFileSize != 0 && pixelDataOffset >= declaredFileSize {
		return false
	}

	var colorPlanes, bitsPerPixel int
	if dibHeaderSize == 12 {
		colorPlanes = readUint16LE(buffer, 22)
		bitsPerPixel = readUint16LE(buffer, 24)
	} else if dibHeaderSize >= 40 && dibHeaderSize <= 124 {
		if len(buffer) < 30 {
			return false
		}
		colorPlanes = readUint16LE(buffer, 26)
		bitsPerPixel = readUint16LE(buffer, 28)
	} else {
		return false
	}

	if colorPlanes != 1 {
		return false
	}
	switch bitsPerPixel {
	case 1, 4, 8, 16, 24, 32:
		return true
	default:
		return false
	}
}

func startsWithBytes(buffer, bytes []byte) bool {
	if len(buffer) < len(bytes) {
		return false
	}
	for index, byteValue := range bytes {
		if buffer[index] != byteValue {
			return false
		}
	}
	return true
}

func startsWithASCII(buffer []byte, offset int, text string) bool {
	if len(buffer) < offset+len(text) {
		return false
	}
	for index := 0; index < len(text); index++ {
		if buffer[offset+index] != text[index] {
			return false
		}
	}
	return true
}

func readUint16LE(buffer []byte, offset int) int {
	return int(byteAt(buffer, offset)) + int(byteAt(buffer, offset+1))<<8
}

func readUint32BE(buffer []byte, offset int) uint32 {
	return uint32(byteAt(buffer, offset))<<24 |
		uint32(byteAt(buffer, offset+1))<<16 |
		uint32(byteAt(buffer, offset+2))<<8 |
		uint32(byteAt(buffer, offset+3))
}

func readUint32LE(buffer []byte, offset int) uint32 {
	return uint32(byteAt(buffer, offset)) |
		uint32(byteAt(buffer, offset+1))<<8 |
		uint32(byteAt(buffer, offset+2))<<16 |
		uint32(byteAt(buffer, offset+3))<<24
}

func byteAt(buffer []byte, offset int) byte {
	if offset < 0 || offset >= len(buffer) {
		return 0
	}
	return buffer[offset]
}
