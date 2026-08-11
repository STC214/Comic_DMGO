//go:build windows

package main

import (
	"bytes"
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
		found = path
		return filepath.SkipDir
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
			if resolved := resolveDownloadedFilePath(item); resolved != "" {
				return resolved
			}
		}
	case []any:
		for _, item := range value {
			switch typed := item.(type) {
			case string:
				if resolved := resolveDownloadedFilePath(typed); resolved != "" {
					return resolved
				}
			case map[string]any:
				for _, key := range []string{"path", "file", "filename", "name"} {
					if raw, ok := typed[key]; ok {
						if resolved := resolveDownloadedFilePath(fmt.Sprint(raw)); resolved != "" {
							return resolved
						}
					}
				}
			}
		}
	default:
		if raw := fmt.Sprint(value); raw != "" && raw != "<nil>" {
			return resolveDownloadedFilePath(raw)
		}
	}
	return ""
}

func convertImageToThumbnailJPG(srcPath, dstPath string, maxWidth, maxHeight uint) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
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
		return "", fmt.Errorf("no downloaded file available for thumbnail")
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
