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
	"fyne.io/fyne/v2/widget"
	"github.com/lxn/win"
)

const (
	captionHeight     = 40
	captionSubclassID = 1
	captionFramePad   = 92 // SM_CXPADDEDBORDER
)

var (
	captionDWM             = syscall.NewLazyDLL("dwmapi.dll")
	captionControls        = syscall.NewLazyDLL("comctl32.dll")
	captionUser            = syscall.NewLazyDLL("user32.dll")
	captionKernel          = syscall.NewLazyDLL("kernel32.dll")
	dwmExtendCaption       = captionDWM.NewProc("DwmExtendFrameIntoClientArea")
	dwmCaptionProc         = captionDWM.NewProc("DwmDefWindowProc")
	dwmSetCaptionAttribute = captionDWM.NewProc("DwmSetWindowAttribute")
	setCaptionSubclass     = captionControls.NewProc("SetWindowSubclass")
	removeCaptionSubclass  = captionControls.NewProc("RemoveWindowSubclass")
	defCaptionSubclass     = captionControls.NewProc("DefSubclassProc")
	dpiForCaptionWindow    = captionUser.NewProc("GetDpiForWindow")
	captionMetricsForDPI   = captionUser.NewProc("GetSystemMetricsForDpi")
	copyCaptionMemory      = captionKernel.NewProc("RtlMoveMemory")
	windowCaptionCallback  uintptr
	// Only accessed by the Fyne/GLFW thread that owns the native window.
	windowCaptionCanvases = make(map[win.HWND]fyne.Canvas)
)

func init() {
	windowCaptionCallback = syscall.NewCallback(windowCaptionProc)
}

func makeWindowCaption() fyne.CanvasObject {
	background := canvas.NewRectangle(color.NRGBA{R: 16, G: 22, B: 34, A: 255})
	background.SetMinSize(fyne.NewSize(0, captionHeight))
	title := widget.NewLabelWithStyle(fyneWindowTitle, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	badge := canvas.NewImageFromResource(goTitleMarkResource())
	badge.FillMode = canvas.ImageFillContain
	badge.SetMinSize(fyne.NewSize(27, 17))
	// Keep the right side clear for the Windows-owned caption buttons.
	content := container.NewHBox(title, badge)
	return container.NewStack(background, container.NewBorder(nil, nil, container.NewCenter(content), nil))
}

func (ui *fyneUI) installWindowCaption() {
	native, ok := ui.window.(driver.NativeWindow)
	if !ok {
		return
	}
	// Fyne creates the HWND asynchronously when Show is first processed.
	for i := 0; i < 80 && !ui.closing.Load(); i++ {
		var ready bool
		native.RunNative(func(value any) {
			ctx, ok := value.(driver.WindowsWindowContext)
			if !ok || ctx.HWND == 0 {
				return
			}
			ready = true
			hwnd := win.HWND(ctx.HWND)
			if attachWindowCaption(hwnd, ui.window.Canvas()) {
				ui.caption.Show()
				ui.root.Refresh()
				log.Printf("native window caption enabled scale=%g", ui.window.Canvas().Scale())
			}
		})
		if ready {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func attachWindowCaption(hwnd win.HWND, canvas fyne.Canvas) bool {
	for _, proc := range []*syscall.LazyProc{
		dwmExtendCaption, dwmCaptionProc, setCaptionSubclass, removeCaptionSubclass, defCaptionSubclass, copyCaptionMemory,
	} {
		if proc.Find() != nil {
			return false
		}
	}
	windowCaptionCanvases[hwnd] = canvas
	attached := false
	defer func() {
		if !attached {
			delete(windowCaptionCanvases, hwnd)
		}
	}()
	if !extendWindowCaption(hwnd) {
		return false
	}
	ok, _, _ := setCaptionSubclass.Call(uintptr(hwnd), windowCaptionCallback, captionSubclassID, 0)
	if ok == 0 {
		resetCaptionFrame(hwnd)
		return false
	}
	if forceCaptionLayout(hwnd) {
		attached = true
		if dwmSetCaptionAttribute.Find() == nil {
			// Match the dark workspace when the system supports dark captions.
			dark := int32(1)
			const useImmersiveDarkMode = 20
			dwmSetCaptionAttribute.Call(uintptr(hwnd), useImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark))
		}
		return true
	}
	removeCaptionSubclass.Call(uintptr(hwnd), windowCaptionCallback, captionSubclassID)
	resetCaptionFrame(hwnd)
	return false
}

func extendWindowCaption(hwnd win.HWND) bool {
	// DWM MARGINS: left, right, top, bottom (physical pixels).
	margin := [4]int32{0, 0, windowCaptionHeight(hwnd) + captionTopFrame(hwnd), 0}
	hr, _, _ := dwmExtendCaption.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&margin)))
	return int32(hr) == 0
}

