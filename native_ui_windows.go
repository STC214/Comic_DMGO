//go:build windows && legacyui

package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/lxn/win"
)

const (
	nativeTimerID    = 1
	nativeMsgRefresh = win.WM_APP + 1
	nativeMsgTray    = win.WM_APP + 2
	nativeIconSmall  = 0
	nativeIconBig    = 1
	nativeIconSmall2 = 2
	ownerDrawButton  = 4

	tabBase    = 1001
	tabOps     = 1002
	tabAdblock = 1003
	tabTheme   = 1004

	btnAddTask    = 1101
	btnClearDone  = 1102
	btnPauseTask  = 1103
	btnResumeTask = 1104
	btnRetryTask  = 1105
	btnCancelTask = 1106
	trayIconID    = 1
)

var (
	procDwmSetWindowAttribute = syscall.NewLazyDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")

	themeWindowBg         = win.RGB(22, 23, 27)
	themePanelBg          = win.RGB(26, 28, 33)
	themePanelAltBg       = win.RGB(30, 32, 38)
	themeBorderColor      = win.RGB(48, 50, 56)
	themeAccentColor      = win.RGB(42, 61, 82)
	themeAccentHoverColor = win.RGB(52, 74, 98)
	themeAccentTextColor  = win.RGB(255, 255, 255)
	themeTextColor        = win.RGB(224, 227, 233)
	themeMutedTextColor   = win.RGB(134, 141, 151)

	nativeClassName               = syscall.StringToUTF16Ptr("ComicDownloaderNativeWindow")
	nativeButtonClass             = syscall.StringToUTF16Ptr("Button")
	nativeEditClass               = syscall.StringToUTF16Ptr("Edit")
	nativeStaticClass             = syscall.StringToUTF16Ptr("Static")
	nativeListViewClass           = syscall.StringToUTF16Ptr("SysListView32")
	nativeEmptyString             = syscall.StringToUTF16Ptr("")
	nativeWndProcCallback uintptr = syscall.NewCallback(nativeWndProc)
)

type nativeUI struct {
	manager   *Manager
	hInstance win.HINSTANCE
	hwnd      win.HWND

	bgBrush               win.HBRUSH
	panelBrush            win.HBRUSH
	accentBrush           win.HBRUSH
	buttonBrush           win.HBRUSH
	buttonAccentBrush     win.HBRUSH
	buttonBorderPen       win.HPEN
	buttonAccentBorderPen win.HPEN
	font                  win.HFONT
	appIcon               win.HICON
	trayAdded             bool

	// Tabs.
	tabBaseBtn    win.HWND
	tabOpsBtn     win.HWND
	tabAdblockBtn win.HWND
	tabThemeBtn   win.HWND

	// Base panel.
	summaryQueued  win.HWND
	summaryRunning win.HWND
	summaryDone    win.HWND
	summaryError   win.HWND
	urlEdit        win.HWND
	addBtn         win.HWND
	clearBtn       win.HWND
	taskList       win.HWND
	pauseBtn       win.HWND
	resumeBtn      win.HWND
	retryBtn       win.HWND
	cancelBtn      win.HWND
	logEdit        win.HWND
	statusBar      win.HWND
	footerBar      win.HWND
	taskListHeader win.HWND

	// Ops panel.
	opsStatus1 win.HWND
	opsStatus2 win.HWND

	// Adblock panel.
	adblockStatus  win.HWND
	adblockSource  win.HWND
	adblockPattern win.HWND
	adblockUpdated win.HWND

	// Theme panel.
	themeStatus1 win.HWND
	themeStatus2 win.HWND

	activeTab string

	selectedTaskID int
	rowTaskIDs     []int
	taskRowSigs    map[int]string

	lastTaskSig    string
	lastLogSig     string
	lastMetaSig    string
	lastAdblockSig string

	refreshSeq     atomic.Uint64
	refreshPending atomic.Bool
}

func runNativeUI(manager *Manager, persisted *persistedAppState) error {
	runtime.LockOSThread()

	ui := &nativeUI{
		manager:   manager,
		hInstance: win.GetModuleHandle(nil),
		activeTab: "base",
	}

	if err := ui.initCommonControls(); err != nil {
		return err
	}
	if err := ui.registerWindowClass(); err != nil {
		return err
	}
	if err := ui.createWindow(); err != nil {
		return err
	}

	setAppRefreshHook(ui.requestRefresh)
	win.ShowWindow(ui.hwnd, win.SW_SHOW)
	win.UpdateWindow(ui.hwnd)
	ui.requestRefresh()

	var msg win.MSG
	for {
		r := win.GetMessage(&msg, 0, 0, 0)
		if r == 0 {
			break
		}
		if r < 0 {
			return fmt.Errorf("GetMessage failed")
		}
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}

	return nil
}

func (ui *nativeUI) initCommonControls() error {
	icc := win.INITCOMMONCONTROLSEX{
		DwSize: uint32(unsafe.Sizeof(win.INITCOMMONCONTROLSEX{})),
		DwICC:  win.ICC_STANDARD_CLASSES | win.ICC_LISTVIEW_CLASSES,
	}
	if !win.InitCommonControlsEx(&icc) {
		bootstrapTrace("InitCommonControlsEx failed, continuing")
	}
	return nil
}

func (ui *nativeUI) registerWindowClass() error {
	bg := win.CreateBrushIndirect(&win.LOGBRUSH{
		LbStyle: win.BS_SOLID,
		LbColor: themeWindowBg,
	})
	ui.bgBrush = bg
	ui.panelBrush = win.CreateBrushIndirect(&win.LOGBRUSH{
		LbStyle: win.BS_SOLID,
		LbColor: themePanelBg,
	})
	ui.accentBrush = win.CreateBrushIndirect(&win.LOGBRUSH{
		LbStyle: win.BS_SOLID,
		LbColor: themeAccentColor,
	})
	ui.buttonBrush = win.CreateBrushIndirect(&win.LOGBRUSH{
		LbStyle: win.BS_SOLID,
		LbColor: themePanelAltBg,
	})
	ui.buttonAccentBrush = win.CreateBrushIndirect(&win.LOGBRUSH{
		LbStyle: win.BS_SOLID,
		LbColor: themeAccentColor,
	})
	ui.buttonBorderPen = win.ExtCreatePen(
		win.PS_GEOMETRIC|win.PS_SOLID|win.PS_ENDCAP_FLAT|win.PS_JOIN_MITER,
		1,
		&win.LOGBRUSH{LbStyle: win.BS_SOLID, LbColor: themeBorderColor},
		0,
		nil,
	)
	ui.buttonAccentBorderPen = win.ExtCreatePen(
		win.PS_GEOMETRIC|win.PS_SOLID|win.PS_ENDCAP_FLAT|win.PS_JOIN_MITER,
		1,
		&win.LOGBRUSH{LbStyle: win.BS_SOLID, LbColor: themeAccentHoverColor},
		0,
		nil,
	)
	ui.font = ui.createUIFont()
	if icon, err := ui.loadAppIcon(); err == nil {
		ui.appIcon = icon
	}
	icon := ui.appIcon
	if icon == 0 {
		icon = win.LoadIcon(0, resourcePtr(win.IDI_APPLICATION))
	}

	wndClass := win.WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
		Style:         win.CS_HREDRAW | win.CS_VREDRAW,
		LpfnWndProc:   nativeWndProcCallback,
		CbClsExtra:    0,
		CbWndExtra:    0,
		HInstance:     ui.hInstance,
		HIcon:         icon,
		HCursor:       win.LoadCursor(0, resourcePtr(win.IDC_ARROW)),
		HbrBackground: ui.bgBrush,
		LpszMenuName:  nil,
		LpszClassName: nativeClassName,
		HIconSm:       icon,
	}

	if atom := win.RegisterClassEx(&wndClass); atom == 0 {
		return fmt.Errorf("RegisterClassEx failed")
	}
	return nil
}

