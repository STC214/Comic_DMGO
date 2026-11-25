//go:build windows && !legacyui

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThumbnailReviewGIFContainers(t *testing.T) {
	frame := image.NewPaletted(image.Rect(0, 0, 3, 3), color.Palette{color.Black, color.White})
	for _, count := range []int{1, 2, 500} {
		animation := &gif.GIF{}
		for i := 0; i < count; i++ {
			animation.Image = append(animation.Image, frame)
			animation.Delay = append(animation.Delay, 1)
		}
		var buf bytes.Buffer
		if err := gif.EncodeAll(&buf, animation); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "source.gif")
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(t.TempDir(), "thumb.jpg")
		err := convertImageToThumbnailJPG(path, dest, 240, 240)
		if (count == 1) != (err == nil) {
			t.Errorf("frames=%d err=%v", count, err)
		}
		if count > 1 {
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Error("animation wrote a thumbnail")
			}
		}
		if count == 1 {
			data := buf.Bytes()
			for cut := 0; cut < len(data); cut++ {
				if err := os.WriteFile(path, data[:cut], 0o644); err != nil {
					t.Fatal(err)
				}
				// Malformed inputs must return an error, not panic or allocate frames indefinitely.
				if _, err := decodeStaticThumbnailImage(path); err == nil {
					t.Errorf("truncated GIF accepted at byte %d", cut)
				}
			}
		}
	}
}

func TestThumbnailReviewNestedOrder(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", "b"} {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "0001.png"), testPNGBytes(t), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := filepath.Join(root, "a", "0001.png")
	if got := firstImageFileInDir(root); got != want {
		t.Fatalf("first nested source=%q, want %q", got, want)
	}
}

func TestThumbnailReviewTitleDelimiters(t *testing.T) {
	for _, title := range []string{
		"Sample | pages=spoof | output=hidden title",
		"Sample | output=hidden title | pages=spoof",
		"Sample | pages=7/9 | output=hidden title",
	} {
		detail := "done | 完成 | title=" + title + " | pages=58/58 | output=C:\\books"
		got := compactTaskDetail(detail)
		if strings.Contains(got, "spoof") || strings.Contains(got, "hidden") || strings.Contains(got, "7/9") || !strings.Contains(got, "pages=58/58") {
			t.Errorf("title=%q displayed=%q", title, got)
		}
	}
	// Only structured completion summaries should have their title field hidden.
	const diagnostic = "error | title=abc | retry"
	if got := compactTaskDetail(diagnostic); got != diagnostic {
		t.Errorf("diagnostic lost: %q", got)
	}
}

func FuzzThumbnailGIFContainer(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("GIF89a"))
	frame := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	for _, count := range []int{1, 2} {
		var buf bytes.Buffer
		animation := &gif.GIF{}
		for i := 0; i < count; i++ {
			animation.Image = append(animation.Image, frame)
			animation.Delay = append(animation.Delay, 1)
		}
		if err := gif.EncodeAll(&buf, animation); err != nil {
			f.Fatal(err)
		}
		f.Add(buf.Bytes())
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = thumbnailGIFIsAnimated(data)
	})
}