func resetCaptionFrame(hwnd win.HWND) {
	margin := [4]int32{}
	dwmExtendCaption.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&margin)))
	forceCaptionLayout(hwnd)
}

func forceCaptionLayout(hwnd win.HWND) bool {
	return win.SetWindowPos(hwnd, 0, 0, 0, 0, 0,
		win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE|win.SWP_FRAMECHANGED)
}

func windowCaptionProc(hwnd uintptr, msg uint32, wp, lp, id, _ uintptr) uintptr {
	window := win.HWND(hwnd)
	if msg == win.WM_NCDESTROY {
		delete(windowCaptionCanvases, window)
		removeCaptionSubclass.Call(hwnd, windowCaptionCallback, id)
		return defaultCaptionProc(hwnd, msg, wp, lp)
	}

	var result uintptr
	handled, _, _ := dwmCaptionProc.Call(hwnd, uintptr(msg), wp, lp, uintptr(unsafe.Pointer(&result)))
	if handled != 0 && (msg != win.WM_NCHITTEST || result != 0) {
		return result
	}

	switch msg {
	case win.WM_NCCALCSIZE:
		if lp != 0 {
			return calculateWindowCaption(hwnd, msg, wp, lp)
		}
	case win.WM_NCHITTEST:
		return hitTestWindowCaption(window, defaultCaptionProc(hwnd, msg, wp, lp), lp)
	case 0x02E0: // WM_DPICHANGED
		result := defaultCaptionProc(hwnd, msg, wp, lp)
		extendWindowCaption(window)
		return result
	}
	return defaultCaptionProc(hwnd, msg, wp, lp)
}

// The first RECT is the proposed outer rectangle in both forms of NCCALCSIZE.
// Keep the native sides and bottom but leave no non-client inset at the top.
// An inset there (even when maximized) prevents DWM from hit-testing its
// caption buttons. The caption is tall enough to clear the maximized border.
func calculateWindowCaption(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	var top int32
	topAddress := lp + unsafe.Offsetof(win.RECT{}.Top)
	copyCaptionMemory.Call(uintptr(unsafe.Pointer(&top)), topAddress, unsafe.Sizeof(top))
	defaultCaptionProc(hwnd, msg, wp, lp)
	copyCaptionMemory.Call(topAddress, uintptr(unsafe.Pointer(&top)), unsafe.Sizeof(top))
	return 0
}

func hitTestWindowCaption(hwnd win.HWND, original, lp uintptr) uintptr {
	if original != win.HTCLIENT && original != win.HTCAPTION && original != win.HTSYSMENU {
		return original
	}
	point := win.POINT{X: int32(int16(lp)), Y: int32(int16(lp >> 16))}
	if !win.ScreenToClient(hwnd, &point) {
		return original
	}
	resize := captionTopFrame(hwnd)
	if win.IsZoomed(hwnd) {
		resize = 0
	}
	return captionHitZone(original, point.Y, windowCaptionHeight(hwnd), resize)
}

func captionHitZone(original uintptr, y, height, resize int32) uintptr {
	if y < 0 || y >= height {
		return original
	}
	if y < resize {
		return win.HTTOP
	}
	return win.HTCAPTION
}

func captionPixels(logical, dpi int32) int32 {
	return (logical*dpi + 48) / 96
}

func windowCaptionHeight(hwnd win.HWND) int32 {
	// Fyne's effective scale can differ from the Windows monitor DPI (for
	// example from FYNE_SCALE). Match the number of pixels it paints.
	if canvas := windowCaptionCanvases[hwnd]; canvas != nil {
		if scale := canvas.Scale(); scale > 0 {
			return int32(math.Ceil(float64(captionHeight * scale)))
		}
	}
	return captionPixels(captionHeight, windowCaptionDPI(hwnd))
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
	if captionMetricsForDPI.Find() == nil {
		dpi := uintptr(windowCaptionDPI(hwnd))
		frame, _, _ := captionMetricsForDPI.Call(win.SM_CYSIZEFRAME, dpi)
		padding, _, _ := captionMetricsForDPI.Call(captionFramePad, dpi)
		return int32(frame + padding)
	}
	return win.GetSystemMetrics(win.SM_CYSIZEFRAME) + win.GetSystemMetrics(captionFramePad)
}

func defaultCaptionProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	result, _, _ := defCaptionSubclass.Call(hwnd, uintptr(msg), wp, lp)
	return result
}