func (ui *nativeUI) createWindow() error {
	title, _ := syscall.UTF16PtrFromString("漫画下载器")
	hwnd := win.CreateWindowEx(
		0,
		nativeClassName,
		title,
		win.WS_OVERLAPPEDWINDOW|win.WS_VISIBLE|win.WS_CLIPCHILDREN,
		win.CW_USEDEFAULT,
		win.CW_USEDEFAULT,
		1280,
		860,
		0,
		0,
		ui.hInstance,
		unsafe.Pointer(ui),
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx failed")
	}
	ui.hwnd = hwnd
	ui.applyWindowChrome()
	return nil
}

func nativeWndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case win.WM_NCCREATE:
		create := (*win.CREATESTRUCT)(unsafe.Pointer(lParam))
		win.SetWindowLongPtr(hwnd, win.GWLP_USERDATA, create.CreateParams)
		return 1
	}

	ui := nativeUIFromHWND(hwnd)
	switch msg {
	case win.WM_CREATE:
		if ui != nil {
			ui.onCreate(hwnd)
		}
		return 0
	case win.WM_SIZE:
		if ui != nil {
			ui.layout()
		}
		return 0
	case win.WM_TIMER:
		if ui != nil && wParam == nativeTimerID {
			ui.requestRefresh()
		}
		return 0
	case nativeMsgRefresh:
		if ui != nil {
			ui.handleRefreshMessage()
		}
		return 0
	case nativeMsgTray:
		if ui != nil {
			ui.onTrayMessage(lParam)
		}
		return 0
	case win.WM_COMMAND:
		log.Printf("ui wndproc WM_COMMAND wParam=%#x lParam=%#x hasUI=%t", wParam, lParam, ui != nil)
		if ui != nil {
			ui.onCommand(wParam, lParam)
		}
		return 0
	case win.WM_NOTIFY:
		if ui != nil {
			return ui.onNotify(lParam)
		}
		return 0
	case win.WM_CTLCOLORSTATIC, win.WM_CTLCOLOREDIT, win.WM_CTLCOLORBTN:
		if ui != nil {
			hdc := win.HDC(wParam)
			if msg == win.WM_CTLCOLOREDIT {
				win.SetBkMode(hdc, win.OPAQUE)
				win.SetBkColor(hdc, themePanelAltBg)
				win.SetTextColor(hdc, themeTextColor)
				return uintptr(ui.buttonBrush)
			} else {
				win.SetBkMode(hdc, win.TRANSPARENT)
			}
			win.SetTextColor(hdc, themeTextColor)
			return uintptr(ui.panelBrush)
		}
		return 0
	case win.WM_DRAWITEM:
		log.Printf("ui wndproc WM_DRAWITEM wParam=%#x lParam=%#x hasUI=%t", wParam, lParam, ui != nil)
		if ui != nil {
			return ui.onDrawItem(lParam)
		}
		return 0
	case win.WM_DESTROY:
		if ui != nil {
			win.KillTimer(hwnd, nativeTimerID)
			ui.removeTrayIcon()
			ui.destroy()
			setAppRefreshHook(nil)
		}
		win.PostQuitMessage(0)
		return 0
	}

	return win.DefWindowProc(hwnd, msg, wParam, lParam)
}

func nativeUIFromHWND(hwnd win.HWND) *nativeUI {
	ptr := win.GetWindowLongPtr(hwnd, win.GWLP_USERDATA)
	if ptr == 0 {
		return nil
	}
	return (*nativeUI)(unsafe.Pointer(ptr))
}

func (ui *nativeUI) onCreate(hwnd win.HWND) {
	ui.hwnd = hwnd
	if ui.taskRowSigs == nil {
		ui.taskRowSigs = make(map[int]string)
	}
	ui.createControls()
	ui.applyThemeToChildren()
	ui.applyWindowChrome()
	ui.installTrayIcon()
	ui.showTab("base")
	ui.layout()
	ui.requestRefresh()
}

func (ui *nativeUI) createControls() {
	ui.tabBaseBtn = ui.createButton(12, 12, 100, 30, "基础设置", tabBase)
	ui.tabOpsBtn = ui.createButton(116, 12, 100, 30, "运行维护", tabOps)
	ui.tabAdblockBtn = ui.createButton(220, 12, 100, 30, "广告拦截", tabAdblock)
	ui.tabThemeBtn = ui.createButton(324, 12, 100, 30, "主题", tabTheme)

	ui.urlEdit = ui.createEdit(12, 56, 980, 32, "")
	ui.addBtn = ui.createButton(1000, 56, 120, 32, "添加任务", btnAddTask)
	ui.clearBtn = ui.createButton(1126, 56, 120, 32, "清理缓存", btnClearDone)

	ui.taskList = ui.createListView(12, 102, 1234, 290)
	ui.pauseBtn = ui.createButton(12, 402, 110, 30, "暂停", btnPauseTask)
	ui.resumeBtn = ui.createButton(128, 402, 110, 30, "继续", btnResumeTask)
	ui.retryBtn = ui.createButton(244, 402, 110, 30, "重试", btnRetryTask)
	ui.cancelBtn = ui.createButton(360, 402, 110, 30, "取消", btnCancelTask)
	ui.logEdit = ui.createLogEdit(12, 442, 1234, 268, "")
	ui.statusBar = ui.createStatic(12, 718, 980, 20, "就绪")
	ui.footerBar = ui.createStatic(1000, 718, 246, 20, appVersionLabel())

	ui.opsStatus1 = ui.createStatic(20, 62, 1160, 28, "任务维护：后续可接入批量重试、历史导出、日志清理等功能。")
	ui.opsStatus2 = ui.createStatic(20, 94, 1160, 28, "程序状态：展示运行中任务数量、队列状态和后端健康情况。")

	ui.adblockStatus = ui.createStatic(20, 62, 1160, 28, "广告拦截状态会在这里显示。")
	ui.adblockSource = ui.createStatic(20, 94, 1160, 28, "规则源：-")
	ui.adblockPattern = ui.createStatic(20, 126, 1160, 28, "规则数量：0")
	ui.adblockUpdated = ui.createStatic(20, 158, 1160, 28, "最近更新：-")

	ui.themeStatus1 = ui.createStatic(20, 62, 1160, 28, "当前固定为深色桌面风格，后续再补主题切换。")
	ui.themeStatus2 = ui.createStatic(20, 94, 1160, 28, "窗口外观保留桌面应用感，但控件由原生 Win32 承担。")

	ui.setListColumns()
	ui.refreshSelectionState()
}

