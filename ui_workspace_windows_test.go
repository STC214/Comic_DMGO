//go:build windows && !legacyui

package main

import (
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func TestWorkspaceLayout(t *testing.T) {
	app := test.NewApp()
	t.Cleanup(app.Quit)
	app.Settings().SetTheme(&fyneComicTheme{base: theme.DefaultTheme()})
	ui := &fyneUI{pages: make(map[string]fyne.CanvasObject)}
	ui.buildUI()
	if ui.urlEntry.OnSubmitted == nil || ui.addBtn.OnTapped == nil || ui.addBtn.Importance != widget.HighImportance {
		t.Fatal("the URL field and primary action must preserve their submission handlers")
	}
	min := ui.root.MinSize()
	for _, size := range []fyne.Size{fyne.NewSize(520, 520), fyne.NewSize(1380, 920)} {
		if min.Width > size.Width || min.Height > size.Height {
			t.Fatalf("workspace minimum %v exceeds test window %v", min, size)
		}
		ui.root.Resize(size)
		if ui.urlEntry.Size().Width < 200 || ui.taskList.Size().Height < 150 {
			t.Fatalf("window %v leaves too little room for the URL field %v or task list %v", size, ui.urlEntry.Size(), ui.taskList.Size())
		}
	}
	ui.statusLabel.SetText(strings.Repeat("下载进度 100% · ", 80))
	if got := ui.root.MinSize(); got != min {
		t.Fatalf("long status changed the workspace minimum from %v to %v", min, got)
	}
}

func TestWorkspaceCoverColumn(t *testing.T) {
	app := test.NewApp()
	t.Cleanup(app.Quit)
	ui := &fyneUI{
		rowTaskIDs: []int{1},
		snapshot:   AppState{Tasks: []*Task{{ID: 1, State: TaskQueued}}},
	}
	card := newTaskCardItem(ui)
	card.setRow(0)
	renderer := card.CreateRenderer()
	defer renderer.Destroy()
	renderer.Layout(fyne.NewSize(520, taskCardItemHeight))
	if card.thumbImage.Visible() || !card.thumbText.Visible() {
		t.Fatal("task without a cover should show its placeholder")
	}
	if got := card.thumbBg.Size().Width; got < 84 {
		t.Errorf("placeholder column width = %.0f, want at least 84", got)
	}
}

func TestWorkspacePreview(t *testing.T) {
	dir := os.Getenv("COMIC_UI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set COMIC_UI_PREVIEW_DIR to render the workspace")
	}
	app := test.NewApp()
	t.Cleanup(app.Quit)
	app.Settings().SetTheme(&fyneComicTheme{base: theme.DefaultTheme()})
	ui := &fyneUI{
		pages:           make(map[string]fyne.CanvasObject),
		rowTaskIDs:      []int{1, 2, 3},
		selectedTaskIDs: map[int]bool{1: true},
		snapshot: AppState{Tasks: []*Task{
			{ID: 1, Title: "示例任务：正在下载", State: TaskRunning, Percent: .66, SpeedText: "2.4 MB/s"},
			{ID: 2, Title: "示例任务：等待下载", State: TaskQueued},
			{ID: 3, Title: "示例任务：已完成", State: TaskDone, Percent: 1},
		}},
	}
	ui.buildUI()
	ui.statusLabel.SetText(strings.Repeat("示例状态：下载任务正在运行 · ", 6))
	ui.caption.Show() // Render the installed caption; the software driver has no HWND.
	window := test.NewWindow(ui.root)
	defer window.Close()
	for _, sample := range []struct {
		name string
		size fyne.Size
	}{
		{"workspace-compact.png", fyne.NewSize(600, 620)},
		{"workspace-wide.png", fyne.NewSize(1000, 740)},
	} {
		window.Resize(sample.size)
		img := window.Canvas().Capture()
		if img.Bounds().Dx() != int(sample.size.Width) || img.Bounds().Dy() != int(sample.size.Height) {
			t.Fatalf("preview %s has unexpected size %v", sample.name, img.Bounds())
		}
		path := filepath.Join(dir, sample.name)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, img)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("preview %s: encode=%v close=%v", path, err, closeErr)
		}
		t.Logf("rendered %s", path)
	}
}

func TestWorkspaceThemeContrast(t *testing.T) {
	palette := &fyneComicTheme{base: theme.DefaultTheme()}
	for _, pair := range [][2]fyne.ThemeColorName{
		{theme.ColorNameForegroundOnPrimary, theme.ColorNamePrimary},
		{theme.ColorNamePlaceHolder, theme.ColorNameInputBackground},
		{theme.ColorNameForeground, theme.ColorNameButton},
	} {
		a := workspaceLuminance(palette.Color(pair[0], theme.VariantDark))
		b := workspaceLuminance(palette.Color(pair[1], theme.VariantDark))
		if ratio := (math.Max(a, b) + .05) / (math.Min(a, b) + .05); ratio < 4.5 {
			t.Errorf("contrast for %v is %.2f, want at least 4.5", pair, ratio)
		}
	}
}

func workspaceLuminance(c color.Color) float64 {
	convert := func(component uint32) float64 {
		v := float64(component) / 65535
		if v <= .04045 {
			return v / 12.92
		}
		return math.Pow((v+.055)/1.055, 2.4)
	}
	r, g, b, _ := c.RGBA()
	return .2126*convert(r) + .7152*convert(g) + .0722*convert(b)
}
