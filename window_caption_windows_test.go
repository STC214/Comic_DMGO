//go:build windows && !legacyui

package main

import (
	"fmt"
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

func TestCaptionHitZones(t *testing.T) {
	for _, dpi := range []int32{96, 120, 144, 192} {
		bar := captionPixels(captionHeight, dpi)
		frame := captionPixels(8, dpi)
		for _, tc := range []struct {
			y      int32
			resize int32
			want   uintptr
		}{
			{-1, frame, win.HTCLIENT},
			{0, frame, win.HTTOP},
			{frame - 1, frame, win.HTTOP},
			{frame, frame, win.HTCAPTION},
			{bar - 1, frame, win.HTCAPTION},
			{bar, frame, win.HTCLIENT},
			{1, 0, win.HTCAPTION},
		} {
			if got := captionHitZone(win.HTCLIENT, tc.y, bar, tc.resize); got != tc.want {
				t.Errorf("dpi=%d y=%d resize=%d: hit=%d, want=%d", dpi, tc.y, tc.resize, got, tc.want)
			}
		}
	}
}

type captionTestCanvas struct {
	fyne.Canvas
	scale float32
}

func (c captionTestCanvas) Scale() float32 { return c.scale }

func TestCaptionUsesFyneScale(t *testing.T) {
	const fakeWindow win.HWND = 1 // no Win32 calls for a valid canvas scale
	defer delete(windowCaptionCanvases, fakeWindow)
	for _, scale := range []float32{0.75, 1, 1.25, 1.5, 2} {
		windowCaptionCanvases[fakeWindow] = captionTestCanvas{scale: scale}
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
	assertNativeCaption(t, hwnd, height)
	var origin win.POINT
	if !win.ClientToScreen(hwnd, &origin) {
		t.Fatal("failed to get client origin")
	}
	point := captionMessagePoint(origin.X+96, origin.Y+captionPixels(25, windowCaptionDPI(hwnd)))
	win.PostMessage(hwnd, win.WM_NCLBUTTONDBLCLK, win.HTCAPTION, point)
	waitForCaption(t, "maximize by caption double click", func() bool { return win.IsZoomed(hwnd) })
	time.Sleep(300 * time.Millisecond) // wait for DWM to publish new button bounds
	assertNativeCaption(t, hwnd, height)
	win.PostMessage(hwnd, win.WM_SYSCOMMAND, win.SC_RESTORE, 0)
	waitForCaption(t, "restore", func() bool { return !win.IsZoomed(hwnd) })
	win.PostMessage(hwnd, win.WM_SYSCOMMAND, win.SC_MINIMIZE, 0)
	waitForCaption(t, "minimize", func() bool { return win.IsIconic(hwnd) })
	win.PostMessage(hwnd, win.WM_SYSCOMMAND, win.SC_RESTORE, 0)
	waitForCaption(t, "restore after minimize", func() bool { return !win.IsIconic(hwnd) })
	closeCaptionSmoke(t, hwnd, app)
	t.Logf("isolated desktop: scale=%g native caption buttons, maximize, restore, minimize, and graceful close passed", scale)
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
	root := t.TempDir()
	for _, folder := range []string{"runtime", "workers"} {
		if err := os.Mkdir(filepath.Join(root, folder), 0755); err != nil {
			t.Fatal(err)
		}
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
		if pid == windowPID && win.GetParent(window) == 0 && win.IsWindowVisible(window) {
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
	old, _, _ := getThreadDesktop.Call(uintptr(windows.GetCurrentThreadId()))
	var app windows.ProcessInformation
	var desk uintptr
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
	startup := windows.StartupInfo{Desktop: namePtr}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	err = windows.CreateProcess(windows.StringToUTF16Ptr(exe), nil, nil, nil, false, 0, nil, windows.StringToUTF16Ptr(root), &startup, &app)
	if err != nil {
		t.Fatalf("failed to start the app on the isolated desktop: %v", err)
	}
	started = true
	return app, cleanup
}

func assertNativeCaption(t *testing.T, hwnd win.HWND, height int32) {
	t.Helper()
	var outer, client, buttons win.RECT
	if !win.GetWindowRect(hwnd, &outer) || !win.GetClientRect(hwnd, &client) {
		t.Fatal("failed to get window geometry")
	}
	var origin win.POINT
	if !win.ClientToScreen(hwnd, &origin) {
		t.Fatal("failed to get client origin")
	}
	getDWMAttribute := captionDWM.NewProc("DwmGetWindowAttribute")
	const captionButtonBounds = 5
	hr, _, _ := getDWMAttribute.Call(uintptr(hwnd), captionButtonBounds, uintptr(unsafe.Pointer(&buttons)), unsafe.Sizeof(buttons))
	if int32(hr) != 0 || buttons.Right <= buttons.Left || buttons.Bottom <= buttons.Top {
		t.Fatalf("native caption buttons unavailable: HRESULT=%x bounds=%+v", hr, buttons)
	}
	hits := map[uintptr]int{}
	// A maximized frame's invisible top border is outside the work area.
	// DWM's buttons are positioned in the visible top band.
	buttonTop := outer.Top
	if origin.Y > buttonTop {
		buttonTop = origin.Y
	}
	for y := buttonTop + buttons.Top + 3; y < buttonTop+buttons.Bottom; y += 4 {
		for x := outer.Left + buttons.Left; x < outer.Left+buttons.Right; x += 5 {
			hits[win.SendMessage(hwnd, win.WM_NCHITTEST, 0, captionMessagePoint(x, y))]++
		}
	}
	for _, button := range []uintptr{win.HTMINBUTTON, win.HTMAXBUTTON, win.HTCLOSE} {
		if hits[button] == 0 {
			t.Errorf("DWM button hit code %d not found; hit counts: %v, bounds=%+v", button, hits, buttons)
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
	}
	t.Logf("dpi=%d zoomed=%v window=%+v client=%+v caption buttons=%+v hit counts=%v", dpi, win.IsZoomed(hwnd), outer, client, buttons, hits)
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