func (ui *nativeUI) applyThemeToChildren() {
	controls := []win.HWND{
		ui.tabBaseBtn, ui.tabOpsBtn, ui.tabAdblockBtn, ui.tabThemeBtn,
		ui.urlEdit, ui.addBtn, ui.clearBtn,
		ui.taskList, ui.pauseBtn, ui.resumeBtn, ui.retryBtn, ui.cancelBtn,
		ui.logEdit, ui.statusBar, ui.footerBar,
		ui.opsStatus1, ui.opsStatus2,
		ui.adblockStatus, ui.adblockSource, ui.adblockPattern, ui.adblockUpdated,
		ui.themeStatus1, ui.themeStatus2,
	}
	for _, h := range controls {
		if h != 0 {
			win.SetWindowTheme(h, nativeEmptyString, nativeEmptyString)
			if ui.font != 0 {
				win.SendMessage(h, win.WM_SETFONT, uintptr(ui.font), 1)
			}
		}
	}
	if ui.taskList != 0 && ui.font != 0 {
		header := win.HWND(win.SendMessage(ui.taskList, win.LVM_GETHEADER, 0, 0))
		if header != 0 {
			win.SendMessage(header, win.WM_SETFONT, uintptr(ui.font), 1)
		}
	}
}

func (ui *nativeUI) createButton(x, y, w, h int32, text string, id int32) win.HWND {
	ptr, _ := syscall.UTF16PtrFromString(text)
	hwnd := win.CreateWindowEx(
		0,
		nativeButtonClass,
		ptr,
		win.WS_CHILD|win.WS_VISIBLE|win.WS_TABSTOP|win.BS_OWNERDRAW|win.BS_PUSHBUTTON,
		x, y, w, h,
		ui.hwnd,
		win.HMENU(id),
		ui.hInstance,
		nil,
	)
	if hwnd != 0 {
		win.SetWindowTheme(hwnd, nativeEmptyString, nativeEmptyString)
	}
	return hwnd
}

func (ui *nativeUI) createEdit(x, y, w, h int32, text string) win.HWND {
	ptr, _ := syscall.UTF16PtrFromString(text)
	style := uint32(win.WS_CHILD | win.WS_VISIBLE | win.WS_BORDER | win.ES_AUTOHSCROLL)
	hwnd := win.CreateWindowEx(
		win.WS_EX_CLIENTEDGE,
		nativeEditClass,
		ptr,
		style,
		x, y, w, h,
		ui.hwnd,
		0,
		ui.hInstance,
		nil,
	)
	if hwnd != 0 {
		win.SetWindowTheme(hwnd, nativeEmptyString, nativeEmptyString)
	}
	return hwnd
}

func (ui *nativeUI) createLogEdit(x, y, w, h int32, text string) win.HWND {
	ptr, _ := syscall.UTF16PtrFromString(text)
	style := uint32(win.WS_CHILD | win.WS_VISIBLE | win.WS_BORDER | win.ES_MULTILINE | win.ES_AUTOVSCROLL | win.ES_READONLY | win.ES_WANTRETURN | win.WS_VSCROLL | win.WS_HSCROLL)
	hwnd := win.CreateWindowEx(
		win.WS_EX_CLIENTEDGE,
		nativeEditClass,
		ptr,
		style,
		x, y, w, h,
		ui.hwnd,
		0,
		ui.hInstance,
		nil,
	)
	if hwnd != 0 {
		win.SetWindowTheme(hwnd, nativeEmptyString, nativeEmptyString)
		if ui.font != 0 {
			win.SendMessage(hwnd, win.WM_SETFONT, uintptr(ui.font), 1)
		}
	}
	return hwnd
}

func (ui *nativeUI) createStatic(x, y, w, h int32, text string) win.HWND {
	ptr, _ := syscall.UTF16PtrFromString(text)
	hwnd := win.CreateWindowEx(
		0,
		nativeStaticClass,
		ptr,
		win.WS_CHILD|win.WS_VISIBLE,
		x, y, w, h,
		ui.hwnd,
		0,
		ui.hInstance,
		nil,
	)
	if hwnd != 0 {
		win.SetWindowTheme(hwnd, nativeEmptyString, nativeEmptyString)
	}
	return hwnd
}

func (ui *nativeUI) createListView(x, y, w, h int32) win.HWND {
	style := uint32(win.WS_CHILD | win.WS_VISIBLE | win.WS_BORDER | win.LVS_REPORT | win.LVS_SINGLESEL | win.LVS_SHOWSELALWAYS)
	hwnd := win.CreateWindowEx(
		win.WS_EX_CLIENTEDGE,
		nativeListViewClass,
		nil,
		style,
		x, y, w, h,
		ui.hwnd,
		0,
		ui.hInstance,
		nil,
	)
	if hwnd != 0 {
		win.SetWindowTheme(hwnd, nativeEmptyString, nativeEmptyString)
		win.SendMessage(hwnd, win.LVM_SETEXTENDEDLISTVIEWSTYLE, 0, uintptr(win.LVS_EX_FULLROWSELECT|win.LVS_EX_GRIDLINES|win.LVS_EX_DOUBLEBUFFER))
		win.SendMessage(hwnd, win.LVM_SETTEXTCOLOR, 0, uintptr(themeTextColor))
		win.SendMessage(hwnd, win.LVM_SETTEXTBKCOLOR, 0, uintptr(themePanelBg))
		win.SendMessage(hwnd, win.LVM_SETBKCOLOR, 0, uintptr(themePanelBg))
		ui.taskListHeader = win.HWND(win.SendMessage(hwnd, win.LVM_GETHEADER, 0, 0))
		if ui.taskListHeader != 0 && ui.font != 0 {
			win.SendMessage(ui.taskListHeader, win.WM_SETFONT, uintptr(ui.font), 1)
		}
	}
	return hwnd
}

func (ui *nativeUI) setListColumns() {
	columns := []struct {
		title string
		width int32
	}{
		{"编号", 60},
		{"状态", 96},
		{"进度", 84},
		{"标题", 260},
		{"链接", 720},
	}
	for i, col := range columns {
		titlePtr, _ := syscall.UTF16PtrFromString(col.title)
		lvc := win.LVCOLUMN{
			Mask:    win.LVCF_TEXT | win.LVCF_WIDTH,
			Fmt:     win.LVCFMT_LEFT,
			Cx:      col.width,
			PszText: titlePtr,
		}
		win.SendMessage(ui.taskList, win.LVM_INSERTCOLUMN, uintptr(i), uintptr(unsafe.Pointer(&lvc)))
	}
}

func (ui *nativeUI) onCommand(wParam, lParam uintptr) {
	id := int32(uint16(wParam & 0xFFFF))
	notify := int32(uint16((wParam >> 16) & 0xFFFF))
	log.Printf("ui command received id=%d notify=%d hwnd=%d", id, notify, lParam)

	if notify == win.BN_CLICKED {
		switch id {
		case tabBase:
			ui.showTab("base")
		case tabOps:
			ui.showTab("ops")
		case tabAdblock:
			ui.showTab("adblock")
		case tabTheme:
			ui.showTab("theme")
		case btnAddTask:
			ui.addTaskFromInput()
		case btnClearDone:
			ui.manager.ClearCompleted()
			ui.requestRefresh()
		case btnPauseTask:
			ui.taskAction("pause")
		case btnResumeTask:
			ui.taskAction("resume")
		case btnRetryTask:
			ui.taskAction("retry")
		case btnCancelTask:
			ui.taskAction("cancel")
		}
		return
	}

	_ = lParam
}

