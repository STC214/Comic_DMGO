//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"comic_downloader/imageconvert"
	"github.com/nfnt/resize"
)

func sanitizePathSegment(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	text = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`).ReplaceAllString(text, "_")
	text = strings.Join(strings.Fields(text), " ")
	text = strings.Trim(text, " ._")
	if len([]rune(text)) > 96 {
		text = string([]rune(text)[:96])
	}
	return text
}

func resolveDownloadedFilePath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	candidates := []string{raw}
	if !filepath.IsAbs(raw) {
		candidates = append(candidates, filepath.Join(projectRootDir(), raw))
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func firstImageFileInDir(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return ""
	}
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !imageconvert.IsSupportedExtension(path) {
			return nil
		}
		if _, err := decodeStaticThumbnailImage(path); err != nil {
			return nil
		}
		found = path
		return filepath.SkipAll
	})
	return found
}

func firstDownloadedFilePath(result map[string]any) string {
	files, ok := result["files"]
	if !ok || files == nil {
		return ""
	}
	switch value := files.(type) {
	case []string:
		for _, item := range value {
			if resolved := resolveStaticThumbnailPath(item); resolved != "" {
				return resolved
			}
		}
	case []any:
		for _, item := range value {
			switch typed := item.(type) {
			case string:
				if resolved := resolveStaticThumbnailPath(typed); resolved != "" {
					return resolved
				}
			case map[string]any:
				for _, key := range []string{"path", "file", "filename", "name"} {
					if raw, ok := typed[key]; ok {
						if resolved := resolveStaticThumbnailPath(fmt.Sprint(raw)); resolved != "" {
							return resolved
						}
					}
				}
			}
		}
	default:
		if raw := fmt.Sprint(value); raw != "" && raw != "<nil>" {
			return resolveStaticThumbnailPath(raw)
		}
	}
	return ""
}

func resolveStaticThumbnailPath(raw string) string {
	path := resolveDownloadedFilePath(raw)
	if path == "" {
		return ""
	}
	if _, err := decodeStaticThumbnailImage(path); err != nil {
		return ""
	}
	return path
}

func decodeStaticThumbnailImage(srcPath string) (image.Image, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, err
	}
	// DecodeConfig accepts animated WebP, while Decode does not handle ANMF.
	if len(data) >= 30 && string(data[:4]) == "RIFF" && string(data[8:16]) == "WEBPVP8X" && data[20]&2 != 0 {
		return nil, fmt.Errorf("animated WebP skipped")
	}
	if bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")) {
		animated, err := thumbnailGIFIsAnimated(data)
		if err != nil {
			return nil, err
		}
		if animated {
			return nil, fmt.Errorf("animated GIF skipped")
		}
	}
	// APNG is otherwise decoded as a static PNG by image.Decode.
	if bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		for pos := 8; pos+12 <= len(data); {
			if string(data[pos+4:pos+8]) == "acTL" {
				return nil, fmt.Errorf("animated PNG skipped")
			}
			n := int(binary.BigEndian.Uint32(data[pos : pos+4]))
			if n > len(data)-pos-12 {
				break
			}
			pos += n + 12
		}
	}
	if len(data) >= 16 && string(data[4:8]) == "ftyp" {
		end := int(binary.BigEndian.Uint32(data[:4]))
		if end > len(data) {
			end = len(data)
		}
		for pos := 8; pos+4 <= end; pos += 4 {
			if pos != 12 && string(data[pos:pos+4]) == "avis" {
				return nil, fmt.Errorf("animated AVIF skipped")
			}
		}
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// Count image descriptors without decompressing every frame of an animation.
// Static GIFs still go through image.Decode for full pixel validation below.
func thumbnailGIFIsAnimated(data []byte) (bool, error) {
	invalid := fmt.Errorf("invalid GIF container")
	if len(data) < 13 {
		return false, invalid
	}
	pos := 13
	skipTable := func(flags byte) bool {
		if flags&0x80 != 0 {
			length := 3 << ((flags & 7) + 1)
			if length > len(data)-pos {
				return false
			}
			pos += length
		}
		return true
	}
	if !skipTable(data[10]) {
		return false, invalid
	}
	frames := 0
	for pos < len(data) {
		kind := data[pos]
		pos++
		switch kind {
		case 0x3b: // Trailer.
			if frames == 1 {
				return false, nil
			}
			return false, invalid
		case 0x21: // Extension label, followed by data sub-blocks.
			if pos >= len(data) {
				return false, invalid
			}
			pos++
		case 0x2c: // Image descriptor, optional table, then LZW code size.
			if len(data)-pos < 9 {
				return false, invalid
			}
			flags := data[pos+8]
			pos += 9
			if !skipTable(flags) || pos >= len(data) {
				return false, invalid
			}
			pos++
			frames++
			if frames > 1 {
				return true, nil
			}
		default:
			return false, invalid
		}
		for {
			if pos >= len(data) {
				return false, invalid
			}
			length := int(data[pos])
			pos++
			if length > len(data)-pos {
				return false, invalid
			}
			pos += length
			if length == 0 {
				break
			}
		}
	}
	return false, invalid
}

func convertImageToThumbnailJPG(srcPath, dstPath string, maxWidth, maxHeight uint) error {
	img, err := decodeStaticThumbnailImage(srcPath)
	if err != nil {
		return err
	}
	thumb := resize.Thumbnail(maxWidth, maxHeight, img, resize.Lanczos3)
	jpg, err := imageconvert.EncodeToJPG(thumb, imageconvert.Options{Quality: 88})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dstPath, jpg, 0o644)
}

func buildTaskThumbnailFromResult(taskID int, workerName, title, outputDir string, result map[string]any) (string, error) {
	sourceFile := firstDownloadedFilePath(result)
	if sourceFile == "" {
		sourceFile = firstImageFileInDir(outputDir)
	}
	if sourceFile == "" {
		return "", fmt.Errorf("no decodable static image available for thumbnail")
	}

	thumbRoot := filepath.Join(projectRootDir(), "runtime", "thumb", sanitizePathSegment(workerName))
	thumbName := sanitizePathSegment(fmt.Sprintf("task-%d", taskID))
	if thumbName == "" {
		thumbName = sanitizePathSegment(title)
		if thumbName == "" {
			thumbName = sanitizePathSegment(filepath.Base(strings.TrimSpace(outputDir)))
		}
		if thumbName == "" {
			thumbName = sanitizePathSegment(workerName)
		}
		if thumbName == "" {
			thumbName = "thumbnail"
		}
	}
	thumbPath := filepath.Join(thumbRoot, thumbName+".jpg")
	if err := convertImageToThumbnailJPG(sourceFile, thumbPath, 240, 240); err != nil {
		return "", err
	}
	return thumbPath, nil
}
