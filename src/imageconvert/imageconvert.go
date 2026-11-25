package imageconvert

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/gen2brain/avif"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

type Options struct {
	Quality    int
	Background color.Color
}

var DefaultOptions = Options{
	Quality:    92,
	Background: color.White,
}

func ConvertFileToJPG(srcPath, dstPath string, opts ...Options) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	jpg, err := ConvertBytesToJPG(data, opts...)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}
	if err := os.WriteFile(dstPath, jpg, 0o644); err != nil {
		return fmt.Errorf("write destination: %w", err)
	}
	return nil
}

func ConvertBytesToJPG(data []byte, opts ...Options) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return EncodeToJPG(img, opts...)
}

func EncodeToJPG(img image.Image, opts ...Options) ([]byte, error) {
	if img == nil {
		return nil, fmt.Errorf("encode jpg: nil image")
	}
	options := DefaultOptions
	if len(opts) > 0 {
		if opts[0].Quality > 0 {
			options.Quality = opts[0].Quality
		}
		if opts[0].Background != nil {
			options.Background = opts[0].Background
		}
	}
	if options.Quality < 1 {
		options.Quality = DefaultOptions.Quality
	}
	if options.Quality > 100 {
		options.Quality = 100
	}

	rgba := image.NewRGBA(img.Bounds())
	bg := options.Background
	if bg == nil {
		bg = color.White
	}
	draw.Draw(rgba, rgba.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Over)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: options.Quality}); err != nil {
		return nil, fmt.Errorf("jpeg encode: %w", err)
	}
	return buf.Bytes(), nil
}

func IsSupportedExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".avifs", ".bmp", ".tif", ".tiff":
		return true
	default:
		return false
	}
}

func DecodeConfig(r io.Reader) (image.Config, string, error) {
	cfg, format, err := image.DecodeConfig(r)
	if err != nil {
		return image.Config{}, "", err
	}
	return cfg, format, nil
}