func (ui *nativeUI) onNotify(lParam uintptr) uintptr {
	if lParam == 0 {
		return 0
	}
	hdr := (*win.NMHDR)(unsafe.Pointer(lParam))
	if hdr == nil {
		return 0
	}
	if hdr.HwndFrom == ui.taskListHeader {
		if hdr.Code == win.NM_CUSTOMDRAW {
			return ui.headerCustomDraw(lParam)
		}
		return 0
	}
	if hdr.HwndFrom != ui.taskList {
		return 0
	}
	switch hdr.Code {
	case win.LVN_ITEMCHANGED:
		ui.syncSelectedTask()
	case win.NM_CUSTOMDRAW:
		return ui.taskListCustomDraw(lParam)
	}
	return 0
}

func (ui *nativeUI) showTab(tab string) {
	ui.activeTab = tab

	base := []win.HWND{
		ui.urlEdit, ui.addBtn, ui.clearBtn, ui.taskList,
		ui.pauseBtn, ui.resumeBtn, ui.retryBtn, ui.cancelBtn,
		ui.logEdit, ui.statusBar, ui.footerBar,
	}
	ops := []win.HWND{ui.opsStatus1, ui.opsStatus2}
	adblock := []win.HWND{ui.adblockStatus, ui.adblockSource, ui.adblockPattern, ui.adblockUpdated}
	theme := []win.HWND{ui.themeStatus1, ui.themeStatus2}

	ui.setGroupVisible(base, tab == "base")
	ui.setGroupVisible(ops, tab == "ops")
	ui.setGroupVisible(adblock, tab == "adblock")
	ui.setGroupVisible(theme, tab == "theme")
	ui.layout()
}

func (ui *nativeUI) setGroupVisible(handles []win.HWND, visible bool) {
	cmd := int32(win.SW_HIDE)
	if visible {
		cmd = win.SW_SHOW
	}
	for _, h := range handles {
		if h != 0 {
			win.ShowWindow(h, cmd)
		}
	}
}

func (ui *nativeUI) layout() {
	var rc win.RECT
	if !win.GetClientRect(ui.hwnd, &rc) {
		return
	}
	width := rc.Right - rc.Left
	height := rc.Bottom - rc.Top

	tabsY := int32(12)
	panelTop := int32(52)
	margin := int32(12)
	gap := int32(8)
	controlH := int32(34)

	tabWidth := int32(100)
	tabGap := int32(4)
	tabX := margin
	for _, h := range []win.HWND{ui.tabBaseBtn, ui.tabOpsBtn, ui.tabAdblockBtn, ui.tabThemeBtn} {
		if h != 0 {
			win.MoveWindow(h, tabX, tabsY, tabWidth, 28, true)
			tabX += tabWidth + tabGap
		}
	}

	if ui.activeTab == "base" {
		editW := width - margin*2 - 236
		if editW < 220 {
			editW = 220
		}
		win.MoveWindow(ui.urlEdit, margin, panelTop, editW, controlH, true)
		win.MoveWindow(ui.addBtn, margin+editW+gap, panelTop, 110, controlH, true)
		win.MoveWindow(ui.clearBtn, margin+editW+gap+110+gap, panelTop, 110, controlH, true)

		listTop := panelTop + controlH + 14
		listH := height - listTop - 222
		if listH < 160 {
			listH = 160
		}
		win.MoveWindow(ui.taskList, margin, listTop, width-margin*2, listH, true)

		actionY := listTop + listH + 10
		win.MoveWindow(ui.pauseBtn, margin, actionY, 96, 30, true)
		win.MoveWindow(ui.resumeBtn, margin+100, actionY, 96, 30, true)
		win.MoveWindow(ui.retryBtn, margin+200, actionY, 96, 30, true)
		win.MoveWindow(ui.cancelBtn, margin+300, actionY, 96, 30, true)

		logTop := actionY + 38
		logH := height - logTop - 40
		if logH < 140 {
			logH = 140
		}
		win.MoveWindow(ui.logEdit, margin, logTop, width-margin*2, logH, true)
		win.MoveWindow(ui.statusBar, margin, height-26, width-290, 18, true)
		win.MoveWindow(ui.footerBar, width-260, height-26, 248, 18, true)
	} else {
		innerTop := panelTop
		innerW := width - margin*2
		switch ui.activeTab {
		case "ops":
			win.MoveWindow(ui.opsStatus1, margin, innerTop, innerW, 28, true)
			win.MoveWindow(ui.opsStatus2, margin, innerTop+34, innerW, 28, true)
		case "adblock":
			win.MoveWindow(ui.adblockStatus, margin, innerTop, innerW, 28, true)
			win.MoveWindow(ui.adblockSource, margin, innerTop+34, innerW, 28, true)
			win.MoveWindow(ui.adblockPattern, margin, innerTop+68, innerW, 28, true)
			win.MoveWindow(ui.adblockUpdated, margin, innerTop+102, innerW, 28, true)
		case "theme":
			win.MoveWindow(ui.themeStatus1, margin, innerTop, innerW, 28, true)
			win.MoveWindow(ui.themeStatus2, margin, innerTop+34, innerW, 28, true)
		}
	}
}

func (ui *nativeUI) requestRefresh() {
	ui.refreshSeq.Add(1)
	if ui.hwnd == 0 {
		return
	}
	if ui.refreshPending.CompareAndSwap(false, true) {
		win.PostMessage(ui.hwnd, nativeMsgRefresh, 0, 0)
	}
}

func (ui *nativeUI) handleRefreshMessage() {
	start := ui.refreshSeq.Load()
	ui.refreshPending.Store(false)
	ui.refresh()
	if ui.refreshSeq.Load() != start {
		ui.requestRefresh()
	}
}

func (ui *nativeUI) refresh() {
	snapshot := ui.manager.Snapshot()
	ui.refreshTasks(snapshot)
	ui.refreshLogs()
	ui.refreshAdblock()
	ui.refreshMeta(snapshot)
}

func (ui *nativeUI) refreshMeta(state AppState) {
	sig := fmt.Sprintf("%d|%d|%d|%d|%d", state.Queue, state.Counts.Running, state.Counts.Done, state.Counts.Error, state.Counts.Paused)
	if sig == ui.lastMetaSig {
		return
	}
	ui.lastMetaSig = sig
	status := fmt.Sprintf("就绪 | 队列 %d | 运行 %d | 完成 %d | 失败 %d | 暂停 %d", state.Queue, state.Counts.Running, state.Counts.Done, state.Counts.Error, state.Counts.Paused)
	setWindowText(ui.statusBar, status)
	ui.footerBar = ui.createStatic(1000, 718, 246, 20, appVersionLabel())
}

