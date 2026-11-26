//go:build windows && !legacyui

package main

import (
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

func TestCaptionLayout(t *testing.T) {
	app := test.NewApp()
	t.Cleanup(app.Quit)
	ui := &fyneUI{pages: make(map[string]fyne.CanvasObject)}
	ui.buildUI()
	badgeCount, titleCount := 0, 0
	var walk func(fyne.CanvasObject)
	walk = func(obj fyne.CanvasObject) {
		switch element := obj.(type) {
		case *fyne.Container:
			for _, child := range element.Objects {
				walk(child)
			}
		case *widget.Label:
			if element.Text == fyneWindowTitle {
				titleCount++
			}
		case *canvas.Image:
			if element.Resource != nil && element.Resource.Name() == "go-title-mark.svg" {
				badgeCount++
			}
		}
	}
	walk(ui.root)
	if titleCount != 1 || badgeCount != 1 {
		t.Fatalf("workspace has %d title labels and %d GO badges; want one of each", titleCount, badgeCount)
	}
	if ui.caption.Visible() {
		t.Fatal("fallback must not duplicate the native title")
	}
	minWithoutCaption := ui.root.MinSize()
	ui.caption.Show()
	if got := ui.root.MinSize().Height; got <= minWithoutCaption.Height {
		t.Fatal("the custom caption did not reserve space above the settings")
	}
}

func TestCaptionButtonsArePainted(t *testing.T) {
	app := test.NewApp()
	t.Cleanup(app.Quit)
	app.Settings().SetTheme(&fyneComicTheme{base: theme.DarkTheme()})
	ui := &fyneUI{pages: make(map[string]fyne.CanvasObject)}
	ui.buildUI()
	ui.caption.Show()
	window := test.NewWindow(ui.root)
	defer window.Close()
	for _, width := range []int{520, 1000} {
		window.Resize(fyne.NewSize(float32(width), 520))
		for _, icon := range []fyne.Resource{theme.WindowMaximizeIcon(), captionRestoreIcon} {
			ui.captionMax.SetIcon(icon)
			frame := window.Canvas().Capture()
			for i := 0; i < 3; i++ {
				mid := width - (3-i)*captionButtonWidth + captionButtonWidth/2
				bright := 0
				for y := 11; y < 30; y++ {
					for x := mid - 9; x <= mid+9; x++ {
						r, g, b, _ := frame.At(x, y).RGBA()
						if r > 0xa000 && g > 0xa000 && b > 0xa000 {
							bright++
						}
					}
				}
				if bright < 8 {
					t.Errorf("width=%d maximize=%s control=%d: only %d visible glyph pixels", width, icon.Name(), i, bright)
				}
			}
		}
	}
}

func TestCaptionHitZones(t *testing.T) {
	client := win.RECT{Right: 640, Bottom: 480}
	for _, tc := range []struct {
		x, y int32
		want uintptr
	}{
		{0, 0, win.HTTOPLEFT}, {639, 0, win.HTTOPRIGHT},
		{0, 479, win.HTBOTTOMLEFT}, {639, 479, win.HTBOTTOMRIGHT},
		{0, 100, win.HTLEFT}, {639, 100, win.HTRIGHT},
		{100, 0, win.HTTOP}, {100, 479, win.HTBOTTOM},
		{9, 13, win.HTCLIENT}, {9, 12, win.HTTOP}, {631, 100, win.HTRIGHT},
	} {
		if got := captionResizeEdge(win.POINT{X: tc.x, Y: tc.y}, client, 9, 13); got != tc.want {
			t.Errorf("(%d,%d): hit=%d want=%d", tc.x, tc.y, got, tc.want)
		}
	}
}

func TestCaptionResizeDoesNotPreserveStalePixels(t *testing.T) {
	for _, tc := range []struct {
		flags   uint32
		resized bool
		want    uint32
	}{
		{0, true, win.SWP_NOCOPYBITS},
		{0, false, 0}, // moving with unchanged dimensions but no SWP_NOSIZE
		{win.SWP_NOMOVE, true, win.SWP_NOMOVE | win.SWP_NOCOPYBITS},
		{win.SWP_NOSIZE, true, win.SWP_NOSIZE},
		{win.SWP_NOSIZE | win.SWP_FRAMECHANGED, false, win.SWP_NOSIZE | win.SWP_FRAMECHANGED | win.SWP_NOCOPYBITS},
	} {
		if got := captionResizeFlags(tc.flags, tc.resized); got != tc.want {
			t.Errorf("flags %#x resized=%v = %#x, want %#x", tc.flags, tc.resized, got, tc.want)
		}
	}
}

func TestCaptionStartupFitsWorkArea(t *testing.T) {
	work := win.RECT{Left: -1600, Top: 48, Right: 0, Bottom: 900}
	oversized := win.RECT{Left: -2400, Top: -290, Right: 300, Bottom: 1200}
	if got := fitStartupRect(oversized, work, persistedWindowPlacement{}); got != work {
		t.Errorf("oversized bounds = %+v, want %+v", got, work)
	}
	saved := persistedWindowPlacement{Valid: true, NormalPosition: persistedRect{Left: -400, Top: -100, Right: 500, Bottom: 640}}
	want := win.RECT{Left: -1250, Top: 104, Right: -350, Bottom: 844}
	if got := fitStartupRect(oversized, work, saved); got != want {
		t.Errorf("saved physical dimensions = %+v, want %+v", got, want)
	}
	visible := win.RECT{Left: -1300, Top: 100, Right: -650, Bottom: 740}
	if got := fitStartupRect(visible, work, persistedWindowPlacement{}); got != visible {
		t.Errorf("already-visible bounds moved: %+v", got)
	}
}

type captionTestCanvas struct {
	fyne.Canvas
	scale float32
}

func (c captionTestCanvas) Scale() float32 { return c.scale }

func TestCaptionUsesFyneScale(t *testing.T) {
	const fakeWindow win.HWND = 1 // no Win32 calls for a valid canvas scale
	defer delete(windowCaptions, fakeWindow)
	for _, scale := range []float32{0.75, 1, 1.25, 1.5, 2} {
		windowCaptions[fakeWindow] = &windowCaptionState{canvas: captionTestCanvas{scale: scale}}
		if got := windowCaptionHeight(fakeWindow); got != int32(captionHeight*scale) {
			t.Errorf("scale=%g paints a different caption height: %d", scale, got)
		}
	}
}

// Exercise the actual Win32/DWM frame of a disposable copy, without loading
// production settings, tasks, or browser data.
func TestCaptionNativeWindow(t *testing.T) {
	binary := os.Getenv("COMIC_CAPTION_SMOKE_EXE")
	if binary == "" {
		t.Skip("set COMIC_CAPTION_SMOKE_EXE to exercise the Windows release")
	}
	scale, err := strconv.ParseFloat(os.Getenv("FYNE_SCALE"), 64)
	if err != nil || scale <= 0 {
		scale = 1
		t.Setenv("FYNE_SCALE", "1")
	}
	app, root, finish := startCaptionSmoke(t, binary)
	defer finish()
	hwnd := findCaptionSmokeWindow(t, app, root, "native window caption enabled")
	// Cross a paint/layout cycle so the GLFW canvas has applied FYNE_SCALE.
	time.Sleep(300 * time.Millisecond)
	height := int32(math.Ceil(captionHeight * scale))
	assertStartupWorkArea(t, hwnd)
	assertNativeCaption(t, hwnd, height, scale)
	assertRapidCaptionMove(t, hwnd)
	assertRapidCaptionResize(t, hwnd, scale)
	verifyNativeCaptionCapture(t, root, height, scale, "normal")
	clickCaptionButton(t, hwnd, 1, height, scale)
	waitForCaption(t, "maximize by painted button", func() bool { return win.IsZoomed(hwnd) })
	assertNativeCaption(t, hwnd, height, scale)
	verifyNativeCaptionCapture(t, root, height, scale, "maximized")
	clickCaptionButton(t, hwnd, 1, height, scale)
	waitForCaption(t, "restore by painted button", func() bool { return !win.IsZoomed(hwnd) })
	var origin win.POINT
	if !win.ClientToScreen(hwnd, &origin) {
		t.Fatal("failed to get client origin")
	}
	point := captionMessagePoint(origin.X+96, origin.Y+captionPixels(25, windowCaptionDPI(hwnd)))
	win.PostMessage(hwnd, win.WM_NCLBUTTONDBLCLK, win.HTCAPTION, point)
	waitForCaption(t, "maximize by caption double click", func() bool { return win.IsZoomed(hwnd) })
	clickCaptionButton(t, hwnd, 1, height, scale)
	waitForCaption(t, "restore after caption double click", func() bool { return !win.IsZoomed(hwnd) })
	clickCaptionButton(t, hwnd, 0, height, scale)
	waitForCaption(t, "minimize by painted button", func() bool { return win.IsIconic(hwnd) })
	win.PostMessage(hwnd, win.WM_SYSCOMMAND, win.SC_RESTORE, 0)
	waitForCaption(t, "restore after minimize", func() bool { return !win.IsIconic(hwnd) })
	clickCaptionButton(t, hwnd, 2, height, scale)
	waitForCaptionClose(t, app)
	t.Logf("isolated desktop: scale=%g painted buttons clicked successfully (maximize, restore, minimize and graceful close)", scale)
}

func TestCaptionNativeOversizedSavedWindow(t *testing.T) {
	binary := os.Getenv("COMIC_CAPTION_SMOKE_EXE")
	if binary == "" {
		t.Skip("set COMIC_CAPTION_SMOKE_EXE to exercise the Windows release")
	}
	t.Setenv("FYNE_SCALE", "2")
	saved := persistedWindowPlacement{
		Valid: true, NormalPosition: persistedRect{Left: -900, Top: -500, Right: 2600, Bottom: 1700},
	}
	app, root, finish := startCaptionSmokeWithPlacement(t, binary, saved)
	defer finish()
	hwnd := findCaptionSmokeWindow(t, app, root, "native window caption enabled")
	assertStartupWorkArea(t, hwnd)
	// Fyne can queue a second resize when the custom caption becomes visible.
	// Check that it has not moved the window back outside the work area.
	time.Sleep(300 * time.Millisecond)
	assertStartupWorkArea(t, hwnd)
	closeCaptionSmoke(t, hwnd, app)
}

func assertStartupWorkArea(t *testing.T, hwnd win.HWND) {
	t.Helper()
	var bounds win.RECT
	info := win.MONITORINFO{CbSize: uint32(unsafe.Sizeof(win.MONITORINFO{}))}
	if !win.GetWindowRect(hwnd, &bounds) || !win.GetMonitorInfo(win.MonitorFromWindow(hwnd, 2), &info) {
		t.Fatal("cannot read restored window's work area")
	}
	work := info.RcWork
	if bounds.Left < work.Left || bounds.Top < work.Top || bounds.Right > work.Right || bounds.Bottom > work.Bottom {
		t.Fatalf("startup window is outside monitor work area: bounds=%+v work=%+v", bounds, work)
	}
	t.Logf("startup bounds=%+v fit within work area=%+v", bounds, work)
}

func TestCaptionPreviousWindow(t *testing.T) {
	binary := os.Getenv("COMIC_CAPTION_BASELINE_EXE")
	if binary == "" {
		t.Skip("set COMIC_CAPTION_BASELINE_EXE to exercise the previous build")
	}
	app, root, finish := startCaptionSmoke(t, binary)
	defer finish()
	hwnd := findCaptionSmokeWindow(t, app, root, "startup projectRoot=")
	var outer win.RECT
	var origin win.POINT
	if !win.GetWindowRect(hwnd, &outer) || !win.ClientToScreen(hwnd, &origin) || origin.Y <= outer.Top {
		t.Fatal("baseline should still reserve a separate system title area")
	}
	closeCaptionSmoke(t, hwnd, app)
	t.Logf("isolated desktop: previous window had a separate %d px native caption area and closed gracefully", origin.Y-outer.Top)
}

func startCaptionSmoke(t *testing.T, binary string) (windows.ProcessInformation, string, func()) {
	t.Helper()
	return startCaptionSmokeWithPlacement(t, binary, persistedWindowPlacement{})
}

func startCaptionSmokeWithPlacement(t *testing.T, binary string, placement persistedWindowPlacement) (windows.ProcessInformation, string, func()) {
	t.Helper()
	root := t.TempDir()
	for _, folder := range []string{"runtime", "workers"} {
		if err := os.Mkdir(filepath.Join(root, folder), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Completed synthetic tasks populate the real task-card list without
	// loading the user's library or starting downloads.
	fixture := persistedAppState{Version: persistedAppStateVersion, Concurrency: 1, NextTaskID: 9, UI: persistedUIState{Window: placement}}
	for i := 1; i <= 8; i++ {
		fixture.Tasks = append(fixture.Tasks, &Task{
			ID: i, URL: fmt.Sprintf("https://example.invalid/resize-fixture/%d", i),
			Title:  fmt.Sprintf("Resize fixture %d — a long synthetic title", i),
			Detail: "Completed fixture", Percent: 1, State: TaskDone,
		})
	}
	state, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime", "comic_downloader_state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "Comic_PC.exe")
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(exe, data, 0700); err != nil {
		t.Fatal(err)
	}
	app, finish := startCaptionTestDesktop(t, exe, root)
	return app, root, finish
}

func findCaptionSmokeWindow(t *testing.T, app windows.ProcessInformation, root, marker string) win.HWND {
	t.Helper()
	var hwnd win.HWND
	pid := app.ProcessId
	cb := syscall.NewCallback(func(candidate, _ uintptr) uintptr {
		var windowPID uint32
		window := win.HWND(candidate)
		win.GetWindowThreadProcessId(window, &windowPID)
		var bounds win.RECT
		if pid == windowPID && win.GetParent(window) == 0 && win.IsWindowVisible(window) &&
			win.GetWindowRect(window, &bounds) && validWindowRect(bounds) {
			hwnd = window
			return 0
		}
		return 1
	})
	logPath := filepath.Join(root, "logs", "comic_downloader.log")
	waitForCaption(t, "custom frame", func() bool {
		enumWindowsProc.Call(cb, 0)
		log, _ := os.ReadFile(logPath)
		return hwnd != 0 && strings.Contains(string(log), marker)
	})
	return hwnd
}

func closeCaptionSmoke(t *testing.T, hwnd win.HWND, app windows.ProcessInformation) {
	t.Helper()
	win.PostMessage(hwnd, win.WM_SYSCOMMAND, win.SC_CLOSE, 0)
	waitForCaptionClose(t, app)
}

func waitForCaptionClose(t *testing.T, app windows.ProcessInformation) {
	t.Helper()
	event, err := windows.WaitForSingleObject(app.Process, 15000)
	if err != nil || event != windows.WAIT_OBJECT_0 {
		t.Fatalf("window did not close gracefully: event=%d err=%v", event, err)
	}
	var exit uint32
	if err = windows.GetExitCodeProcess(app.Process, &exit); err != nil || exit != 0 {
		t.Fatalf("application exit=%d err=%v", exit, err)
	}
}

// This creates a separate desktop and never switches the user's input desktop.
// The test thread is temporarily bound to it so Win32 can address only the
// disposable test window, even when another app owns the foreground.
func startCaptionTestDesktop(t *testing.T, exe, root string) (windows.ProcessInformation, func()) {
	t.Helper()
	runtime.LockOSThread()
	createDesktop := captionUser.NewProc("CreateDesktopW")
	closeDesktop := captionUser.NewProc("CloseDesktop")
	getThreadDesktop := captionUser.NewProc("GetThreadDesktop")
	setThreadDesktop := captionUser.NewProc("SetThreadDesktop")
	setThreadDPI := captionUser.NewProc("SetThreadDpiAwarenessContext")
	old, _, _ := getThreadDesktop.Call(uintptr(windows.GetCurrentThreadId()))
	var app windows.ProcessInformation
	var desk, oldDPI uintptr
	cleanup := func() {
		if app.Process != 0 {
			state, _ := windows.WaitForSingleObject(app.Process, 0)
			if state != windows.WAIT_OBJECT_0 {
				_ = windows.TerminateProcess(app.Process, 1)
				_, _ = windows.WaitForSingleObject(app.Process, 5000)
			}
			_ = windows.CloseHandle(app.Thread)
			_ = windows.CloseHandle(app.Process)
		}
		if desk != 0 {
			setThreadDesktop.Call(old)
			closeDesktop.Call(desk)
		}
		if oldDPI != 0 {
			setThreadDPI.Call(oldDPI)
		}
		runtime.UnlockOSThread()
	}
	started := false
	defer func() {
		if !started {
			cleanup()
		}
	}()
	if old == 0 {
		t.Fatal("test thread has no desktop")
	}
	name := fmt.Sprintf("ComicCaptionTest-%d-%d", os.Getpid(), time.Now().UnixNano())
	namePtr := windows.StringToUTF16Ptr(name)
	const desktopAllAccess = 0x01ff
	var err error
	desk, _, err = createDesktop.Call(uintptr(unsafe.Pointer(namePtr)), 0, 0, 0, desktopAllAccess, 0)
	if desk == 0 {
		t.Fatalf("failed to create isolated test desktop: %v", err)
	}
	if ok, _, err := setThreadDesktop.Call(desk); ok == 0 {
		t.Fatalf("failed to bind only the test thread to its desktop: %v", err)
	}
	// Compare HWND and client bounds in physical pixels. At 125% Windows can
	// otherwise round the virtualized window and client rectangles separately.
	if setThreadDPI.Find() == nil {
		const perMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
		oldDPI, _, err = setThreadDPI.Call(perMonitorAwareV2)
		if oldDPI == 0 {
			t.Fatalf("failed to use physical coordinates on the test thread: %v", err)
		}
	}
	startup := windows.StartupInfo{Desktop: namePtr}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	err = windows.CreateProcess(windows.StringToUTF16Ptr(exe), nil, nil, nil, false, 0, nil, windows.StringToUTF16Ptr(root), &startup, &app)
	if err != nil {
		t.Fatalf("failed to start the app on the isolated desktop: %v", err)
	}
	started = true
	return app, cleanup
}

func captionButtonPoint(client win.RECT, button int, height int32, scale float64) (int32, int32) {
	return client.Right - int32(math.Round((2.5-float64(button))*captionButtonWidth*scale)), height / 2
}

// The optional capture build writes its own GLFW canvas from the isolated
// desktop. A screenshot of a classic Windows frame is not valid evidence.
func verifyNativeCaptionCapture(t *testing.T, root string, height int32, scale float64, state string) {
	t.Helper()
	dir := os.Getenv("COMIC_CAPTION_CAPTURE_DIR")
	if dir == "" {
		return
	}
	source := filepath.Join(root, "caption-"+state+".png")
	waitForCaption(t, "GLFW canvas capture: "+state, func() bool {
		_, err := os.Stat(source)
		return err == nil
	})
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := png.Decode(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("capture decode=%v close=%v", err, closeErr)
	}
	path := filepath.Join(dir, fmt.Sprintf("native-caption-%g-%s.png", scale, state))
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	// Reject a blank or forwarded native frame even if it happens to contain
	// three bright button-sized patches. Our painted row also includes the cyan
	// GO badge beside the title.
	badge := 0
	for y := 0; y < int(height); y++ {
		for x := int(60 * scale); x < int(135*scale); x++ {
			r, g, b, _ := preview.At(x, y).RGBA()
			if r < 25700 && g > 30840 && b > 43690 {
				badge++
			}
		}
	}
	if badge < 12 {
		t.Errorf("native capture %s does not show the app-painted GO caption", path)
	}
	for i := 0; i < 3; i++ {
		mid, _ := captionButtonPoint(win.RECT{Right: int32(preview.Bounds().Dx())}, i, height, scale)
		bright := 0
		for y := int(height) / 5; y < int(height)*4/5; y++ {
			for x := int(mid) - int(9*scale); x <= int(mid)+int(9*scale); x++ {
				r, g, b, _ := preview.At(x, y).RGBA()
				if r > 41120 && g > 41120 && b > 41120 {
					bright++
				}
			}
		}
		if bright < 8 {
			t.Errorf("native capture %s: control %d has only %d visible glyph pixels", path, i, bright)
		}
	}
	t.Logf("native caption pixels captured in %s", path)
}

func clickCaptionButton(t *testing.T, hwnd win.HWND, button int, height int32, scale float64) {
	t.Helper()
	var client win.RECT
	if !win.GetClientRect(hwnd, &client) {
		t.Fatal("failed to get client rectangle")
	}
	x, y := captionButtonPoint(client, button, height, scale)
	point := captionMessagePoint(x, y)
	win.PostMessage(hwnd, win.WM_MOUSEMOVE, 0, point)
	win.PostMessage(hwnd, win.WM_LBUTTONDOWN, win.MK_LBUTTON, point)
	win.PostMessage(hwnd, win.WM_LBUTTONUP, 0, point)
}

func assertNativeCaption(t *testing.T, hwnd win.HWND, height int32, scale float64) {
	t.Helper()
	var outer, client win.RECT
	if !win.GetWindowRect(hwnd, &outer) || !win.GetClientRect(hwnd, &client) {
		t.Fatal("failed to get window geometry")
	}
	var origin win.POINT
	if !win.ClientToScreen(hwnd, &origin) {
		t.Fatal("failed to get client origin")
	}
	getDWMAttribute := captionDWM.NewProc("DwmGetWindowAttribute")
	const ncRenderingEnabled = 1
	var enabled int32
	hr, _, _ := getDWMAttribute.Call(uintptr(hwnd), ncRenderingEnabled, uintptr(unsafe.Pointer(&enabled)), unsafe.Sizeof(enabled))
	if int32(hr) != 0 || enabled != 0 {
		t.Errorf("system frame painting should be disabled: HRESULT=%x enabled=%d", hr, enabled)
	}
	for i := 0; i < 3; i++ {
		x, y := captionButtonPoint(client, i, height, scale)
		if got := win.SendMessage(hwnd, win.WM_NCHITTEST, 0, captionMessagePoint(origin.X+x, origin.Y+y)); got != win.HTCLIENT {
			t.Errorf("painted control %d not client-clickable: %d", i, got)
		}
	}
	dpi := windowCaptionDPI(hwnd)
	x := origin.X + client.Right/2
	drag := win.SendMessage(hwnd, win.WM_NCHITTEST, 0, captionMessagePoint(x, origin.Y+height-2))
	body := win.SendMessage(hwnd, win.WM_NCHITTEST, 0, captionMessagePoint(x, origin.Y+height+2))
	if drag != win.HTCAPTION || body != win.HTCLIENT {
		t.Errorf("drag/body hit codes = %d/%d", drag, body)
	}
	if !win.IsZoomed(hwnd) {
		requireFlushClient(t, hwnd)
		top := win.SendMessage(hwnd, win.WM_NCHITTEST, 0, captionMessagePoint(x, outer.Top+1))
		if top != win.HTTOP {
			t.Errorf("top resize hit code = %d, want %d", top, win.HTTOP)
		}
		y := origin.Y + height + 50
		left := win.SendMessage(hwnd, win.WM_NCHITTEST, 0, captionMessagePoint(outer.Left+1, y))
		right := win.SendMessage(hwnd, win.WM_NCHITTEST, 0, captionMessagePoint(outer.Right-2, y))
		if left != win.HTLEFT || right != win.HTRIGHT {
			t.Errorf("side resize hit codes = %d/%d, want %d/%d", left, right, win.HTLEFT, win.HTRIGHT)
		}
		leftCorner := win.SendMessage(hwnd, win.WM_NCHITTEST, 0, captionMessagePoint(outer.Left+1, outer.Top+1))
		rightCorner := win.SendMessage(hwnd, win.WM_NCHITTEST, 0, captionMessagePoint(outer.Right-2, outer.Top+1))
		if leftCorner != win.HTTOPLEFT || rightCorner != win.HTTOPRIGHT {
			t.Errorf("top corner codes = %d/%d, want %d/%d", leftCorner, rightCorner, win.HTTOPLEFT, win.HTTOPRIGHT)
		}
		bottom := win.SendMessage(hwnd, win.WM_NCHITTEST, 0, captionMessagePoint(x, outer.Bottom-2))
		if bottom != win.HTBOTTOM {
			t.Errorf("bottom resize hit code = %d, want %d", bottom, win.HTBOTTOM)
		}
	} else {
		monitor := win.MonitorFromWindow(hwnd, 2) // MONITOR_DEFAULTTONEAREST
		info := win.MONITORINFO{CbSize: uint32(unsafe.Sizeof(win.MONITORINFO{}))}
		if monitor == 0 || !win.GetMonitorInfo(monitor, &info) {
			t.Fatal("cannot read maximized window work area")
		}
		work := info.RcWork
		if origin.X < work.Left || origin.Y < work.Top ||
			origin.X+client.Right > work.Right || origin.Y+client.Bottom > work.Bottom {
			t.Errorf("maximized client leaves work area: origin=%+v client=%+v work=%+v", origin, client, work)
		}
	}
	t.Logf("dpi=%d zoomed=%v: painted button zones, title drag, body and resize checks", dpi, win.IsZoomed(hwnd))
}

func requireFlushClient(t *testing.T, hwnd win.HWND) {
	t.Helper()
	var outer, client win.RECT
	var origin win.POINT
	if !win.GetWindowRect(hwnd, &outer) || !win.GetClientRect(hwnd, &client) || !win.ClientToScreen(hwnd, &origin) {
		t.Fatal("could not read restored window geometry")
	}
	if origin.X != outer.Left || origin.Y != outer.Top || client.Right != outer.Right-outer.Left || client.Bottom != outer.Bottom-outer.Top {
		t.Fatalf("restored window has an exposed nonclient strip: outer=%+v client=%+v origin=%+v", outer, client, origin)
	}
}

func assertRapidCaptionMove(t *testing.T, hwnd win.HWND) {
	t.Helper()
	var initial win.RECT
	if !win.GetWindowRect(hwnd, &initial) {
		t.Fatal("cannot read window before moving")
	}
	width, height := initial.Right-initial.Left, initial.Bottom-initial.Top
	const flags = win.SWP_NOACTIVATE | win.SWP_NOZORDER
	for i := int32(1); i <= 24; i++ {
		// Supplying the same size without SWP_NOSIZE also occurs in practice.
		if !win.SetWindowPos(hwnd, 0, initial.Left+i, initial.Top+i/2, width, height, flags) {
			t.Fatalf("move %d failed", i)
		}
		var bounds win.RECT
		if !win.GetWindowRect(hwnd, &bounds) || bounds.Right-bounds.Left != width || bounds.Bottom-bounds.Top != height {
			t.Fatalf("move %d changed the window's dimensions: %+v", i, bounds)
		}
	}
	if !win.SetWindowPos(hwnd, 0, initial.Left, initial.Top, 0, 0, flags|win.SWP_NOSIZE) {
		t.Fatal("failed to restore position after moves")
	}
	t.Log("24 rapid position-only moves preserved the window dimensions")
}

func assertRapidCaptionResize(t *testing.T, hwnd win.HWND, scale float64) {
	t.Helper()
	var original win.RECT
	if !win.GetWindowRect(hwnd, &original) {
		t.Fatal("could not read starting size")
	}
	w, h := original.Right-original.Left, original.Bottom-original.Top
	maxCut := w - int32(math.Ceil(520*scale))
	if maxCut < 50 {
		t.Fatalf("starting width %d has insufficient room for the narrow-window test", w)
	}
	minWidth := w
	const flags = win.SWP_NOZORDER | win.SWP_NOACTIVATE
	for i := 0; i < 36; i++ {
		// Alternate shrinking from the left and right without waiting for a
		// paint between calls; also exercise the bottom edge.
		cut := 20 + (maxCut-20)*int32(i%6)/5
		x := original.Left
		if i%2 == 0 {
			x += cut
		}
		if !win.SetWindowPos(hwnd, 0, x, original.Top, w-cut, h-int32(i%6)*20, flags) {
			t.Fatalf("resize %d failed", i)
		}
		requireFlushClient(t, hwnd)
		var client win.RECT
		if !win.GetClientRect(hwnd, &client) {
			t.Fatal("cannot measure narrowed task list")
		}
		if client.Right < minWidth {
			minWidth = client.Right
		}
	}
	if !win.SetWindowPos(hwnd, 0, original.Left, original.Top, w, h, flags) {
		t.Fatal("failed to restore original bounds")
	}
	requireFlushClient(t, hwnd)
	t.Logf("36 rapid left/right/bottom resizes with synthetic task cards kept the client flush; narrowest width=%d px", minWidth)
}

func captionMessagePoint(x, y int32) uintptr {
	return uintptr(uint32(uint16(x)) | uint32(uint16(y))<<16)
}

func waitForCaption(t *testing.T, action string, check func() bool) {
	t.Helper()
	for end := time.Now().Add(12 * time.Second); time.Now().Before(end); time.Sleep(40 * time.Millisecond) {
		if check() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", action)
}
