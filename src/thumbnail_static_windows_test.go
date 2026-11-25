//go:build windows && !legacyui

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThumbnailFirstStatic(t *testing.T) {
	dir := t.TempDir()
	pal := color.Palette{color.Black, color.White}
	frame := image.NewPaletted(image.Rect(0, 0, 2, 2), pal)
	var animated bytes.Buffer
	if err := gif.EncodeAll(&animated, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{1, 1}}); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(dir, "0001.gif"), filepath.Join(dir, "0002.webp"), filepath.Join(dir, "0003.png"), filepath.Join(dir, "0004.png")}
	for i, data := range [][]byte{animated.Bytes(), []byte("broken"), testPNGBytes(t), testPNGBytes(t)} {
		if err := os.WriteFile(paths[i], data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := firstImageFileInDir(dir); got != paths[2] {
		t.Fatalf("directory chose %q, want %q", got, paths[2])
	}
	for _, files := range []any{
		paths,
		[]any{paths[0], paths[1], paths[2], paths[3]},
		[]any{map[string]any{"path": paths[0]}, map[string]any{"file": paths[1]}, map[string]any{"filename": paths[2]}},
	} {
		if got := firstDownloadedFilePath(map[string]any{"files": files}); got != paths[2] {
			t.Errorf("result chose %q, want %q", got, paths[2])
		}
	}
	if err := convertImageToThumbnailJPG(paths[0], filepath.Join(t.TempDir(), "animated.jpg"), 240, 240); err == nil {
		t.Error("animated GIF accepted as static thumbnail")
	}
	if got := firstDownloadedFilePath(map[string]any{"files": paths[0]}); got != "" {
		t.Errorf("single animation accepted: %q", got)
	}
	if got := firstImageFileInDir(t.TempDir()); got != "" {
		t.Errorf("empty directory=%q", got)
	}
}

func TestThumbnailCardHidesTitle(t *testing.T) {
	task := &Task{State: TaskDone, Percent: 1, Detail: "done | 完成 | title=[Sample] A | B | pages=58/58 | output=C:\\books"}
	before := task.Detail
	got := taskCardMetaText(task)
	if strings.Contains(got, "title=") || strings.Contains(got, "Sample") || strings.Contains(got, "A | B") {
		t.Fatalf("title leaked: %q", got)
	}
	if !strings.Contains(got, "pages=58/58") {
		t.Fatalf("page count lost: %q", got)
	}
	if task.Detail != before {
		t.Fatal("display formatting mutated task detail")
	}
	if got := compactTaskDetail("downloading images 2/5"); got != "下载图片" {
		t.Fatalf("running detail changed: %q", got)
	}
}

func TestThumbnailStaticFormats(t *testing.T) {
	dir := t.TempDir()
	png := testPNGBytes(t)
	chunk := make([]byte, 20)
	binary.BigEndian.PutUint32(chunk[:4], 8)
	copy(chunk[4:8], "acTL")
	binary.BigEndian.PutUint32(chunk[8:12], 2)
	binary.BigEndian.PutUint32(chunk[16:20], crc32.ChecksumIEEE(chunk[4:16]))
	apng := append(append(append([]byte{}, png[:33]...), chunk...), png[33:]...)
	webp := append([]byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00"), []byte{2, 0, 0, 0, 1, 0, 0, 1, 0, 0}...)
	var gifData bytes.Buffer
	if err := gif.Encode(&gifData, image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"animated.png": apng, "animated.webp": webp, "static.gif": gifData.Bytes(), "static.png": png} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		err := convertImageToThumbnailJPG(path, filepath.Join(t.TempDir(), "thumb.jpg"), 240, 240)
		if strings.HasPrefix(name, "animated") != (err != nil) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := firstDownloadedFilePath(map[string]any{"files": []string{filepath.Join(dir, "static.png"), filepath.Join(dir, "static.gif")}}); filepath.Base(got) != "static.png" {
		t.Errorf("result order lost: %q", got)
	}
	if _, err := buildTaskThumbnailFromResult(99999, "static-test", "empty", t.TempDir(), nil); err == nil {
		t.Error("empty task should report no static image")
	}
}

// Opt-in read-only regression against the preserved production downloads.
func TestThumbnailProductionStatic(t *testing.T) {
	path := os.Getenv("COMIC_THUMBNAIL_TEST_STATE")
	if path == "" {
		t.Skip("production input not requested")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Tasks []Task `json:"tasks"`
	}
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, task := range state.Tasks {
		if task.ID != 99 && task.ID != 100 {
			continue
		}
		checked++
		source := firstImageFileInDir(task.OutputDir)
		if filepath.Base(source) != "0002.webp" {
			t.Fatalf("task=%d source=%q want 0002.webp", task.ID, source)
		}
		dest := filepath.Join(t.TempDir(), "thumb.jpg")
		if root := os.Getenv("COMIC_THUMBNAIL_TEST_OUTPUT"); root != "" {
			dest = filepath.Join(root, fmt.Sprintf("task-%d.jpg", task.ID))
		}
		if err := convertImageToThumbnailJPG(source, dest, 240, 240); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(dest)
		if err != nil {
			t.Fatal(err)
		}
		cfg, format, err := image.DecodeConfig(f)
		f.Close()
		if err != nil || format != "jpeg" || cfg.Width > 240 || cfg.Height > 240 {
			t.Fatalf("invalid thumbnail: %+v %s %v", cfg, format, err)
		}
		t.Logf("task=%d source=%s thumbnail=%dx%d jpeg OK", task.ID, filepath.Base(source), cfg.Width, cfg.Height)
	}
	if checked != 2 {
		t.Fatalf("checked %d production tasks, want 2", checked)
	}
}