func (ui *nativeUI) refreshLogs() {
	lines := appLogs.Snapshot()
	sig := fmt.Sprintf("%d|%s", len(lines), tailLine(lines))
	if sig == ui.lastLogSig {
		return
	}
	ui.lastLogSig = sig
	if len(lines) == 0 {
		lines = []string{
			"启动信息：",
			fmt.Sprintf("  cwd=%s", mustGetwd()),
			fmt.Sprintf("  exe=%s", mustGetexe()),
			fmt.Sprintf("  args=%s", strings.Join(os.Args, " ")),
			fmt.Sprintf("  projectRoot=%s", projectRootDir()),
			fmt.Sprintf("  runtimeRoot=%s", runtimeRootDir()),
		}
	}
	text := strings.Join(lines, "\r\n")
	setWindowText(ui.logEdit, text)
	win.SendMessage(ui.logEdit, win.EM_SETSEL, ^uintptr(0), ^uintptr(0))
	win.SendMessage(ui.logEdit, win.EM_SCROLLCARET, 0, 0)
}

func (ui *nativeUI) refreshAdblock() {
	status := loadAdblockStatus()
	updated := status.UpdatedAt
	if updated == "" {
		updated = status.Date
	}
	sig := fmt.Sprintf("%t|%s|%d|%s", status.Ready, status.Source, status.PatternCount, updated)
	if sig == ui.lastAdblockSig {
		return
	}
	ui.lastAdblockSig = sig
	if status.Ready {
		setWindowText(ui.adblockStatus, "规则已加载，页面会自动套用拦截列表。")
	} else {
		setWindowText(ui.adblockStatus, "当前未发现可用规则缓存。")
	}
	setWindowText(ui.adblockSource, fmt.Sprintf("规则源：%s", blankIfEmpty(status.Source)))
	setWindowText(ui.adblockPattern, fmt.Sprintf("规则数量：%d", status.PatternCount))
	setWindowText(ui.adblockUpdated, fmt.Sprintf("最近更新：%s", blankIfEmpty(updated)))
}

func (ui *nativeUI) refreshTasks(state AppState) {
	layoutSig := taskLayoutSignature(state.Tasks)
	log.Printf("ui refresh tasks count=%d layoutChanged=%t lastRows=%d", len(state.Tasks), layoutSig != ui.lastTaskSig, len(ui.rowTaskIDs))
	if layoutSig != ui.lastTaskSig || len(ui.rowTaskIDs) != len(state.Tasks) {
		ui.lastTaskSig = layoutSig
		ui.rebuildTaskList(state.Tasks)
		ui.restoreSelection()
		ui.refreshSelectionState()
		return
	}

	updated := false
	win.SendMessage(ui.taskList, win.WM_SETREDRAW, 0, 0)
	for row, task := range state.Tasks {
		if ui.taskRowSigs == nil {
			ui.taskRowSigs = make(map[int]string)
		}
		sig := taskRowSignature(task)
		if ui.taskRowSigs[task.ID] == sig {
			continue
		}
		ui.updateTaskRow(row, task)
		ui.taskRowSigs[task.ID] = sig
		updated = true
	}
	win.SendMessage(ui.taskList, win.WM_SETREDRAW, 1, 0)
	if updated {
		win.InvalidateRect(ui.taskList, nil, true)
	}
	if updated {
		log.Printf("ui refresh tasks updated rows=%d", len(state.Tasks))
	}
	ui.refreshSelectionState()
}

func (ui *nativeUI) rebuildTaskList(tasks []*Task) {
	log.Printf("ui rebuild task list rows=%d", len(tasks))
	ui.rowTaskIDs = ui.rowTaskIDs[:0]
	if ui.taskRowSigs == nil {
		ui.taskRowSigs = make(map[int]string)
	} else {
		for k := range ui.taskRowSigs {
			delete(ui.taskRowSigs, k)
		}
	}
	win.SendMessage(ui.taskList, win.WM_SETREDRAW, 0, 0)
	win.SendMessage(ui.taskList, win.LVM_DELETEALLITEMS, 0, 0)
	for row, task := range tasks {
		ui.rowTaskIDs = append(ui.rowTaskIDs, task.ID)
		ui.insertTaskRow(row, task)
		ui.taskRowSigs[task.ID] = taskRowSignature(task)
	}
	win.SendMessage(ui.taskList, win.WM_SETREDRAW, 1, 0)
	win.InvalidateRect(ui.taskList, nil, true)
}

func (ui *nativeUI) insertTaskRow(row int, task *Task) {
	percent := int(task.Percent * 100)
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	columns := []string{
		fmt.Sprintf("%d", task.ID),
		string(task.State),
		fmt.Sprintf("%d%%", percent),
		blankIfEmpty(task.Title),
		blankIfEmpty(task.URL),
	}
	for i, text := range columns {
		ptr, _ := syscall.UTF16PtrFromString(text)
		item := win.LVITEM{
			Mask:     win.LVIF_TEXT,
			IItem:    int32(row),
			ISubItem: int32(i),
			PszText:  ptr,
		}
		if i == 0 {
			win.SendMessage(ui.taskList, win.LVM_INSERTITEM, 0, uintptr(unsafe.Pointer(&item)))
		} else {
			win.SendMessage(ui.taskList, win.LVM_SETITEMTEXT, uintptr(row), uintptr(unsafe.Pointer(&item)))
		}
	}
}

func (ui *nativeUI) updateTaskRow(row int, task *Task) {
	percent := int(task.Percent * 100)
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	columns := []string{
		fmt.Sprintf("%d", task.ID),
		string(task.State),
		fmt.Sprintf("%d%%", percent),
		blankIfEmpty(task.Title),
		blankIfEmpty(task.URL),
	}
	for i, text := range columns {
		ptr, _ := syscall.UTF16PtrFromString(text)
		item := win.LVITEM{
			Mask:     win.LVIF_TEXT,
			IItem:    int32(row),
			ISubItem: int32(i),
			PszText:  ptr,
		}
		win.SendMessage(ui.taskList, win.LVM_SETITEMTEXT, uintptr(row), uintptr(unsafe.Pointer(&item)))
	}
}

func (ui *nativeUI) restoreSelection() {
	if ui.selectedTaskID == 0 {
		return
	}
	for idx, id := range ui.rowTaskIDs {
		if id != ui.selectedTaskID {
			continue
		}
		item := win.LVITEM{
			StateMask: win.LVIS_SELECTED | win.LVIS_FOCUSED,
			State:     win.LVIS_SELECTED | win.LVIS_FOCUSED,
		}
		win.SendMessage(ui.taskList, win.LVM_SETITEMSTATE, uintptr(idx), uintptr(unsafe.Pointer(&item)))
		win.SendMessage(ui.taskList, win.LVM_ENSUREVISIBLE, uintptr(idx), 1)
		break
	}
}

func (ui *nativeUI) syncSelectedTask() {
	index := int(win.SendMessage(ui.taskList, win.LVM_GETNEXTITEM, ^uintptr(0), uintptr(win.LVNI_SELECTED)))
	if index < 0 || index >= len(ui.rowTaskIDs) {
		ui.selectedTaskID = 0
		ui.refreshSelectionState()
		return
	}
	ui.selectedTaskID = ui.rowTaskIDs[index]
	log.Printf("ui selected task id=%d row=%d", ui.selectedTaskID, index)
	ui.refreshSelectionState()
}

