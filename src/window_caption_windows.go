//go:build windows && !legacyui

package main

import (
	"image/color"
	"log"
	"math"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/lxn/win"
)

const (
	captionHeight       = 40
	captionButtonWidth  = 46
	captionButtonsWidth = 3 * captionButtonWidth
	captionSubclassID   = 1
	captionFramePad     = 92 // SM_CXPADDEDBORDER
)

var (
	captionDWM             = syscall.NewLazyDLL("dwmapi.dll")
	captionControls        = syscall.NewLazyDLL("comctl32.dll")
	captionUser            = syscall.NewLazyDLL("user32.dll")
	captionKernel          = syscall.NewLazyDLL("kernel32.dll")
	dwmSetCaptionAttribute = captionDWM.NewProc("DwmSetWindowAttribute")
	setCaptionSubclass     = captionControls.NewProc("SetWindowSubclass")
	removeCaptionSubclass  = captionControls.NewProc("RemoveWindowSubclass")
	defCaptionSubclass     = captionControls.NewProc("DefSubclassProc")
	dpiForCaptionWindow    = captionUser.NewProc("GetDpiForWindow")
	captionMetricsForDPI   = captionUser.NewProc("GetSystemMetricsForDpi")
	copyCaptionMemory      = captionKernel.NewProc("RtlMoveMemory")
	windowCaptionCallback  uintptr
	// Only accessed by the Fyne/GLFW thread that owns the native window.
	windowCaptions = make(map[win.HWND]*windowCaptionState)
)

type windowCaptionState struct {
	canvas       fyne.Canvas
	ui           *fyneUI
	maximized    bool
	startupUntil time.Time
}

