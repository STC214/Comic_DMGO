package imageconvert

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func TestConvertBytesToJPG_WebP(t *testing.T) {
	data := mustReadTestFixture(t, "sample.webp")
	jpg, err := ConvertBytesToJPG(data)
	if err != nil {
		t.Fatalf("ConvertBytesToJPG(webp): %v", err)
	}
	assertJPEGOutput(t, jpg)
}

func TestConvertBytesToJPG_AVIF(t *testing.T) {
	data := mustReadTestFixture(t, "sample.avif")
	jpg, err := ConvertBytesToJPG(data)
	if err != nil {
		t.Fatalf("ConvertBytesToJPG(avif): %v", err)
	}
	assertJPEGOutput(t, jpg)
}

func TestEncodeToJPG_Transparent(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.NRGBA{R: 255, G: 0, B: 0, A: 128})
	src.Set(1, 1, color.NRGBA{R: 0, G: 255, B: 0, A: 255})

	jpg, err := EncodeToJPG(src)
	if err != nil {
		t.Fatalf("EncodeToJPG: %v", err)
	}
	assertJPEGOutput(t, jpg)
}

func TestConvertFileToJPG(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "sample.webp")
	dstPath := filepath.Join(dir, "sample.jpg")
	data := mustReadTestFixture(t, "sample.webp")
	if err := os.WriteFile(srcPath, data, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := ConvertFileToJPG(srcPath, dstPath); err != nil {
		t.Fatalf("ConvertFileToJPG: %v", err)
	}
	out, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	assertJPEGOutput(t, out)
}

func assertJPEGOutput(t *testing.T, data []byte) {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("decode output format = %q, want jpeg", format)
	}
	if img.Bounds().Dx() <= 0 || img.Bounds().Dy() <= 0 {
		t.Fatalf("decoded image has invalid bounds: %v", img.Bounds())
	}
}

func mustReadTestFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "tests", "fixtures", "imageconvert", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}