func (ui *nativeUI) refreshSelectionState() {
	var task *Task
	if ui.selectedTaskID != 0 {
		if copyTask, ok := ui.manager.taskCopy(ui.selectedTaskID); ok {
			task = &copyTask
		}
	}
	win.EnableWindow(ui.pauseBtn, canTaskAction(task, "pause"))
	win.EnableWindow(ui.resumeBtn, canTaskAction(task, "resume"))
	win.EnableWindow(ui.retryBtn, canTaskAction(task, "retry"))
	win.EnableWindow(ui.cancelBtn, canTaskAction(task, "cancel"))
}

func (ui *nativeUI) taskListCustomDraw(lParam uintptr) uintptr {
	draw := (*win.NMLVCUSTOMDRAW)(unsafe.Pointer(lParam))
	if draw == nil {
		return win.CDRF_DODEFAULT
	}

	stage := draw.Nmcd.DwDrawStage
	switch stage {
	case win.CDDS_PREPAINT:
		return win.CDRF_NOTIFYITEMDRAW
	case win.CDDS_ITEMPREPAINT:
		return win.CDRF_NOTIFYSUBITEMDRAW
	}

	if stage&win.CDDS_ITEM == 0 || stage&win.CDDS_SUBITEM == 0 {
		return win.CDRF_DODEFAULT
	}

	row := int(draw.Nmcd.DwItemSpec)
	state, ok := ui.taskStateForRow(row)
	if !ok {
		state = TaskQueued
	}
	selected := (draw.Nmcd.UItemState & win.CDIS_SELECTED) != 0
	evenRow := row%2 == 0
	textColor, backColor := ui.taskPalette(state, selected, evenRow)
	subitem := int(draw.ISubItem)
	rc := draw.RcText
	if rc.Right == 0 && rc.Bottom == 0 {
		rc = draw.Nmcd.Rc
	}
	ui.fillRect(win.HDC(draw.Nmcd.Hdc), rc, backColor)
	win.SetBkMode(win.HDC(draw.Nmcd.Hdc), win.TRANSPARENT)
	win.SetTextColor(win.HDC(draw.Nmcd.Hdc), textColor)
	task, ok := ui.manager.taskCopy(ui.rowTaskIDs[row])
	if !ok {
		return win.CDRF_SKIPDEFAULT
	}
	label := ui.taskCellText(task, subitem)
	text, _ := syscall.UTF16PtrFromString(label)
	textRc := rc
	textRc.Left += 8
	textRc.Right -= 8
	if subitem == 2 {
		ui.drawProgressCell(win.HDC(draw.Nmcd.Hdc), textRc, task.Percent, selected)
	} else {
		win.DrawTextEx(win.HDC(draw.Nmcd.Hdc), text, -1, &textRc, win.DT_LEFT|win.DT_VCENTER|win.DT_SINGLELINE|win.DT_END_ELLIPSIS|win.DT_NOPREFIX, nil)
	}
	return win.CDRF_SKIPDEFAULT
}

func (ui *nativeUI) headerCustomDraw(lParam uintptr) uintptr {
	draw := (*win.NMCUSTOMDRAW)(unsafe.Pointer(lParam))
	if draw == nil {
		return win.CDRF_DODEFAULT
	}
	switch draw.DwDrawStage {
	case win.CDDS_PREPAINT:
		return win.CDRF_NOTIFYITEMDRAW
	case win.CDDS_ITEMPREPAINT:
	}
	if draw.DwDrawStage&win.CDDS_ITEM == 0 {
		return win.CDRF_DODEFAULT
	}
	rc := draw.Rc
	ui.fillRect(win.HDC(draw.Hdc), rc, themePanelBg)
	win.SetBkMode(win.HDC(draw.Hdc), win.TRANSPARENT)
	win.SetTextColor(win.HDC(draw.Hdc), themeMutedTextColor)
	title := ui.headerTitle(int(draw.DwItemSpec))
	if title != "" {
		text, _ := syscall.UTF16PtrFromString(title)
		textRc := rc
		textRc.Left += 8
		textRc.Right -= 8
		win.DrawTextEx(win.HDC(draw.Hdc), text, -1, &textRc, win.DT_LEFT|win.DT_VCENTER|win.DT_SINGLELINE|win.DT_END_ELLIPSIS|win.DT_NOPREFIX, nil)
	}
	return win.CDRF_SKIPDEFAULT
}

func (ui *nativeUI) fillRect(hdc win.HDC, rc win.RECT, color win.COLORREF) {
	if hdc == 0 {
		return
	}
	brush := win.CreateBrushIndirect(&win.LOGBRUSH{LbStyle: win.BS_SOLID, LbColor: color})
	if brush == 0 {
		return
	}
	oldBrush := win.SelectObject(hdc, win.HGDIOBJ(brush))
	oldPen := win.SelectObject(hdc, win.GetStockObject(win.NULL_PEN))
	win.Rectangle_(hdc, rc.Left, rc.Top, rc.Right, rc.Bottom)
	win.SelectObject(hdc, oldPen)
	win.SelectObject(hdc, oldBrush)
	win.DeleteObject(win.HGDIOBJ(brush))
}

func (ui *nativeUI) drawProgressCell(hdc win.HDC, rc win.RECT, percent float64, selected bool) {
	if percent < 0 {
		percent = 0
	}
	if percent > 1 {
		percent = 1
	}
	track := rc
	track.Top = track.Bottom - 7
	if track.Top < rc.Top+4 {
		track.Top = rc.Top + 4
	}
	track.Bottom = track.Top + 3
	if selected {
		ui.fillRect(hdc, track, themePanelAltBg)
	} else {
		ui.fillRect(hdc, track, themePanelBg)
	}
	fill := track
	fill.Right = fill.Left + int32(float64(track.Right-track.Left)*percent)
	if fill.Right > fill.Left {
		ui.fillRect(hdc, fill, themeAccentColor)
	}
	label := fmt.Sprintf("%d%%", int(percent*100))
	text, _ := syscall.UTF16PtrFromString(label)
	textRc := rc
	textRc.Left += 8
	textRc.Right -= 8
	textRc.Bottom = track.Top - 2
	if textRc.Bottom < textRc.Top+12 {
		textRc.Bottom = rc.Bottom
	}
	win.DrawTextEx(hdc, text, -1, &textRc, win.DT_CENTER|win.DT_VCENTER|win.DT_SINGLELINE|win.DT_NOPREFIX, nil)
}

func (ui *nativeUI) taskCellText(task Task, subitem int) string {
	percent := int(task.Percent * 100)
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	switch subitem {
	case 0:
		return fmt.Sprintf("%d", task.ID)
	case 1:
		return string(task.State)
	case 2:
		return fmt.Sprintf("%d%%", percent)
	case 3:
		return blankIfEmpty(task.Title)
	case 4:
		return blankIfEmpty(task.URL)
	default:
		return ""
	}
}

func (ui *nativeUI) headerTitle(index int) string {
	switch index {
	case 0:
		return "编号"
	case 1:
		return "状态"
	case 2:
		return "进度"
	case 3:
		return "标题"
	case 4:
		return "链接"
	default:
		return ""
	}
}