var captionRestoreIcon = theme.NewThemedResource(fyne.NewStaticResource("windowRestore.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M9 3h12v12h-3v6H3V6h6zm2 2v1h7v7h1V5zM5 8v11h11V8z"/></svg>`)))

func init() {
	windowCaptionCallback = syscall.NewCallback(windowCaptionProc)
}

func (ui *fyneUI) makeWindowCaption() fyne.CanvasObject {
	background := canvas.NewRectangle(color.NRGBA{R: 16, G: 22, B: 34, A: 255})
	background.SetMinSize(fyne.NewSize(0, captionHeight))
	title := widget.NewLabelWithStyle(fyneWindowTitle, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	badge := canvas.NewImageFromResource(goTitleMarkResource())
	badge.FillMode = canvas.ImageFillContain
	badge.SetMinSize(fyne.NewSize(27, 17))
	content := container.NewHBox(title, badge)
	button := func(icon fyne.Resource, command uintptr) *widget.Button {
		b := widget.NewButtonWithIcon("", icon, func() { ui.postCaptionCommand(command) })
		b.Importance = widget.LowImportance
		return b
	}
	ui.captionMax = button(theme.WindowMaximizeIcon(), win.SC_MAXIMIZE)
	controls := container.New(captionControlsLayout{},
		button(theme.WindowMinimizeIcon(), win.SC_MINIMIZE),
		ui.captionMax,
		button(theme.WindowCloseIcon(), win.SC_CLOSE),
	)
	return container.NewStack(background, container.NewBorder(nil, nil, container.NewCenter(content), controls))
}

type captionControlsLayout struct{}

func (captionControlsLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(captionButtonsWidth, captionHeight)
}

func (captionControlsLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for i, button := range objects {
		button.Move(fyne.NewPos(float32(i)*captionButtonWidth, 0))
		button.Resize(fyne.NewSize(captionButtonWidth, size.Height))
	}
}

func (ui *fyneUI) postCaptionCommand(command uintptr) {
	hwnd := win.HWND(ui.captionHWND.Load())
	if hwnd == 0 {
		return
	}
	if command == win.SC_MAXIMIZE && win.IsZoomed(hwnd) {
		command = win.SC_RESTORE
	}
	win.PostMessage(hwnd, win.WM_SYSCOMMAND, command, 0)
}

func (ui *fyneUI) installWindowCaption(startupPlacement persistedWindowPlacement) {
	native, ok := ui.window.(driver.NativeWindow)
	if !ok {
		return
	}
	// Fyne creates the HWND asynchronously when Show is first processed.
	for i := 0; i < 80 && !ui.closing.Load(); i++ {
		var ready bool
		native.RunNative(func(value any) {
			ctx, ok := value.(driver.WindowsWindowContext)
			if !ok || ctx.HWND == 0 || ui.closing.Load() {
				return
			}
			ready = true
			hwnd := win.HWND(ctx.HWND)
			if !win.IsWindowVisible(hwnd) {
				ready = false
				return
			}
			if attachWindowCaption(hwnd, ui) {
				ui.captionHWND.Store(uintptr(hwnd))
				ui.caption.Show()
				ui.root.Refresh()
				fitStartupWindow(hwnd, startupPlacement)
				log.Printf("native window caption enabled scale=%g", ui.window.Canvas().Scale())
			} else {
				fitStartupWindow(hwnd, startupPlacement)
			}
		})
		if ready {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func fitStartupWindow(hwnd win.HWND, saved persistedWindowPlacement) {
	var outer win.RECT
	if !win.GetWindowRect(hwnd, &outer) || win.IsZoomed(hwnd) || win.IsIconic(hwnd) {
		return
	}
	work, ok := captionMonitorWorkArea(hwnd)
	if !ok {
		return
	}
	fitted := fitStartupRect(outer, work, saved)
	if fitted == outer {
		return
	}
	flags := uint32(win.SWP_NOZORDER | win.SWP_NOACTIVATE)
	if fitted.Right-fitted.Left == outer.Right-outer.Left && fitted.Bottom-fitted.Top == outer.Bottom-outer.Top {
		flags |= win.SWP_NOSIZE
	}
	if win.SetWindowPos(hwnd, 0, fitted.Left, fitted.Top, fitted.Right-fitted.Left, fitted.Bottom-fitted.Top, flags) {
		log.Printf("startup window fitted to work area: old=%+v target=%+v", outer, fitted)
	}
}

func captionMonitorWorkArea(hwnd win.HWND) (win.RECT, bool) {
	monitor := win.MonitorFromWindow(hwnd, 2) // MONITOR_DEFAULTTONEAREST
	info := win.MONITORINFO{CbSize: uint32(unsafe.Sizeof(win.MONITORINFO{}))}
	ok := monitor != 0 && win.GetMonitorInfo(monitor, &info) &&
		info.RcWork.Right > info.RcWork.Left && info.RcWork.Bottom > info.RcWork.Top
	return info.RcWork, ok
}

func fitStartupRect(outer, work win.RECT, saved persistedWindowPlacement) win.RECT {
	availableW, availableH := work.Right-work.Left, work.Bottom-work.Top
	width, height := outer.Right-outer.Left, outer.Bottom-outer.Top
	if availableW <= 0 || availableH <= 0 || width <= 0 || height <= 0 {
		return outer
	}
	left, top := outer.Left, outer.Top
	if saved.Valid && validPersistedRect(saved.NormalPosition) {
		// Saved bounds are already physical Windows pixels. Do not feed them
		// into Fyne.Resize, which would multiply them by UI scale again.
		width = saved.NormalPosition.Right - saved.NormalPosition.Left
		height = saved.NormalPosition.Bottom - saved.NormalPosition.Top
		width, height = min(width, availableW), min(height, availableH)
		left, top = work.Left+(availableW-width)/2, work.Top+(availableH-height)/2
	}
	width, height = min(width, availableW), min(height, availableH)
	if left < work.Left {
		left = work.Left
	}
	if top < work.Top {
		top = work.Top
	}
	left, top = min(left, work.Right-width), min(top, work.Bottom-height)
	return win.RECT{Left: left, Top: top, Right: left + width, Bottom: top + height}
}

func attachWindowCaption(hwnd win.HWND, ui *fyneUI) bool {
	for _, proc := range []*syscall.LazyProc{
		dwmSetCaptionAttribute, setCaptionSubclass, removeCaptionSubclass, defCaptionSubclass, copyCaptionMemory,
	} {
		if proc.Find() != nil {
			return false
		}
	}
	// Showing the caption can queue another GLFW size adjustment after the
	// first fit. Keep those startup adjustments inside the work area too.
	state := &windowCaptionState{canvas: ui.window.Canvas(), ui: ui, startupUntil: time.Now().Add(time.Second)}
	windowCaptions[hwnd] = state
	attached := false
	defer func() {
		if !attached {
			delete(windowCaptions, hwnd)
		}
	}()
	ok, _, _ := setCaptionSubclass.Call(uintptr(hwnd), windowCaptionCallback, captionSubclassID, 0)
	if ok == 0 {
		return false
	}
	const noNativePainting = 1 // DWMNCRP_DISABLED
	if setCaptionRendering(hwnd, noNativePainting) && forceCaptionLayout(hwnd) {
		attached = true
		state.refreshMaxIcon(hwnd)
		return true
	}
	removeCaptionSubclass.Call(uintptr(hwnd), windowCaptionCallback, captionSubclassID)
	setCaptionRendering(hwnd, 0) // DWMNCRP_USEWINDOWSTYLE
	forceCaptionLayout(hwnd)
	return false
}

func setCaptionRendering(hwnd win.HWND, policy int32) bool {
	const ncRenderingPolicy = 2 // DWMWA_NCRENDERING_POLICY
	hr, _, _ := dwmSetCaptionAttribute.Call(uintptr(hwnd), ncRenderingPolicy, uintptr(unsafe.Pointer(&policy)), unsafe.Sizeof(policy))
	return int32(hr) == 0
}

func (state *windowCaptionState) refreshMaxIcon(hwnd win.HWND) {
	maximized := win.IsZoomed(hwnd)
	if maximized == state.maximized {
		return
	}
	state.maximized = maximized
	if maximized {
		state.ui.captionMax.SetIcon(captionRestoreIcon)
	} else {
		state.ui.captionMax.SetIcon(theme.WindowMaximizeIcon())
	}
}

func forceCaptionLayout(hwnd win.HWND) bool {
	return win.SetWindowPos(hwnd, 0, 0, 0, 0, 0,
		win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE|win.SWP_FRAMECHANGED)
}

func windowCaptionProc(hwnd uintptr, msg uint32, wp, lp, id, _ uintptr) uintptr {
	window := win.HWND(hwnd)
	if msg == win.WM_NCDESTROY {
		if state := windowCaptions[window]; state != nil {
			state.ui.captionHWND.Store(0)
		}
		delete(windowCaptions, window)
		removeCaptionSubclass.Call(hwnd, windowCaptionCallback, id)
		return defaultCaptionProc(hwnd, msg, wp, lp)
	}

	switch msg {
	case win.WM_ENTERSIZEMOVE:
		if state := windowCaptions[window]; state != nil {
			state.startupUntil = time.Time{}
		}
	case win.WM_WINDOWPOSCHANGING:
		result := defaultCaptionProc(hwnd, msg, wp, lp)
		if lp != 0 {
			var pos win.WINDOWPOS
			copyCaptionMemory.Call(uintptr(unsafe.Pointer(&pos)), lp, unsafe.Sizeof(pos))
			before := pos
			var current win.RECT
			haveCurrent := win.GetWindowRect(window, &current)
			if pos.Flags&win.SWP_NOSIZE == 0 && haveCurrent &&
				(pos.Cx != current.Right-current.Left || pos.Cy != current.Bottom-current.Top) &&
				!win.IsZoomed(window) && !win.IsIconic(window) {
				// Do this after GLFW/Win32 validate the proposed geometry:
				// their native style calculations still add the caption and
				// resize margins, even though our restored window has none.
				if work, ok := captionMonitorWorkArea(window); ok {
					pos.Cx = min(pos.Cx, work.Right-work.Left)
					pos.Cy = min(pos.Cy, work.Bottom-work.Top)
					if state := windowCaptions[window]; state != nil && time.Now().Before(state.startupUntil) {
						x, y := pos.X, pos.Y
						if pos.Flags&win.SWP_NOMOVE != 0 {
							x, y = current.Left, current.Top
						}
						fitted := fitStartupRect(win.RECT{Left: x, Top: y, Right: x + pos.Cx, Bottom: y + pos.Cy}, work, persistedWindowPlacement{})
						if fitted.Left != x || fitted.Top != y {
							pos.X, pos.Y = fitted.Left, fitted.Top
							pos.Flags &^= win.SWP_NOMOVE
						}
					}
				}
			}
			sizeChanged := haveCurrent &&
				(pos.Cx != current.Right-current.Left || pos.Cy != current.Bottom-current.Top)
			pos.Flags = captionResizeFlags(pos.Flags, sizeChanged)
			if pos != before {
				copyCaptionMemory.Call(lp, uintptr(unsafe.Pointer(&pos)), unsafe.Sizeof(pos))
			}
		}
		return result
	case win.WM_ERASEBKGND:
		// Keep GLFW's double-buffered surface intact during movement. A GDI
		// fill here clears the frame before OpenGL can present the next one.
		return 1
	case win.WM_NCPAINT:
		// DWM is disabled for the custom caption; don't let the classic
		// Windows frame paint another title or set of buttons.
		return 0
	case win.WM_NCACTIVATE:
		// Preserve native activation without asking it to repaint the frame.
		return defaultCaptionProc(hwnd, msg, wp, ^uintptr(0))
	case win.WM_NCCALCSIZE:
		if lp != 0 {
			return calculateWindowCaption(hwnd, msg, wp, lp)
		}
	case win.WM_NCHITTEST:
		return hitTestWindowCaption(window, defaultCaptionProc(hwnd, msg, wp, lp), lp)
	case win.WM_SIZE:
		result := defaultCaptionProc(hwnd, msg, wp, lp)
		if state := windowCaptions[window]; state != nil {
			state.refreshMaxIcon(window)
		}
		return result
	}
	return defaultCaptionProc(hwnd, msg, wp, lp)
}

// The first RECT is the proposed outer rectangle in both forms of NCCALCSIZE.
// Restored windows have no unpainted native strips. Maximized windows retain
// the invisible resize insets so the client doesn't extend over the taskbar.
func calculateWindowCaption(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	window := win.HWND(hwnd)
	if win.IsIconic(window) {
		return defaultCaptionProc(hwnd, msg, wp, lp)
	}
	if !win.IsZoomed(window) {
		// Use the whole restored window: there is no unpainted nonclient
		// strip along either side or the bottom.
		return 0
	}
	var top int32
	topAddress := lp + unsafe.Offsetof(win.RECT{}.Top)
	copyCaptionMemory.Call(uintptr(unsafe.Pointer(&top)), topAddress, unsafe.Sizeof(top))
	defaultCaptionProc(hwnd, msg, wp, lp)
	// Retain the maximized resize insets so the client stays in the work area.
	top += captionTopFrame(window)
	copyCaptionMemory.Call(topAddress, uintptr(unsafe.Pointer(&top)), unsafe.Sizeof(top))
	return 0
}

func hitTestWindowCaption(hwnd win.HWND, original, lp uintptr) uintptr {
	point := win.POINT{X: int32(int16(lp)), Y: int32(int16(lp >> 16))}
	var client win.RECT
	if !win.ScreenToClient(hwnd, &point) || !win.GetClientRect(hwnd, &client) ||
		point.X < 0 || point.X >= client.Right {
		return original
	}
	if point.Y < 0 || point.Y >= client.Bottom {
		return original
	}
	if !win.IsZoomed(hwnd) {
		if edge := captionResizeEdge(point, client, captionFrameMetric(hwnd, win.SM_CXSIZEFRAME), captionTopFrame(hwnd)); edge != win.HTCLIENT {
			return edge
		}
	}
	height := windowCaptionHeight(hwnd)
	if point.Y >= height {
		return win.HTCLIENT
	}
	if point.X >= client.Right-windowCaptionPixels(hwnd, captionButtonsWidth) {
		return win.HTCLIENT // GLFW/Fyne handle the painted buttons.
	}
	return win.HTCAPTION
}

func captionResizeFlags(flags uint32, sizeChanged bool) uint32 {
	if (flags&win.SWP_NOSIZE == 0 && sizeChanged) || flags&win.SWP_FRAMECHANGED != 0 {
		return flags | win.SWP_NOCOPYBITS
	}
	return flags
}

func captionResizeEdge(p win.POINT, bounds win.RECT, horizontal, vertical int32) uintptr {
	left, right := p.X < horizontal, p.X >= bounds.Right-horizontal
	if p.Y < vertical {
		if left {
			return win.HTTOPLEFT
		}
		if right {
			return win.HTTOPRIGHT
		}
		return win.HTTOP
	}
	if p.Y >= bounds.Bottom-vertical {
		if left {
			return win.HTBOTTOMLEFT
		}
		if right {
			return win.HTBOTTOMRIGHT
		}
		return win.HTBOTTOM
	}
	if left {
		return win.HTLEFT
	}
	if right {
		return win.HTRIGHT
	}
	return win.HTCLIENT
}

func captionPixels(logical, dpi int32) int32 {
	return (logical*dpi + 48) / 96
}

func windowCaptionHeight(hwnd win.HWND) int32 {
	return windowCaptionPixels(hwnd, captionHeight)
}

func windowCaptionPixels(hwnd win.HWND, logical int32) int32 {
	// Fyne's effective scale can differ from the Windows monitor DPI (for
	// example from FYNE_SCALE). Match the number of pixels it paints.
	if state := windowCaptions[hwnd]; state != nil && state.canvas != nil {
		if scale := state.canvas.Scale(); scale > 0 {
			return int32(math.Ceil(float64(float32(logical) * scale)))
		}
	}
	return captionPixels(logical, windowCaptionDPI(hwnd))
}

func windowCaptionDPI(hwnd win.HWND) int32 {
	if dpiForCaptionWindow.Find() == nil {
		if dpi, _, _ := dpiForCaptionWindow.Call(uintptr(hwnd)); dpi != 0 {
			return int32(dpi)
		}
	}
	return 96
}

func captionTopFrame(hwnd win.HWND) int32 {
	return captionFrameMetric(hwnd, win.SM_CYSIZEFRAME)
}

func captionFrameMetric(hwnd win.HWND, metric int32) int32 {
	if captionMetricsForDPI.Find() == nil {
		dpi := uintptr(windowCaptionDPI(hwnd))
		frame, _, _ := captionMetricsForDPI.Call(uintptr(metric), dpi)
		padding, _, _ := captionMetricsForDPI.Call(captionFramePad, dpi)
		return int32(frame + padding)
	}
	return win.GetSystemMetrics(metric) + win.GetSystemMetrics(captionFramePad)
}

func defaultCaptionProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	result, _, _ := defCaptionSubclass.Call(hwnd, uintptr(msg), wp, lp)
	return result
}