func (ui *nativeUI) taskStateForRow(row int) (TaskState, bool) {
	if row < 0 || row >= len(ui.rowTaskIDs) {
		return "", false
	}
	task, ok := ui.manager.taskCopy(ui.rowTaskIDs[row])
	if !ok {
		return "", false
	}
	return task.State, true
}

func (ui *nativeUI) taskPalette(state TaskState, selected, evenRow bool) (win.COLORREF, win.COLORREF) {
	if selected {
		return themeTextColor, win.RGB(34, 46, 58)
	}
	switch state {
	case TaskRunning:
		if evenRow {
			return themeTextColor, win.RGB(20, 32, 24)
		}
		return themeTextColor, win.RGB(18, 28, 22)
	case TaskWaitingVerification:
		if evenRow {
			return win.RGB(220, 210, 241), win.RGB(37, 30, 49)
		}
		return win.RGB(220, 210, 241), win.RGB(32, 27, 43)
	case TaskPaused:
		if evenRow {
			return win.RGB(214, 210, 182), win.RGB(40, 39, 24)
		}
		return win.RGB(214, 210, 182), win.RGB(35, 34, 21)
	case TaskDone:
		if evenRow {
			return win.RGB(199, 206, 214), win.RGB(29, 31, 35)
		}
		return win.RGB(199, 206, 214), win.RGB(32, 34, 38)
	case TaskError:
		if evenRow {
			return win.RGB(241, 205, 205), win.RGB(48, 26, 28)
		}
		return win.RGB(241, 205, 205), win.RGB(43, 24, 26)
	default:
		if evenRow {
			return themeTextColor, themePanelBg
		}
		return themeTextColor, themePanelAltBg
	}
}

func (ui *nativeUI) taskAction(action string) {
	if ui.selectedTaskID == 0 {
		return
	}
	switch action {
	case "pause":
		ui.manager.Pause(ui.selectedTaskID)
	case "resume":
		ui.manager.Resume(ui.selectedTaskID)
	case "retry":
		ui.manager.Retry(ui.selectedTaskID)
	case "cancel":
		ui.manager.Cancel(ui.selectedTaskID)
	}
	ui.requestRefresh()
}

func (ui *nativeUI) addTaskFromInput() {
	url := getWindowText(ui.urlEdit)
	url = strings.TrimSpace(url)
	if url == "" {
		log.Printf("ui add task skipped: empty url")
		setWindowText(ui.statusBar, "请输入任务网址")
		return
	}
	log.Printf("ui add task begin url=%s", url)
	headless := defaultHeadlessForRoute(prefilterSiteRoute(url), url)
	task := ui.manager.AddTask(url, "", "", headless, false)
	ui.selectedTaskID = task.ID
	log.Printf("ui add task id=%d url=%s", task.ID, task.URL)
	setWindowText(ui.statusBar, fmt.Sprintf("已添加任务 #%d", task.ID))
	setWindowText(ui.urlEdit, "")
	ui.refresh()
	ui.refreshSelectionState()
	win.SetFocus(ui.taskList)
	ui.requestRefresh()
}

func (ui *nativeUI) destroy() {
	ui.removeTrayIcon()
	if ui.font != 0 {
		win.DeleteObject(win.HGDIOBJ(ui.font))
		ui.font = 0
	}
	if ui.appIcon != 0 {
		win.DestroyIcon(ui.appIcon)
		ui.appIcon = 0
	}
	if ui.bgBrush != 0 {
		win.DeleteObject(win.HGDIOBJ(ui.bgBrush))
		ui.bgBrush = 0
	}
	if ui.panelBrush != 0 {
		win.DeleteObject(win.HGDIOBJ(ui.panelBrush))
		ui.panelBrush = 0
	}
	if ui.accentBrush != 0 {
		win.DeleteObject(win.HGDIOBJ(ui.accentBrush))
		ui.accentBrush = 0
	}
	if ui.buttonBrush != 0 {
		win.DeleteObject(win.HGDIOBJ(ui.buttonBrush))
		ui.buttonBrush = 0
	}
	if ui.buttonAccentBrush != 0 {
		win.DeleteObject(win.HGDIOBJ(ui.buttonAccentBrush))
		ui.buttonAccentBrush = 0
	}
	if ui.buttonBorderPen != 0 {
		win.DeleteObject(win.HGDIOBJ(ui.buttonBorderPen))
		ui.buttonBorderPen = 0
	}
	if ui.buttonAccentBorderPen != 0 {
		win.DeleteObject(win.HGDIOBJ(ui.buttonAccentBorderPen))
		ui.buttonAccentBorderPen = 0
	}
}

func (ui *nativeUI) applyWindowChrome() {
	if ui.hwnd == 0 {
		return
	}
	if ui.appIcon != 0 {
		win.SendMessage(ui.hwnd, win.WM_SETICON, uintptr(nativeIconBig), uintptr(ui.appIcon))
		win.SendMessage(ui.hwnd, win.WM_SETICON, uintptr(nativeIconSmall), uintptr(ui.appIcon))
		win.SendMessage(ui.hwnd, win.WM_SETICON, uintptr(nativeIconSmall2), uintptr(ui.appIcon))
	}
	ui.setDarkTitleBar()
}

func (ui *nativeUI) loadAppIcon() (win.HICON, error) {
	path, err := ensureAppIconFile()
	if err != nil {
		return 0, err
	}
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	handle := win.LoadImage(0, ptr, win.IMAGE_ICON, 0, 0, win.LR_LOADFROMFILE|win.LR_DEFAULTSIZE)
	if handle == 0 {
		return 0, fmt.Errorf("LoadImage failed for %s", path)
	}
	return win.HICON(handle), nil
}

func (ui *nativeUI) setDarkTitleBar() {
	if ui.hwnd == 0 {
		return
	}
	enabled := uint32(1)
	_, _, _ = procDwmSetWindowAttribute.Call(uintptr(ui.hwnd), 20, uintptr(unsafe.Pointer(&enabled)), unsafe.Sizeof(enabled))
	captionColor := uint32(themeWindowBg)
	textColor := uint32(themeTextColor)
	borderColor := uint32(themeBorderColor)
	for _, attr := range []struct {
		id   uintptr
		ptr  *uint32
		size uintptr
	}{
		{34, &borderColor, unsafe.Sizeof(borderColor)},
		{35, &captionColor, unsafe.Sizeof(captionColor)},
		{36, &textColor, unsafe.Sizeof(textColor)},
	} {
		_, _, _ = procDwmSetWindowAttribute.Call(uintptr(ui.hwnd), attr.id, uintptr(unsafe.Pointer(attr.ptr)), attr.size)
	}
}

func (ui *nativeUI) installTrayIcon() {
	if ui.hwnd == 0 || ui.trayAdded || ui.appIcon == 0 {
		return
	}
	nid := win.NOTIFYICONDATA{
		CbSize:           uint32(unsafe.Sizeof(win.NOTIFYICONDATA{})),
		HWnd:             ui.hwnd,
		UID:              trayIconID,
		UFlags:           win.NIF_MESSAGE | win.NIF_ICON | win.NIF_TIP,
		UCallbackMessage: nativeMsgTray,
		HIcon:            ui.appIcon,
	}
	tip, _ := syscall.UTF16FromString("漫画下载器")
	copy(nid.SzTip[:], tip)
	if win.Shell_NotifyIcon(win.NIM_ADD, &nid) {
		ui.trayAdded = true
	}
}

func (ui *nativeUI) removeTrayIcon() {
	if ui.hwnd == 0 || !ui.trayAdded {
		return
	}
	nid := win.NOTIFYICONDATA{
		CbSize: uint32(unsafe.Sizeof(win.NOTIFYICONDATA{})),
		HWnd:   ui.hwnd,
		UID:    trayIconID,
	}
	_ = win.Shell_NotifyIcon(win.NIM_DELETE, &nid)
	ui.trayAdded = false
}

func (ui *nativeUI) onTrayMessage(lParam uintptr) {
	switch uint32(lParam) {
	case win.WM_LBUTTONUP, win.WM_LBUTTONDBLCLK:
		if ui.hwnd != 0 {
			win.ShowWindow(ui.hwnd, win.SW_RESTORE)
			win.SetForegroundWindow(ui.hwnd)
		}
	}
}

func setWindowText(hwnd win.HWND, text string) {
	ptr, _ := syscall.UTF16PtrFromString(text)
	win.SendMessage(hwnd, win.WM_SETTEXT, 0, uintptr(unsafe.Pointer(ptr)))
}

func getWindowText(hwnd win.HWND) string {
	length := int(win.SendMessage(hwnd, win.WM_GETTEXTLENGTH, 0, 0))
	if length <= 0 {
		return ""
	}
	buf := make([]uint16, length+1)
	win.SendMessage(hwnd, win.WM_GETTEXT, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	return syscall.UTF16ToString(buf)
}

func blankIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func taskSignature(state AppState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%d|%d|%d|%d|%d", state.Queue, state.Counts.Queued, state.Counts.Running, state.Counts.Paused, state.Counts.Done, state.Counts.Error)
	for _, task := range state.Tasks {
		fmt.Fprintf(&b, "|%d:%s:%s:%.2f:%s:%s", task.ID, task.State, task.Detail, task.Percent, task.Title, task.URL)
	}
	return b.String()
}

func taskLayoutSignature(tasks []*Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d", len(tasks))
	for _, task := range tasks {
		fmt.Fprintf(&b, "|%d", task.ID)
	}
	return b.String()
}

func taskRowSignature(task *Task) string {
	return fmt.Sprintf("%s|%.2f|%s|%s", task.State, task.Percent, task.Title, task.URL)
}

func (ui *nativeUI) createUIFont() win.HFONT {
	face := comicPreferredUIFontFace()
	lf := win.LOGFONT{
		LfHeight:         -14,
		LfWeight:         win.FW_BOLD,
		LfCharSet:        win.DEFAULT_CHARSET,
		LfOutPrecision:   win.OUT_DEFAULT_PRECIS,
		LfClipPrecision:  win.CLIP_DEFAULT_PRECIS,
		LfQuality:        win.CLEARTYPE_QUALITY,
		LfPitchAndFamily: win.VARIABLE_PITCH | win.FF_DONTCARE,
	}
	copy(lf.LfFaceName[:], syscall.StringToUTF16(face))
	return win.CreateFontIndirect(&lf)
}

func (ui *nativeUI) onDrawItem(lParam uintptr) uintptr {
	dis := (*win.DRAWITEMSTRUCT)(unsafe.Pointer(lParam))
	if dis == nil || dis.CtlType != ownerDrawButton {
		return 0
	}
	return ui.drawButton(dis)
}

func (ui *nativeUI) drawButton(dis *win.DRAWITEMSTRUCT) uintptr {
	hdc := dis.HDC
	rc := dis.RcItem
	id := int32(dis.CtlID)
	disabled := dis.ItemState&win.ODS_DISABLED != 0
	pressed := dis.ItemState&win.ODS_SELECTED != 0
	focused := dis.ItemState&win.ODS_FOCUS != 0
	activeTab := ui.isActiveTabButton(id)
	primary := id == btnAddTask

	var brush win.HBRUSH
	var pen win.HPEN
	textColor := themeTextColor
	switch {
	case disabled:
		brush = ui.buttonBrush
		pen = ui.buttonBorderPen
		textColor = themeMutedTextColor
	case activeTab || primary:
		brush = ui.buttonAccentBrush
		pen = ui.buttonAccentBorderPen
		textColor = themeTextColor
	default:
		if pressed {
			brush = ui.panelBrush
		} else {
			brush = ui.buttonBrush
		}
		pen = ui.buttonBorderPen
	}

	var oldFont win.HGDIOBJ
	if ui.font != 0 {
		oldFont = win.SelectObject(hdc, win.HGDIOBJ(ui.font))
	}
	oldPen := win.SelectObject(hdc, win.HGDIOBJ(pen))
	oldBrush := win.SelectObject(hdc, win.HGDIOBJ(brush))
	win.RoundRect(hdc, rc.Left, rc.Top, rc.Right, rc.Bottom, 8, 8)
	win.SelectObject(hdc, oldBrush)
	win.SelectObject(hdc, oldPen)

	win.SetBkMode(hdc, win.TRANSPARENT)
	win.SetTextColor(hdc, textColor)

	label := ui.buttonTextByID(id)
	if label != "" {
		text, _ := syscall.UTF16PtrFromString(label)
		textRc := rc
		textRc.Left += 10
		textRc.Top += 2
		textRc.Right -= 10
		textRc.Bottom -= 2
		win.DrawTextEx(hdc, text, -1, &textRc, win.DT_CENTER|win.DT_VCENTER|win.DT_SINGLELINE|win.DT_END_ELLIPSIS|win.DT_NOPREFIX, nil)
	}
	if focused {
		focusRc := rc
		focusRc.Left += 3
		focusRc.Top += 3
		focusRc.Right -= 3
		focusRc.Bottom -= 3
		win.DrawFocusRect(hdc, &focusRc)
	}
	if oldFont != 0 {
		win.SelectObject(hdc, oldFont)
	}
	return 1
}

func (ui *nativeUI) buttonTextByID(id int32) string {
	switch id {
	case tabBase:
		return "基础设置"
	case tabOps:
		return "运行维护"
	case tabAdblock:
		return "广告拦截"
	case tabTheme:
		return "主题"
	case btnAddTask:
		return "添加任务"
	case btnClearDone:
		return "清理缓存"
	case btnPauseTask:
		return "暂停"
	case btnResumeTask:
		return "继续"
	case btnRetryTask:
		return "重试"
	case btnCancelTask:
		return "取消"
	default:
		return ""
	}
}

func (ui *nativeUI) isActiveTabButton(id int32) bool {
	switch id {
	case tabBase:
		return ui.activeTab == "base"
	case tabOps:
		return ui.activeTab == "ops"
	case tabAdblock:
		return ui.activeTab == "adblock"
	case tabTheme:
		return ui.activeTab == "theme"
	default:
		return false
	}
}

func tailLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func mustGetexe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func resourcePtr(id uint16) *uint16 {
	return (*uint16)(unsafe.Pointer(uintptr(id)))
}
