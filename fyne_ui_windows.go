//go:build windows && !legacyui

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/lxn/win"
)

const (
	fyneTabBase        = "base"
	taskCardItemHeight = 136
)

var fyneWindowTitle = "\u6f2b\u753b\u4e0b\u8f7d\u5668"
var comicUIFontOnce sync.Once
var comicUIFontRegular fyne.Resource
var comicUIFontBold fyne.Resource
var enumWindowsProc = syscall.NewLazyDLL("user32.dll").NewProc("EnumWindows")

type fyneComicTheme struct {
	base fyne.Theme
}

func (t *fyneComicTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 18, G: 20, B: 24, A: 255}
	case theme.ColorNameButton:
		return color.NRGBA{R: 31, G: 34, B: 40, A: 255}
	case theme.ColorNameDisabled:
		return color.NRGBA{R: 90, G: 94, B: 102, A: 255}
	case theme.ColorNameDisabledButton:
		return color.NRGBA{R: 44, G: 47, B: 54, A: 255}
	case theme.ColorNameError:
		return color.NRGBA{R: 164, G: 79, B: 79, A: 255}
	case theme.ColorNameForeground:
		return color.NRGBA{R: 225, G: 229, B: 235, A: 255}
	case theme.ColorNameForegroundOnError:
		return color.NRGBA{R: 247, G: 247, B: 248, A: 255}
	case theme.ColorNameForegroundOnSuccess:
		return color.NRGBA{R: 235, G: 245, B: 238, A: 255}
	case theme.ColorNameForegroundOnWarning:
		return color.NRGBA{R: 245, G: 241, B: 231, A: 255}
	case theme.ColorNameHover:
		return color.NRGBA{R: 41, G: 46, B: 54, A: 255}
	case theme.ColorNameHeaderBackground:
		return color.NRGBA{R: 24, G: 27, B: 32, A: 255}
	case theme.ColorNameInputBackground:
		return color.NRGBA{R: 23, G: 26, B: 31, A: 255}
	case theme.ColorNameInputBorder:
		return color.NRGBA{R: 53, G: 58, B: 66, A: 255}
	case theme.ColorNameMenuBackground:
		return color.NRGBA{R: 23, G: 26, B: 31, A: 255}
	case theme.ColorNameOverlayBackground:
		return color.NRGBA{R: 14, G: 16, B: 20, A: 238}
	case theme.ColorNamePlaceHolder:
		return color.NRGBA{R: 122, G: 128, B: 138, A: 255}
	case theme.ColorNamePressed:
		return color.NRGBA{R: 48, G: 55, B: 64, A: 255}
	case theme.ColorNameScrollBar:
		return color.NRGBA{R: 66, G: 72, B: 81, A: 255}
	case theme.ColorNameSeparator:
		return color.NRGBA{R: 55, G: 59, B: 67, A: 255}
	case theme.ColorNameShadow:
		return color.NRGBA{R: 0, G: 0, B: 0, A: 160}
	case theme.ColorNameSuccess:
		return color.NRGBA{R: 96, G: 140, B: 110, A: 255}
	case theme.ColorNameWarning:
		return color.NRGBA{R: 140, G: 122, B: 82, A: 255}
	case theme.ColorNamePrimary, theme.ColorNameHyperlink:
		return color.NRGBA{R: 58, G: 79, B: 103, A: 255}
	case theme.ColorNameForegroundOnPrimary:
		return color.NRGBA{R: 242, G: 244, B: 246, A: 255}
	case theme.ColorNameFocus:
		return color.NRGBA{R: 72, G: 91, B: 112, A: 255}
	case theme.ColorNameSelection:
		return color.NRGBA{R: 56, G: 72, B: 91, A: 255}
	}
	if t.base != nil {
		return t.base.Color(name, variant)
	}
	return theme.DarkTheme().Color(name, variant)
}

func (t *fyneComicTheme) Font(style fyne.TextStyle) fyne.Resource {
	if res := comicUIFontResource(style.Bold); res != nil {
		return res
	}
	if t.base != nil {
		return t.base.Font(style)
	}
	return theme.DarkTheme().Font(style)
}

func (t *fyneComicTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	if t.base != nil {
		return t.base.Icon(name)
	}
	return theme.DarkTheme().Icon(name)
}

func (t *fyneComicTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 13
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNameSubHeadingText:
		return 16
	case theme.SizeNameHeadingText:
		return 20
	case theme.SizeNameInnerPadding:
		return 6
	case theme.SizeNamePadding:
		return 3
	case theme.SizeNameLineSpacing:
		return 2
	}
	if t.base != nil {
		return t.base.Size(name)
	}
	return theme.DarkTheme().Size(name)
}

func comicUIFontResource(bold bool) fyne.Resource {
	comicUIFontOnce.Do(func() {
		if len(embeddedFyneFontRegular) > 0 {
			comicUIFontRegular = fyne.NewStaticResource("simhei.ttf", embeddedFyneFontRegular)
		}
		if comicUIFontRegular != nil {
			comicUIFontBold = comicUIFontRegular
		}
		if comicUIFontRegular == nil {
			comicUIFontRegular = comicLoadFontResource([]string{
				resourcePath("assets", "fonts", "simhei.ttf"),
				resourcePath("assets", "fonts", "NotoSansSC-VF.ttf"),
				`C:\Windows\Fonts\simhei.ttf`,
				`C:\Windows\Fonts\NotoSansSC-VF.ttf`,
				`C:\Windows\Fonts\segoeui.ttf`,
			})
		}
		if comicUIFontBold == nil {
			comicUIFontBold = comicLoadFontResource([]string{
				resourcePath("assets", "fonts", "simhei.ttf"),
				resourcePath("assets", "fonts", "NotoSansSC-VF.ttf"),
				`C:\Windows\Fonts\simhei.ttf`,
				`C:\Windows\Fonts\NotoSansSC-VF.ttf`,
				`C:\Windows\Fonts\segoeuib.ttf`,
			})
		}
	})
	if bold && comicUIFontBold != nil {
		return comicUIFontBold
	}
	return comicUIFontRegular
}

func comicLoadFontResource(candidates []string) fyne.Resource {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		data, err := os.ReadFile(candidate)
		if err != nil || len(data) == 0 {
			continue
		}
		bootstrapTrace("comic font selected: %s", candidate)
		name := filepath.Base(candidate)
		return fyne.NewStaticResource(name, data)
	}
	return nil
}

type fyneUI struct {
	manager *Manager
	app     fyne.App
	window  fyne.Window

	root       *fyne.Container
	pageHolder *fyne.Container
	pages      map[string]fyne.CanvasObject

	statusLabel       *widget.Label
	urlEntry          *widget.Entry
	downloadRootDraft string
	concurrencyEntry  *widget.Entry
	taskList          *widget.List

	queuedValue  *widget.Label
	runningValue *widget.Label
	doneValue    *widget.Label
	errorValue   *widget.Label

	opsValue1 *widget.Label
	opsValue2 *widget.Label

	adSource  *widget.Label
	adURL     *widget.Label
	adDate    *widget.Label
	adPattern *widget.Label
	adReady   *widget.Label

	themeValue1 *widget.Label
	themeValue2 *widget.Label

	addBtn            *widget.Button
	clearBtn          *widget.Button
	selectAllBtn      *widget.Button
	clearSelectionBtn *widget.Button
	pauseBtn          *widget.Button
	resumeBtn         *widget.Button
	retryBtn          *widget.Button
	cancelBtn         *widget.Button
	deleteBtn         *widget.Button

	activeTab string

	snapshot AppState

	rowTaskIDs          []int
	selectedTaskIDs     map[int]bool
	autoRetryTaskIDs    map[int]bool
	chromiumPath        string
	chromiumSetupActive atomic.Bool
	taskRangeDragActive bool
	taskRangeDragAnchor int
	taskRangeDragAppend bool
	taskRangeDragLast   int
	taskRangeDragSelect bool
	lastAdblockSig      string
	lastStatusSig       string

	refreshPending  atomic.Bool
	refreshMu       sync.Mutex
	persistPending  atomic.Bool
	persistMu       sync.Mutex
	persistTimer    *time.Timer
	autoPersistMu   sync.Mutex
	autoPersistStop chan struct{}
	closing         atomic.Bool
	shutdownActive  atomic.Bool
	windowReady     atomic.Bool

	persistedState *persistedAppState
}

func (ui *fyneUI) showDuplicateTaskPrompt(url, downloadRoot string, existing *Task) {
	if ui == nil || ui.window == nil {
		return
	}
	var message strings.Builder
	message.WriteString("\u68c0\u6d4b\u5230\u76f8\u540c\u7f51\u5740\u7684\u4efb\u52a1\u5df2\u7ecf\u5b58\u5728\u3002\n\n")
	message.WriteString("\u7f51\u5740\uff1a")
	message.WriteString(url)
	if existing != nil {
		message.WriteString("\n\u5df2\u5b58\u5728\u4efb\u52a1\uff1a#")
		message.WriteString(fmt.Sprintf("%d", existing.ID))
		message.WriteString(" ")
		message.WriteString(displayTaskTitle(existing))
	}
	message.WriteString("\n\n\u662f\u5426\u4ecd\u7136\u521b\u5efa\u91cd\u590d\u4e0b\u8f7d\u4efb\u52a1\uff1f")
	dialog.ShowConfirm("\u91cd\u590d\u4efb\u52a1", message.String(), func(ok bool) {
		if !ok {
			ui.setStatus("\u5df2\u53d6\u6d88\u91cd\u590d\u4efb\u52a1\u521b\u5efa")
			return
		}
		ui.addTaskWithValuesForced(url, downloadRoot)
	}, ui.window)
}
func runNativeUI(manager *Manager, persisted *persistedAppState) error {
	runtime.LockOSThread()

	ui := newFyneUI(manager, persisted)
	defer ui.stopAutoPersistLoop()
	setAppRefreshHook(ui.requestRefresh)
	ui.window.Show()
	ui.windowReady.Store(true)
	ui.startAutoPersistLoop()
	ui.requestRefresh()
	ui.app.Run()
	return nil
}

func newFyneUI(manager *Manager, persisted *persistedAppState) *fyneUI {
	a := app.NewWithID("comic.downloader")
	a.Settings().SetTheme(&fyneComicTheme{base: theme.DarkTheme()})
	icon := fyne.NewStaticResource("app.ico", embeddedAppIcon)
	a.SetIcon(icon)

	ui := &fyneUI{
		manager:          manager,
		app:              a,
		activeTab:        fyneTabBase,
		pages:            make(map[string]fyne.CanvasObject),
		selectedTaskIDs:  make(map[int]bool),
		autoRetryTaskIDs: make(map[int]bool),
		chromiumPath:     currentChromiumPath(),
		persistedState:   persisted,
	}

	ui.window = a.NewWindow(fyneWindowTitle)
	ui.window.SetIcon(icon)
	ui.window.SetTitle(fyneWindowTitle)
	ui.window.Resize(fyne.NewSize(1380, 920))
	ui.window.SetFixedSize(false)

	ui.buildUI()
	ui.window.SetContent(ui.root)
	ui.applyPersistedUIState()
	// Fyne can size and center the native window before its first frame. Moving
	// it with Win32 only after Show causes a visible default-position flash.
	ui.window.CenterOnScreen()
	ui.window.SetCloseIntercept(func() {
		log.Printf("ui closing")
		ui.closing.Store(true)
		if !ui.shutdownActive.CompareAndSwap(false, true) {
			return
		}
		ui.persistMu.Lock()
		if ui.persistTimer != nil {
			ui.persistTimer.Stop()
		}
		ui.persistMu.Unlock()
		windowPlacement := ui.captureWindowPlacement()
		ui.setExitProgress("Exiting, cleaning up resources.", "Preparing to save task state")
		go func() {
			ui.stopAutoPersistLoop()
			ui.runOnMain(func() {
				ui.setExitProgress("Exiting, cleaning up resources.", "Saving task state")
			})
			ui.savePersistedStateWithWindow(windowPlacement)
			ui.runOnMain(func() {
				ui.setExitProgress("Exiting, cleaning up resources.", "Closing active workers and resources")
			})
			ui.manager.CloseActiveWorkers()
			ui.runOnMain(func() {
				ui.setExitProgress("Exiting, cleaning up resources.", "Stopping scheduler")
			})
			ui.manager.Quit()
			ui.runOnMain(func() {
				ui.setExitProgress("Cleanup complete", "All resources closed, exiting in 0.5s")
			})
			time.Sleep(500 * time.Millisecond)
			ui.runOnMain(func() {
				ui.app.Quit()
			})
		}()
	})
	return ui
}

func (ui *fyneUI) startAutoPersistLoop() {
	if ui == nil || !ui.windowReady.Load() || ui.closing.Load() || ui.shutdownActive.Load() {
		return
	}
	ui.autoPersistMu.Lock()
	defer ui.autoPersistMu.Unlock()
	if ui.autoPersistStop != nil {
		return
	}
	stop := make(chan struct{})
	ui.autoPersistStop = stop
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if ui == nil || ui.closing.Load() || ui.shutdownActive.Load() || !ui.windowReady.Load() {
					continue
				}
				ui.runOnMain(func() {
					ui.savePersistedState()
				})
			case <-stop:
				return
			}
		}
	}()
}

func (ui *fyneUI) stopAutoPersistLoop() {
	if ui == nil {
		return
	}
	ui.autoPersistMu.Lock()
	stop := ui.autoPersistStop
	ui.autoPersistStop = nil
	ui.autoPersistMu.Unlock()
	if stop == nil {
		return
	}
	select {
	case <-stop:
	default:
		close(stop)
	}
}

func (ui *fyneUI) buildUI() {
	ui.opsValue1 = widget.NewLabel("\u961f\u5217 0 | \u8fd0\u884c 0 | \u5b8c\u6210 0 | \u5931\u8d25 0")
	ui.opsValue2 = widget.NewLabel("\u65e5\u5fd7 0 | \u6392\u961f 0")
	ui.adSource = widget.NewLabel("-")
	ui.adURL = widget.NewLabel("-")
	ui.adDate = widget.NewLabel("-")
	ui.adPattern = widget.NewLabel("-")
	ui.adReady = widget.NewLabel("\u672a\u52a0\u8f7d")
	ui.themeValue1 = widget.NewLabel("\u6df1\u8272\u684c\u9762\u4e3b\u9898")
	ui.themeValue2 = widget.NewLabel("Go + Fyne \u7a97\u53e3")
	ui.statusLabel = widget.NewLabel("\u5c31\u7eea")
	ui.statusLabel.Wrapping = fyne.TextWrapWord
	ui.opsValue1.TextStyle = fyne.TextStyle{Bold: true}
	ui.opsValue2.TextStyle = fyne.TextStyle{Bold: true}
	ui.adSource.TextStyle = fyne.TextStyle{Bold: true}
	ui.adURL.TextStyle = fyne.TextStyle{Bold: true}
	ui.adDate.TextStyle = fyne.TextStyle{Bold: true}
	ui.adPattern.TextStyle = fyne.TextStyle{Bold: true}
	ui.adReady.TextStyle = fyne.TextStyle{Bold: true}
	ui.themeValue1.TextStyle = fyne.TextStyle{Bold: true}
	ui.themeValue2.TextStyle = fyne.TextStyle{Bold: true}
	ui.statusLabel.TextStyle = fyne.TextStyle{Bold: true}

	ui.urlEntry = widget.NewEntry()
	ui.urlEntry.SetPlaceHolder("\u8bf7\u8f93\u5165\u6f2b\u753b URL")
	ui.urlEntry.OnSubmitted = func(_ string) {
		ui.addTaskFromInput()
	}
	ui.urlEntry.OnChanged = func(string) {
		ui.requestPersist()
	}

	ui.concurrencyEntry = widget.NewEntry()
	ui.concurrencyEntry.SetPlaceHolder("\u5e76\u53d1\u6570 1-16")
	ui.concurrencyEntry.OnSubmitted = func(_ string) {
		ui.applyConcurrencyFromInput()
	}
	ui.concurrencyEntry.OnChanged = func(string) {
		ui.requestPersist()
	}
	ui.addBtn = widget.NewButton("\u6dfb\u52a0\u4efb\u52a1", func() {
		ui.addTaskFromInput()
	})
	ui.addBtn.Importance = widget.MediumImportance
	ui.selectAllBtn = widget.NewButton("\u5168\u9009", func() {
		ui.selectAllTasks()
	})
	ui.clearSelectionBtn = widget.NewButton("\u6e05\u9664\u9009\u62e9", func() {
		ui.clearTaskSelection()
	})
	ui.clearBtn = widget.NewButton("\u6e05\u7406\u5df2\u5b8c\u6210", func() {
		ui.setStatus("\u6b63\u5728\u6e05\u7406\u5df2\u5b8c\u6210\u4efb\u52a1")
		go func() {
			ui.manager.ClearCompleted()
			ui.runOnMain(func() {
				ui.setStatus("\u5df2\u6e05\u7406\u5df2\u5b8c\u6210\u4efb\u52a1")
				ui.requestRefresh()
			})
		}()
	})
	ui.clearBtn.Importance = widget.MediumImportance

	ui.pauseBtn = widget.NewButton("\u6682\u505c", func() {
		ui.runSelectedActionMulti("pause")
	})
	ui.resumeBtn = widget.NewButton("\u7ee7\u7eed", func() {
		ui.runSelectedActionMulti("resume")
	})
	ui.retryBtn = widget.NewButton("\u91cd\u8bd5", func() {
		ui.runSelectedActionMulti("retry")
	})
	ui.cancelBtn = widget.NewButton("\u53d6\u6d88", func() {
		ui.runSelectedActionMulti("cancel")
	})
	ui.deleteBtn = widget.NewButton("\u5220\u9664", func() {
		ui.runSelectedActionMulti("delete")
	})
	ui.taskList = widget.NewList(
		func() int {
			if ui == nil {
				return 0
			}
			return len(ui.rowTaskIDs)
		},
		func() fyne.CanvasObject {
			return newTaskCardItem(ui)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			card, ok := obj.(*taskCardItem)
			if !ok || card == nil {
				return
			}
			card.setRow(int(id))
		},
	)
	ui.taskList.HideSeparators = true

	ui.pages[fyneTabBase] = ui.makeBasePage()

	ui.pageHolder = container.NewMax(ui.pages[fyneTabBase])

	topBar := ui.makeTopBar()
	statusBar := ui.makeStatusBar()
	ui.root = container.NewBorder(topBar, statusBar, nil, nil, container.NewPadded(ui.pageHolder))
}

func (ui *fyneUI) makeTopBar() fyne.CanvasObject {
	buttons := []fyne.CanvasObject{
		widget.NewButton("\u4e0b\u8f7d\u76ee\u5f55", func() { ui.showDownloadRootDialog() }),
		widget.NewButton("\u5e76\u53d1\u6570", func() { ui.showConcurrencyDialog() }),
		widget.NewButton("\u66f4\u65b0\u5e7f\u544a\u62e6\u622a\u89c4\u5219", func() { ui.updateAdblockRulesFromMenu() }),
		widget.NewButton("\u5bfc\u5165\u5386\u53f2\u8bb0\u5f55", func() { ui.showImportHistoryDialog() }),
	}
	tabBar := container.NewHBox(buttons...)
	return container.NewPadded(tabBar)
}
func (ui *fyneUI) makeStatusBar() fyne.CanvasObject {
	ui.statusLabel.Wrapping = fyne.TextWrapOff
	ui.statusLabel.Alignment = fyne.TextAlignLeading
	versionLabel := widget.NewLabel(appVersionLabel())
	versionLabel.Alignment = fyne.TextAlignTrailing
	versionLabel.TextStyle = fyne.TextStyle{Monospace: true}
	return container.NewPadded(container.NewBorder(nil, nil, nil, versionLabel, ui.statusLabel))
}

func (ui *fyneUI) makeBasePage() fyne.CanvasObject {
	listCard := widget.NewCard("", "", ui.taskList)
	actionRow := container.NewHBox(ui.selectAllBtn, ui.clearSelectionBtn, ui.pauseBtn, ui.resumeBtn, ui.retryBtn, ui.cancelBtn, ui.deleteBtn)
	actionCard := widget.NewCard("", "", actionRow)

	topControls := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabelWithStyle("\u7f51\u5740", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), container.NewHBox(ui.addBtn, ui.clearBtn), ui.urlEntry),
	)
	upper := container.NewBorder(topControls, actionCard, nil, nil, listCard)
	upperWrap := container.NewPadded(upper)
	return upperWrap
}

func (ui *fyneUI) keyValueRow(key string, value *widget.Label) fyne.CanvasObject {
	keyLabel := widget.NewLabel(key)
	keyLabel.TextStyle = fyne.TextStyle{Bold: true}
	return container.NewBorder(nil, nil, keyLabel, nil, value)
}

func displayTaskTitle(task *Task) string {
	title := cleanVisibleText(task.Title)
	if title == "" || title == "pending" {
		return fmt.Sprintf("\u4efb\u52a1 #%d", task.ID)
	}
	return title
}
func displayTaskURL(url string) string {
	text := cleanVisibleText(url)
	if text == "" {
		return ""
	}
	return truncateText(text, 64)
}

func displayTaskWorker(task *Task) string {
	if task == nil {
		return "\u81ea\u52a8"
	}
	worker := strings.TrimSpace(task.Worker)
	if worker == "" {
		return "\u81ea\u52a8"
	}
	route := strings.TrimSpace(task.WorkerSource)
	if route != "" && route != "task.worker" {
		worker = worker + " | " + route
	}
	return truncateText(worker, 18)
}
func taskStateLabel(state TaskState) string {
	switch state {
	case TaskQueued:
		return "\u6392\u961f"
	case TaskRunning:
		return "\u8fd0\u884c"
	case TaskWaitingVerification:
		return "\u9a8c\u8bc1"
	case TaskPaused:
		return "\u6682\u505c"
	case TaskDone:
		return "\u5b8c\u6210"
	case TaskError:
		return "\u5931\u8d25"
	default:
		return string(state)
	}
}
func taskCardMetaText(task *Task) string {
	if task == nil {
		return ""
	}
	parts := []string{taskStateLabel(task.State), formatTaskPercent(task.Percent)}
	if speed := strings.TrimSpace(task.SpeedText); speed != "" {
		parts = append(parts, speed)
	}
	if detail := compactTaskDetail(task.Detail); detail != "" {
		parts = append(parts, detail)
	}
	return strings.Join(parts, " | ")
}

func compactTaskDetail(detail string) string {
	detail = cleanVisibleText(detail)
	if detail == "" {
		return ""
	}
	replacements := []struct {
		old string
		new string
	}{
		{"starting myreadingmanga worker", "\u542f\u52a8 worker"},
		{"resolving image URLs", "\u89e3\u6790\u56fe\u7247 URL"},
		{"preparing downloads", "\u51c6\u5907\u4e0b\u8f7d"},
		{"queuing downloads", "\u6392\u961f\u4e0b\u8f7d"},
		{"downloading images", "\u4e0b\u8f7d\u56fe\u7247"},
		{"download complete", "\u4e0b\u8f7d\u5b8c\u6210"},
		{"download finished with errors", "\u4e0b\u8f7d\u6709\u9519\u8bef"},
		{"waiting for verification", "\u7b49\u5f85\u9a8c\u8bc1"},
		{"verification completed; resumed", "\u9a8c\u8bc1\u5b8c\u6210"},
	}
	lower := strings.ToLower(detail)
	for _, item := range replacements {
		if strings.EqualFold(detail, item.old) || strings.Contains(lower, strings.ToLower(item.old)) {
			return item.new
		}
	}
	if strings.HasPrefix(lower, "queued ") {
		return strings.Replace(detail, "queued", "\u6392\u961f", 1)
	}
	if strings.HasPrefix(lower, "download failed") {
		return strings.Replace(detail, "download failed", "\u5931\u8d25", 1)
	}
	return truncateText(detail, 32)
}

type taskCardItem struct {
	widget.BaseWidget
	ui  *fyneUI
	row int

	bg              *canvas.Rectangle
	selectedOverlay *canvas.Rectangle
	accent          *canvas.Rectangle
	thumbBg         *canvas.Rectangle
	thumbImage      *canvas.Image
	thumbText       *canvas.Text
	selectedFrame   *canvas.Rectangle
	title           *widget.Label
	meta            *widget.Label
	progress        *widget.ProgressBar
	root            fyne.CanvasObject

	lastTaskID         int
	lastThumbnailPath  string
	lastThumbnailShown bool
}

type taskCardBodyLayout struct{}

func (taskCardBodyLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) == 0 {
		return
	}
	visible := make([]fyne.CanvasObject, 0, len(objects))
	totalHeight := float32(0)
	maxWidth := size.Width
	for _, obj := range objects {
		if obj == nil || !obj.Visible() {
			continue
		}
		min := obj.MinSize()
		totalHeight += min.Height
		visible = append(visible, obj)
	}
	if len(visible) == 0 {
		return
	}
	gap := float32(0)
	if len(visible) > 1 {
		gap = ((size.Height - totalHeight) / float32(len(visible)-1)) * 0.8
		if gap < 2 {
			gap = 2
		}
	}
	groupHeight := totalHeight + gap*float32(len(visible)-1)
	startY := (size.Height-groupHeight)/2 - 6
	if startY < 0 {
		startY = 0
	}
	y := startY
	for _, obj := range visible {
		min := obj.MinSize()
		obj.Move(fyne.NewPos(0, y))
		obj.Resize(fyne.NewSize(maxWidth, min.Height))
		y += min.Height + gap
	}
}

func (taskCardBodyLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var width float32
	var height float32
	for _, obj := range objects {
		if obj == nil {
			continue
		}
		min := obj.MinSize()
		if min.Width > width {
			width = min.Width
		}
		if min.Height > height {
			height = min.Height
		}
	}
	return fyne.NewSize(width, height*3)
}

type taskCardRenderer struct {
	item *taskCardItem
	root fyne.CanvasObject
}

func newTaskCardItem(ui *fyneUI) *taskCardItem {
	item := &taskCardItem{ui: ui, row: -1}
	item.ExtendBaseWidget(item)
	return item
}

func (c *taskCardItem) setRow(row int) {
	c.row = row
	c.Refresh()
}

func (c *taskCardItem) task() *Task {
	if c == nil || c.ui == nil {
		return nil
	}
	return c.ui.taskByRow(c.row)
}

func (c *taskCardItem) CreateRenderer() fyne.WidgetRenderer {
	c.bg = canvas.NewRectangle(color.NRGBA{R: 27, G: 30, B: 35, A: 255})
	c.bg.StrokeWidth = 1
	c.bg.StrokeColor = color.NRGBA{R: 40, G: 45, B: 53, A: 255}
	c.selectedOverlay = canvas.NewRectangle(color.NRGBA{A: 0})
	c.accent = canvas.NewRectangle(color.NRGBA{R: 66, G: 95, B: 125, A: 255})
	c.accent.SetMinSize(fyne.NewSize(0, 5))
	c.selectedFrame = canvas.NewRectangle(color.NRGBA{A: 0})
	c.selectedFrame.StrokeWidth = 0
	c.selectedFrame.StrokeColor = color.NRGBA{R: 111, G: 174, B: 255, A: 255}
	c.thumbBg = canvas.NewRectangle(color.NRGBA{R: 34, G: 38, B: 45, A: 255})
	c.thumbImage = canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	c.thumbImage.FillMode = canvas.ImageFillContain
	c.thumbImage.SetMinSize(fyne.NewSize(84, 84))
	c.thumbText = canvas.NewText("THUMB", color.NRGBA{R: 190, G: 197, B: 206, A: 255})
	c.thumbText.TextStyle = fyne.TextStyle{Bold: true}
	c.thumbText.Alignment = fyne.TextAlignCenter
	c.title = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.title.Wrapping = fyne.TextWrapOff
	c.title.TextStyle = fyne.TextStyle{Bold: true}
	c.title.Truncation = fyne.TextTruncateEllipsis
	c.title.Importance = widget.MediumImportance
	c.meta = widget.NewLabel("")
	c.meta.Wrapping = fyne.TextWrapOff
	c.meta.Truncation = fyne.TextTruncateEllipsis
	c.meta.Importance = widget.MediumImportance
	c.progress = widget.NewProgressBar()

	thumb := container.NewStack(c.thumbBg, c.thumbImage, container.NewCenter(c.thumbText))
	body := container.New(
		taskCardBodyLayout{},
		c.title,
		c.meta,
		c.progress,
	)
	content := container.NewBorder(nil, nil, thumb, nil, body)
	c.root = container.NewPadded(content)
	statusWrap := container.NewBorder(c.accent, nil, nil, nil, c.root)
	stack := container.NewStack(c.bg, statusWrap, c.selectedFrame)
	c.refreshContent()
	return &taskCardRenderer{item: c, root: stack}
}

func (c *taskCardItem) Tapped(_ *fyne.PointEvent) {
	task := c.task()
	if task == nil || task.ID <= 0 || c.ui == nil {
		return
	}
	if c.ui.ctrlPressed() {
		c.ui.toggleTaskSelection(task.ID)
		return
	}
	c.ui.selectSingleTask(task.ID)
}

func (c *taskCardItem) TappedSecondary(ev *fyne.PointEvent) {
	task := c.task()
	if task == nil || task.ID <= 0 || c.ui == nil {
		return
	}
	c.ui.showTaskCardMenu(task, ev.AbsolutePosition)
}

func (c *taskCardItem) Dragged(ev *fyne.DragEvent) {
	// Keep drag gestures from turning a simple click into range-selection noise.
	// Range selection is intentionally disabled for now to keep single-click selection stable.
	_ = ev
}

func (c *taskCardItem) DragEnd() {
}

func (c *taskCardItem) refreshContent() {
	if c == nil || c.ui == nil {
		return
	}
	task := c.task()
	if task == nil || task.ID <= 0 {
		c.lastTaskID = 0
		c.lastThumbnailPath = ""
		c.lastThumbnailShown = false
		if c.bg != nil {
			c.bg.FillColor = color.NRGBA{R: 27, G: 30, B: 35, A: 255}
			c.bg.Refresh()
		}
		if c.accent != nil {
			c.accent.FillColor = color.NRGBA{R: 126, G: 147, B: 172, A: 255}
			c.accent.Refresh()
		}
		return
	}
	if c.lastTaskID != task.ID {
		c.lastTaskID = task.ID
		c.lastThumbnailPath = ""
		c.lastThumbnailShown = false
	}
	selected := c.ui.taskSelected(task.ID)
	statusAccent := taskStatusAccentColor(task)
	if c.bg != nil {
		if selected {
			c.bg.FillColor = color.NRGBA{R: 39, G: 55, B: 80, A: 255}
			c.bg.StrokeWidth = 2
			c.bg.StrokeColor = color.NRGBA{R: 111, G: 174, B: 255, A: 255}
		} else if c.row%2 == 0 {
			c.bg.FillColor = color.NRGBA{R: 27, G: 30, B: 35, A: 255}
			c.bg.StrokeWidth = 1
			c.bg.StrokeColor = color.NRGBA{R: 40, G: 45, B: 53, A: 255}
		} else {
			c.bg.FillColor = color.NRGBA{R: 31, G: 34, B: 40, A: 255}
			c.bg.StrokeWidth = 1
			c.bg.StrokeColor = color.NRGBA{R: 43, G: 48, B: 56, A: 255}
		}
		c.bg.Refresh()
	}
	if c.accent != nil {
		c.accent.FillColor = statusAccent
		c.accent.Refresh()
	}
	if c.selectedFrame != nil {
		if selected {
			c.selectedFrame.FillColor = color.NRGBA{A: 0}
			c.selectedFrame.StrokeWidth = 2
			c.selectedFrame.StrokeColor = color.NRGBA{R: 111, G: 174, B: 255, A: 255}
		} else {
			c.selectedFrame.FillColor = color.NRGBA{A: 0}
			c.selectedFrame.StrokeWidth = 0
			c.selectedFrame.StrokeColor = color.NRGBA{A: 0}
		}
		c.selectedFrame.Refresh()
	}
	if c.thumbBg != nil {
		if selected {
			c.thumbBg.FillColor = color.NRGBA{R: 44, G: 52, B: 67, A: 255}
		} else {
			c.thumbBg.FillColor = color.NRGBA{R: 34, G: 38, B: 45, A: 255}
		}
		c.thumbBg.Refresh()
	}
	if c.thumbText != nil {
		if selected {
			c.thumbText.Color = color.NRGBA{R: 230, G: 236, B: 246, A: 255}
		} else {
			c.thumbText.Color = color.NRGBA{R: 190, G: 197, B: 206, A: 255}
		}
		c.thumbText.Text = "THUMB"
		if c.thumbImage != nil {
			thumbPath := resolveTaskAssetPath(task.ThumbnailPath)
			if thumbPath != c.lastThumbnailPath {
				c.lastThumbnailPath = thumbPath
				c.lastThumbnailShown = false
			}
			if thumbPath != "" {
				if !c.lastThumbnailShown {
					if info, err := os.Stat(thumbPath); err == nil && !info.IsDir() {
						c.lastThumbnailShown = true
						c.thumbImage.File = thumbPath
						c.thumbImage.Image = nil
						c.thumbImage.Resource = nil
					}
				}
				if c.lastThumbnailShown {
					c.thumbImage.Show()
					c.thumbImage.Refresh()
					c.thumbText.Hide()
				} else {
					c.thumbImage.Hide()
					c.thumbText.Show()
				}
			} else {
				c.thumbImage.Hide()
				c.thumbText.Show()
			}
		}
		c.thumbText.Refresh()
	}
	if c.title != nil {
		c.title.SetText(displayTaskTitle(task))
		c.title.Refresh()
	}
	if c.meta != nil {
		c.meta.SetText(taskCardMetaText(task))
		c.meta.Refresh()
	}
	if c.progress != nil {
		c.progress.SetValue(normalizedTaskPercent(task.Percent))
		c.progress.Refresh()
	}
}

func (r *taskCardRenderer) Layout(size fyne.Size) {
	if r == nil || r.root == nil {
		return
	}
	r.root.Resize(size)
}

func (r *taskCardRenderer) MinSize() fyne.Size {
	if r == nil || r.root == nil {
		return fyne.NewSize(0, taskCardItemHeight)
	}
	min := r.root.MinSize()
	if min.Height < taskCardItemHeight {
		min.Height = taskCardItemHeight
	}
	return min
}

func (r *taskCardRenderer) Refresh() {
	if r == nil {
		return
	}
	if r.item != nil {
		r.item.refreshContent()
	}
	if r.root != nil {
		r.root.Refresh()
	}
}

func (r *taskCardRenderer) Objects() []fyne.CanvasObject {
	if r == nil || r.root == nil {
		return nil
	}
	return []fyne.CanvasObject{r.root}
}

func (r *taskCardRenderer) Destroy() {}

func (ui *fyneUI) showTaskCardMenu(task *Task, pos fyne.Position) {
	if ui == nil || ui.window == nil || task == nil {
		return
	}
	openDir := fyne.NewMenuItem("\u6253\u5f00\u76ee\u5f55", func() { ui.openTaskDirectory(task) })
	openDir.Disabled = task.State != TaskDone || strings.TrimSpace(task.OutputDir) == ""
	details := fyne.NewMenuItem("\u8be6\u60c5", func() { ui.showTaskDetails(task) })
	menu := fyne.NewMenu(fmt.Sprintf("\u4efb\u52a1 #%d", task.ID), fyne.NewMenuItem("\u6682\u505c", func() { go ui.runTaskAction(task.ID, "pause") }), fyne.NewMenuItem("\u7ee7\u7eed", func() { go ui.runTaskAction(task.ID, "resume") }), fyne.NewMenuItem("\u91cd\u8bd5", func() { go ui.runTaskAction(task.ID, "retry") }), fyne.NewMenuItem("\u53d6\u6d88", func() { go ui.runTaskAction(task.ID, "cancel") }), fyne.NewMenuItemSeparator(), openDir, details, fyne.NewMenuItemSeparator(), fyne.NewMenuItem("\u5220\u9664", func() { go ui.runTaskAction(task.ID, "delete") }))
	if !canTaskAction(task, "pause") {
		menu.Items[0].Disabled = true
	}
	if !canTaskAction(task, "resume") {
		menu.Items[1].Disabled = true
	}
	if len(menu.Items) > 2 {
		menu.Items[2].Disabled = !canTaskAction(task, "retry")
	}
	if len(menu.Items) > 3 {
		menu.Items[3].Disabled = !canTaskAction(task, "cancel")
	}
	if len(menu.Items) > 0 {
		menu.Items[len(menu.Items)-1].Disabled = !canTaskAction(task, "delete")
	}
	popup := widget.NewPopUpMenu(menu, ui.window.Canvas())
	popup.ShowAtPosition(pos)
}
func (ui *fyneUI) runTaskAction(id int, action string) {
	if ui == nil || ui.manager == nil {
		return
	}
	ok := ui.manager.applyTaskAction(id, action)
	if ok && action == "retry" {
		if task := ui.taskByID(id); task != nil && (task.Worker == "myreadingmanga" || task.WorkerSource == "site:myreadingmanga") {
			delete(ui.autoRetryTaskIDs, id)
			log.Printf("manual retry reset auto-retry state id=%d worker=%s source=%s", id, task.Worker, task.WorkerSource)
		}
	}
	ui.runOnMain(func() {
		if ok {
			ui.setStatus(fmt.Sprintf("%s #%d", actionName(action), id))
		} else {
			ui.setStatus(fmt.Sprintf("%s #%d \u5931\u8d25", actionName(action), id))
		}
		ui.requestRefresh()
	})
}

func (ui *fyneUI) openTaskDirectory(task *Task) {
	if ui == nil || task == nil {
		return
	}
	dir := strings.TrimSpace(task.OutputDir)
	if dir == "" {
		ui.setStatus("\u4efb\u52a1\u8f93\u51fa\u76ee\u5f55\u4e0d\u53ef\u7528")
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		ui.setStatus("\u4efb\u52a1\u8f93\u51fa\u76ee\u5f55\u4e0d\u5b58\u5728")
		return
	}
	cmd := exec.Command("explorer", dir)
	if err := cmd.Start(); err != nil {
		ui.setStatus("\u6253\u5f00\u4efb\u52a1\u76ee\u5f55\u5931\u8d25")
		log.Printf("open directory failed id=%d dir=%s err=%v", task.ID, dir, err)
		return
	}
	ui.setStatus("\u4efb\u52a1\u76ee\u5f55\u5df2\u6253\u5f00")
}
func (ui *fyneUI) showTaskDetails(task *Task) {
	if ui == nil || ui.window == nil || task == nil {
		return
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "\u7f16\u53f7\uff1a#%d\n", task.ID)
	fmt.Fprintf(&summary, "\u6807\u9898\uff1a%s\n", displayTaskTitle(task))
	fmt.Fprintf(&summary, "\u72b6\u6001\uff1a%s\n", task.State)
	fmt.Fprintf(&summary, "\u5904\u7406\u5668\uff1a%s\n", displayTaskWorker(task))
	fmt.Fprintf(&summary, "\u7f51\u5740\uff1a%s\n", task.URL)
	fmt.Fprintf(&summary, "\u4e0b\u8f7d\u76ee\u5f55\uff1a%s\n", blankIfEmpty(task.DownloadRoot))
	fmt.Fprintf(&summary, "\u8f93\u51fa\u76ee\u5f55\uff1a%s\n", blankIfEmpty(task.OutputDir))
	fmt.Fprintf(&summary, "\u65e0\u5934\u6a21\u5f0f\uff1a%t\n", task.Headless)
	fmt.Fprintf(&summary, "HTTP-only: %t\n", task.HttpOnly)
	fmt.Fprintf(&summary, "\u8fdb\u5ea6\uff1a%s\n", formatTaskPercent(task.Percent))
	fmt.Fprintf(&summary, "\u8be6\u60c5\uff1a%s\n", blankIfEmpty(cleanVisibleText(task.Detail)))
	fmt.Fprintf(&summary, "\u521b\u5efa\u65f6\u95f4\uff1a%s\n", task.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&summary, "\u66f4\u65b0\u65f6\u95f4\uff1a%s\n", task.UpdatedAt.Format(time.RFC3339))
	entry := widget.NewMultiLineEntry()
	entry.SetText(summary.String())
	entry.Disable()
	entry.Wrapping = fyne.TextWrapWord
	scroll := container.NewScroll(entry)
	scroll.SetMinSize(fyne.NewSize(760, 540))
	dlg := dialog.NewCustom(fmt.Sprintf("\u4efb\u52a1 #%d \u8be6\u60c5", task.ID), "\u5173\u95ed", scroll, ui.window)
	dlg.Resize(fyne.NewSize(820, 620))
	dlg.Show()
	metaPath := filepath.Join(strings.TrimSpace(task.OutputDir), "meta.json")
	go func() {
		raw, err := os.ReadFile(metaPath)
		if err != nil || len(raw) == 0 {
			ui.runOnMain(func() { entry.SetText(summary.String() + "\nmeta.json\uff1a\u4e0d\u53ef\u7528\n") })
			return
		}
		data := append([]byte(nil), raw...)
		var pretty bytes.Buffer
		if json.Indent(&pretty, data, "", "  ") == nil {
			data = pretty.Bytes()
		}
		ui.runOnMain(func() { entry.SetText(summary.String() + "\nmeta.json:\n" + string(data)) })
	}()
}
func cleanVisibleText(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r == utf8.RuneError:
			continue
		case r == '\n' || r == '\r' || r == '	':
			b.WriteRune(' ')
		case !unicode.IsPrint(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	text := strings.Join(strings.Fields(b.String()), " ")
	if strings.EqualFold(strings.TrimSpace(text), "<nil>") {
		return ""
	}
	return text
}

func truncateText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	if limit == 1 {
		return string(runes[:1])
	}
	return string(runes[:limit-1]) + "..."
}

func newTableCell() fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 27, G: 30, B: 35, A: 255})
	progress := canvas.NewRectangle(color.NRGBA{R: 66, G: 95, B: 125, A: 255})
	progress.Hide()
	btn := widget.NewButton(" ", nil)
	btn.Importance = widget.LowImportance
	txt := canvas.NewText(" ", color.NRGBA{R: 227, G: 230, B: 235, A: 255})
	txt.TextSize = 14
	txt.Alignment = fyne.TextAlignLeading
	return container.NewStack(bg, progress, btn, txt)
}

func newHeaderCell() fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 23, G: 26, B: 31, A: 255})
	progress := canvas.NewRectangle(color.NRGBA{R: 23, G: 26, B: 31, A: 255})
	progress.Hide()
	txt := canvas.NewText(" ", color.NRGBA{R: 212, G: 216, B: 224, A: 255})
	txt.TextStyle = fyne.TextStyle{Bold: true}
	txt.TextSize = 13
	txt.Alignment = fyne.TextAlignLeading
	return container.NewStack(bg, progress, txt)
}

func (ui *fyneUI) updateHeaderCell(id widget.TableCellID, obj fyne.CanvasObject) {
	cell, ok := obj.(*fyne.Container)
	if !ok || len(cell.Objects) < 3 {
		return
	}
	bg, _ := cell.Objects[0].(*canvas.Rectangle)
	progress, _ := cell.Objects[1].(*canvas.Rectangle)
	txt, _ := cell.Objects[2].(*canvas.Text)
	if bg == nil || txt == nil {
		return
	}
	headers := []string{"\u9009\u62e9", "\u7f16\u53f7", "\u72b6\u6001", "\u5904\u7406\u5668", "\u8fdb\u5ea6", "\u6807\u9898", "\u7f51\u5740"}
	if id.Col < 0 || id.Col >= len(headers) {
		txt.Text = ""
	} else {
		txt.Text = headers[id.Col]
	}
	bg.FillColor = color.NRGBA{R: 23, G: 26, B: 31, A: 255}
	txt.Color = color.NRGBA{R: 222, G: 226, B: 232, A: 255}
	txt.TextStyle = fyne.TextStyle{Bold: true}
	bg.Refresh()
	if progress != nil {
		progress.Hide()
		progress.Refresh()
	}
	txt.Refresh()
}

func (ui *fyneUI) updateTaskCell(id widget.TableCellID, obj fyne.CanvasObject) {
	ui.updateTaskCellMulti(id, obj)
}

func (ui *fyneUI) taskByID(id int) *Task {
	if ui == nil {
		return nil
	}
	for _, task := range ui.snapshot.Tasks {
		if task != nil && task.ID == id {
			return task
		}
	}
	return nil
}

func (ui *fyneUI) taskByRow(row int) *Task {
	if row < 0 || row >= len(ui.rowTaskIDs) {
		return &Task{}
	}
	id := ui.rowTaskIDs[row]
	for _, task := range ui.snapshot.Tasks {
		if task.ID == id {
			return task
		}
	}
	return &Task{}
}

func taskStateColor(state TaskState) color.Color {
	switch state {
	case TaskQueued:
		return color.NRGBA{R: 126, G: 147, B: 172, A: 255}
	case TaskRunning:
		return color.NRGBA{R: 135, G: 172, B: 202, A: 255}
	case TaskWaitingVerification:
		return color.NRGBA{R: 167, G: 147, B: 202, A: 255}
	case TaskPaused:
		return color.NRGBA{R: 189, G: 160, B: 104, A: 255}
	case TaskDone:
		return color.NRGBA{R: 117, G: 160, B: 123, A: 255}
	case TaskError:
		return color.NRGBA{R: 181, G: 103, B: 103, A: 255}
	default:
		return color.NRGBA{R: 186, G: 192, B: 201, A: 255}
	}
}

func taskStatusAccentColor(task *Task) color.NRGBA {
	if task == nil {
		return color.NRGBA{R: 126, G: 147, B: 172, A: 255}
	}
	switch task.State {
	case TaskQueued:
		return color.NRGBA{R: 126, G: 147, B: 172, A: 255}
	case TaskRunning:
		return color.NRGBA{R: 104, G: 161, B: 214, A: 255}
	case TaskWaitingVerification:
		return color.NRGBA{R: 153, G: 125, B: 205, A: 255}
	case TaskPaused:
		return color.NRGBA{R: 194, G: 158, B: 86, A: 255}
	case TaskDone:
		return color.NRGBA{R: 103, G: 162, B: 111, A: 255}
	case TaskError:
		if strings.Contains(strings.ToLower(task.Detail), "cancel") {
			return color.NRGBA{R: 152, G: 128, B: 176, A: 255}
		}
		return color.NRGBA{R: 196, G: 101, B: 101, A: 255}
	default:
		return color.NRGBA{R: 126, G: 147, B: 172, A: 255}
	}
}

func (ui *fyneUI) taskSelected(id int) bool {
	if ui == nil || len(ui.selectedTaskIDs) == 0 {
		return false
	}
	return ui.selectedTaskIDs[id]
}

func (ui *fyneUI) syncSelectionWithSnapshot() {
	if ui == nil {
		return
	}
	live := make(map[int]struct{}, len(ui.snapshot.Tasks))
	for _, task := range ui.snapshot.Tasks {
		if task == nil {
			continue
		}
		live[task.ID] = struct{}{}
	}
	next := make(map[int]bool, len(ui.selectedTaskIDs))
	for id := range ui.selectedTaskIDs {
		if _, ok := live[id]; ok {
			next[id] = true
		}
	}
	ui.selectedTaskIDs = next
}

func (ui *fyneUI) selectedTaskIDsList() []int {
	if ui == nil || len(ui.selectedTaskIDs) == 0 {
		return nil
	}
	ids := make([]int, 0, len(ui.selectedTaskIDs))
	for _, id := range ui.rowTaskIDs {
		if ui.selectedTaskIDs[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

func (ui *fyneUI) autoRetryTaskIDsList() []int {
	if ui == nil || len(ui.autoRetryTaskIDs) == 0 {
		return nil
	}
	ids := make([]int, 0, len(ui.autoRetryTaskIDs))
	for _, id := range ui.rowTaskIDs {
		if ui.autoRetryTaskIDs[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

func (ui *fyneUI) rowTaskID(row int) int {
	if row < 0 || row >= len(ui.rowTaskIDs) {
		return 0
	}
	return ui.rowTaskIDs[row]
}

func (ui *fyneUI) ctrlPressed() bool {
	if fyne.CurrentApp() == nil || fyne.CurrentApp().Driver() == nil {
		return false
	}
	if dd, ok := fyne.CurrentApp().Driver().(desktop.Driver); ok {
		return dd.CurrentKeyModifiers()&fyne.KeyModifierControl != 0
	}
	return false
}

func (ui *fyneUI) selectSingleTask(id int) {
	if id <= 0 {
		return
	}
	if ui.selectedTaskIDs == nil {
		ui.selectedTaskIDs = make(map[int]bool)
	}
	ui.selectedTaskIDs = map[int]bool{id: true}
	ui.syncSelectionWithSnapshot()
	ui.refreshTaskButtonsMulti()
	ui.refreshSummaryMulti(ui.snapshot)
	ui.refreshTaskSelectionState()
	ui.requestPersist()
}

func (ui *fyneUI) setTaskSelection(id int, selected bool) {
	if ui.selectedTaskIDs == nil {
		ui.selectedTaskIDs = make(map[int]bool)
	}
	if selected {
		ui.selectedTaskIDs[id] = true
	} else {
		delete(ui.selectedTaskIDs, id)
	}
	ui.syncSelectionWithSnapshot()
	ui.refreshTaskButtonsMulti()
	ui.refreshSummaryMulti(ui.snapshot)
	ui.refreshTaskSelectionState()
	ui.requestPersist()
}

func (ui *fyneUI) toggleTaskSelection(id int) {
	ui.setTaskSelection(id, !ui.taskSelected(id))
}

func (ui *fyneUI) beginTaskRangeSelection(anchorRow int, append bool) {
	if anchorRow < 0 {
		return
	}
	ui.taskRangeDragActive = true
	ui.taskRangeDragAnchor = anchorRow
	ui.taskRangeDragLast = anchorRow
	ui.taskRangeDragAppend = append
	anchorID := ui.rowTaskID(anchorRow)
	ui.taskRangeDragSelect = !ui.taskSelected(anchorID)
	if !append && ui.taskRangeDragSelect {
		ui.selectedTaskIDs = make(map[int]bool)
	}
	ui.applyTaskRangeSelection(anchorRow)
}

func (ui *fyneUI) updateTaskRangeSelection(currentRow int) {
	if !ui.taskRangeDragActive || currentRow < 0 {
		return
	}
	if ui.taskRangeDragLast == currentRow {
		return
	}
	ui.taskRangeDragLast = currentRow
	ui.applyTaskRangeSelection(currentRow)
}

func (ui *fyneUI) endTaskRangeSelection() {
	if !ui.taskRangeDragActive {
		return
	}
	ui.taskRangeDragActive = false
	ui.taskRangeDragAnchor = -1
	ui.taskRangeDragLast = -1
	ui.taskRangeDragAppend = false
	ui.taskRangeDragSelect = false
	ui.requestPersist()
}

func (ui *fyneUI) applyTaskRangeSelection(currentRow int) {
	if ui == nil {
		return
	}
	startRow := ui.taskRangeDragAnchor
	endRow := currentRow
	if startRow > endRow {
		startRow, endRow = endRow, startRow
	}
	if !ui.taskRangeDragAppend && ui.taskRangeDragSelect {
		ui.selectedTaskIDs = make(map[int]bool)
	}
	for row := startRow; row <= endRow; row++ {
		id := ui.rowTaskID(row)
		if id > 0 {
			if ui.taskRangeDragSelect {
				ui.selectedTaskIDs[id] = true
			} else {
				delete(ui.selectedTaskIDs, id)
			}
		}
	}
	ui.syncSelectionWithSnapshot()
	ui.refreshTaskButtonsMulti()
	ui.refreshSummaryMulti(ui.snapshot)
	ui.applySelectionState()
}

func (ui *fyneUI) taskRowAtPosition(pos fyne.Position) int {
	if ui == nil || ui.taskList == nil || ui.app == nil {
		return -1
	}
	listPos := ui.app.Driver().AbsolutePositionForObject(ui.taskList)
	y := pos.Y - listPos.Y + ui.taskList.GetScrollOffset()
	if y < 0 {
		return -1
	}
	row := int(y / taskCardItemHeight)
	if row < 0 || row >= len(ui.rowTaskIDs) {
		return -1
	}
	return row
}

func (ui *fyneUI) selectAllTasks() {
	if ui.selectedTaskIDs == nil {
		ui.selectedTaskIDs = make(map[int]bool)
	}
	for _, task := range ui.snapshot.Tasks {
		if task == nil {
			continue
		}
		ui.selectedTaskIDs[task.ID] = true
	}
	ui.syncSelectionWithSnapshot()
	ui.refreshTaskButtonsMulti()
	ui.refreshSummaryMulti(ui.snapshot)
	ui.applySelectionState()
	ui.requestPersist()
}

func (ui *fyneUI) clearTaskSelection() {
	ui.selectedTaskIDs = make(map[int]bool)
	ui.syncSelectionWithSnapshot()
	ui.refreshTaskButtonsMulti()
	ui.refreshSummaryMulti(ui.snapshot)
	ui.applySelectionState()
	ui.requestPersist()
}

func (ui *fyneUI) updateHeaderCellMulti(id widget.TableCellID, obj fyne.CanvasObject) {
	cell, ok := obj.(*fyne.Container)
	if !ok || len(cell.Objects) < 3 {
		return
	}
	bg, _ := cell.Objects[0].(*canvas.Rectangle)
	progress, _ := cell.Objects[1].(*canvas.Rectangle)
	txt, _ := cell.Objects[2].(*canvas.Text)
	if bg == nil || txt == nil {
		return
	}
	headers := []string{"\u9009\u62e9", "\u7f16\u53f7", "\u72b6\u6001", "\u5904\u7406\u5668", "\u8fdb\u5ea6", "\u6807\u9898", "\u7f51\u5740"}
	if id.Col < 0 || id.Col >= len(headers) {
		txt.Text = ""
	} else {
		txt.Text = headers[id.Col]
	}
	bg.FillColor = color.NRGBA{R: 23, G: 26, B: 31, A: 255}
	txt.Color = color.NRGBA{R: 222, G: 226, B: 232, A: 255}
	txt.TextStyle = fyne.TextStyle{Bold: true}
	bg.Refresh()
	if progress != nil {
		progress.Hide()
		progress.Refresh()
	}
	txt.Refresh()
}

func (ui *fyneUI) updateTaskCellMulti(id widget.TableCellID, obj fyne.CanvasObject) {
	cell, ok := obj.(*fyne.Container)
	if !ok || len(cell.Objects) < 4 {
		return
	}
	bg, _ := cell.Objects[0].(*canvas.Rectangle)
	progress, _ := cell.Objects[1].(*canvas.Rectangle)
	btn, _ := cell.Objects[2].(*widget.Button)
	txt, _ := cell.Objects[3].(*canvas.Text)
	if bg == nil || txt == nil {
		return
	}
	if btn != nil {
		btn.Hide()
		btn.Refresh()
		btn.OnTapped = nil
	}
	if progress != nil {
		progress.Hide()
		progress.Refresh()
	}
	txt.Text = ""
	if id.Row < 0 || id.Row >= len(ui.rowTaskIDs) {
		bg.FillColor = color.NRGBA{R: 27, G: 30, B: 35, A: 255}
		bg.Refresh()
		txt.Refresh()
		return
	}
	task := ui.taskByRow(id.Row)
	selected := ui.taskSelected(task.ID)
	text := ""
	switch id.Col {
	case 0:
		if btn != nil {
			btn.Show()
			if selected {
				btn.SetText("[x]")
			} else {
				btn.SetText("[ ]")
			}
			btn.OnTapped = func() {
				ui.toggleTaskSelection(task.ID)
			}
			btn.Refresh()
		}
		text = ""
	case 1:
		text = fmt.Sprintf("%d", task.ID)
	case 2:
		text = string(task.State)
	case 3:
		text = displayTaskWorker(task)
	case 4:
		text = formatTaskPercent(task.Percent)
	case 5:
		text = displayTaskTitle(task)
	case 6:
		text = displayTaskURL(task.URL)
	}
	txt.Text = text
	if selected {
		bg.FillColor = color.NRGBA{R: 47, G: 60, B: 74, A: 255}
	} else if id.Row%2 == 0 {
		bg.FillColor = color.NRGBA{R: 27, G: 30, B: 35, A: 255}
	} else {
		bg.FillColor = color.NRGBA{R: 31, G: 34, B: 40, A: 255}
	}
	switch id.Col {
	case 2:
		txt.Color = taskStateColor(task.State)
	case 4:
		txt.Color = color.NRGBA{R: 194, G: 200, B: 208, A: 255}
		if progress != nil {
			percent := normalizedTaskPercent(task.Percent)
			progress.Show()
			size := cell.Size()
			barHeight := float32(3)
			barWidth := size.Width * float32(percent)
			if barWidth < 0 {
				barWidth = 0
			}
			progress.FillColor = taskStateColor(task.State)
			progress.Resize(fyne.NewSize(barWidth, barHeight))
			progress.Move(fyne.NewPos(0, size.Height-barHeight))
			progress.Refresh()
		}
	case 6:
		txt.Color = color.NRGBA{R: 152, G: 170, B: 188, A: 255}
	default:
		txt.Color = color.NRGBA{R: 227, G: 230, B: 235, A: 255}
	}
	txt.TextStyle = fyne.TextStyle{Bold: true}
	bg.Refresh()
	txt.Refresh()
}

func (ui *fyneUI) requestRefresh() {
	if ui == nil || ui.shutdownActive.Load() {
		return
	}
	if !ui.refreshPending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		time.Sleep(40 * time.Millisecond)
		if ui.app == nil {
			ui.refreshPending.Store(false)
			return
		}
		defer ui.refreshPending.Store(false)
		if runner, ok := ui.app.Driver().(interface{ RunOnMain(func()) }); ok {
			runner.RunOnMain(func() {
				ui.refresh()
			})
			return
		}
		ui.refresh()
	}()
}

func (ui *fyneUI) requestPersist() {
	if ui == nil || ui.closing.Load() || ui.shutdownActive.Load() || !ui.windowReady.Load() {
		return
	}
	if !ui.persistPending.CompareAndSwap(false, true) {
		ui.persistMu.Lock()
		if ui.persistTimer != nil {
			ui.persistTimer.Stop()
			ui.persistTimer.Reset(800 * time.Millisecond)
		} else {
			ui.persistPending.Store(false)
		}
		ui.persistMu.Unlock()
		return
	}
	ui.persistMu.Lock()
	ui.persistTimer = time.AfterFunc(800*time.Millisecond, func() {
		ui.persistMu.Lock()
		ui.persistTimer = nil
		ui.persistMu.Unlock()
		ui.persistPending.Store(false)
		ui.runOnMain(func() {
			ui.savePersistedState()
		})
	})
	ui.persistMu.Unlock()
}

func (ui *fyneUI) runOnMain(fn func()) {
	if ui == nil || fn == nil {
		return
	}
	if ui.app == nil {
		fn()
		return
	}
	if runner, ok := ui.app.Driver().(interface{ RunOnMain(func()) }); ok {
		runner.RunOnMain(fn)
		return
	}
	fn()
}

func (ui *fyneUI) refresh() {
	if ui == nil || ui.shutdownActive.Load() {
		return
	}
	snapshot := ui.manager.Snapshot()
	ui.snapshot = snapshot
	ui.applySnapshot(snapshot)
	ui.handleAutoRetry(snapshot)
	ui.refreshTaskList(snapshot)
	ui.refreshAdblock(snapshot)
	ui.refreshSummaryMulti(snapshot)
	ui.refreshTaskButtonsMulti()
	ui.requestPersist()
}

func (ui *fyneUI) handleAutoRetry(snapshot AppState) {
	if ui == nil || ui.manager == nil {
		return
	}
	// Automatic retries must be opt-in per error class. Retrying every TaskError
	// turns cancellation, unsupported URLs, and configuration failures back into
	// queued work.
	_ = snapshot
}

func (ui *fyneUI) applyPersistedUIState() {
	if ui == nil || ui.persistedState == nil {
		return
	}
	state := ui.persistedState
	if state.UI.Window.Valid {
		width := state.UI.Window.NormalPosition.Right - state.UI.Window.NormalPosition.Left
		height := state.UI.Window.NormalPosition.Bottom - state.UI.Window.NormalPosition.Top
		if width > 0 && height > 0 {
			ui.window.Resize(fyne.NewSize(float32(width), float32(height)))
		}
	}
	if draft := strings.TrimSpace(state.UI.URLDraft); draft != "" {
		ui.urlEntry.SetText(draft)
	}
	ui.downloadRootDraft = strings.TrimSpace(state.UI.DownloadRootDraft)
	if state.Concurrency > 0 {
		ui.concurrencyEntry.SetText(fmt.Sprintf("%d", state.Concurrency))
	}
	if preferred := currentChromiumPath(); preferred != "" {
		ui.rememberChromiumPath(preferred)
	} else if path := strings.TrimSpace(state.UI.ChromiumPath); path != "" {
		ui.rememberChromiumPath(path)
	}
	if len(state.UI.SelectedTaskIDs) > 0 {
		ui.selectedTaskIDs = make(map[int]bool, len(state.UI.SelectedTaskIDs))
		for _, id := range state.UI.SelectedTaskIDs {
			if id <= 0 {
				continue
			}
			ui.selectedTaskIDs[id] = true
		}
	}
	if len(state.UI.AutoRetryTaskIDs) > 0 {
		ui.autoRetryTaskIDs = make(map[int]bool, len(state.UI.AutoRetryTaskIDs))
		for _, id := range state.UI.AutoRetryTaskIDs {
			if id <= 0 {
				continue
			}
			ui.autoRetryTaskIDs[id] = true
		}
	}
	ui.refreshTaskButtonsMulti()
}

func (ui *fyneUI) savePersistedState() {
	ui.savePersistedStateWithWindow(persistedWindowPlacement{})
}

func (ui *fyneUI) savePersistedStateWithWindow(windowPlacement persistedWindowPlacement) {
	if ui == nil || ui.closing.Load() && !ui.windowReady.Load() {
		return
	}
	if !windowPlacement.Valid {
		windowPlacement = ui.captureWindowPlacement()
	}
	if !windowPlacement.Valid && ui.persistedState != nil && ui.persistedState.UI.Window.Valid {
		windowPlacement = ui.persistedState.UI.Window
	}
	snapshot := ui.manager.Snapshot()
	ui.snapshot = snapshot
	state := &persistedAppState{
		Version:     persistedAppStateVersion,
		SavedAt:     time.Now(),
		NextTaskID:  snapshot.NextID,
		Concurrency: snapshot.Concurrency,
		UI: persistedUIState{
			ActiveTab:         fyneTabBase,
			URLDraft:          strings.TrimSpace(ui.urlEntry.Text),
			DownloadRootDraft: strings.TrimSpace(ui.downloadRootDraft),
			ChromiumPath:      strings.TrimSpace(ui.chromiumPathValue()),
			SelectedTaskIDs:   ui.selectedTaskIDsList(),
			AutoRetryTaskIDs:  ui.autoRetryTaskIDsList(),
			Window:            windowPlacement,
		},
	}
	if len(snapshot.Tasks) > 0 {
		state.Tasks = make([]*Task, 0, len(snapshot.Tasks))
		for _, task := range snapshot.Tasks {
			if task == nil {
				continue
			}
			copyTask := *task
			state.Tasks = append(state.Tasks, &copyTask)
		}
	}
	persistedStateMu.Lock()
	err := savePersistedAppState(state)
	persistedStateMu.Unlock()
	if err != nil {
		log.Printf("state save failed: %v", err)
		return
	}
	ui.persistedState = state
}

func (ui *fyneUI) captureWindowPlacement() persistedWindowPlacement {
	var result persistedWindowPlacement
	hwnd := ui.nativeWindowHandle()
	if hwnd == 0 {
		log.Printf("window placement capture failed: native window unavailable")
		return result
	}
	var placement win.WINDOWPLACEMENT
	placement.Length = uint32(unsafe.Sizeof(placement))
	if !win.GetWindowPlacement(hwnd, &placement) {
		var rect win.RECT
		if !win.GetWindowRect(hwnd, &rect) {
			log.Printf("window placement capture failed: GetWindowPlacement/GetWindowRect unavailable")
			return result
		}
		if !validWindowRect(rect) {
			log.Printf("window placement capture ignored small rect=%d,%d,%d,%d", rect.Left, rect.Top, rect.Right, rect.Bottom)
			return result
		}
		result.Valid = true
		result.NormalPosition = persistedRect{
			Left:   rect.Left,
			Top:    rect.Top,
			Right:  rect.Right,
			Bottom: rect.Bottom,
		}
		return result
	}
	result.Valid = true
	result.Flags = placement.Flags
	result.ShowCmd = placement.ShowCmd
	result.MinPosition = persistedPoint{X: placement.PtMinPosition.X, Y: placement.PtMinPosition.Y}
	result.MaxPosition = persistedPoint{X: placement.PtMaxPosition.X, Y: placement.PtMaxPosition.Y}
	result.NormalPosition = persistedRect{
		Left:   placement.RcNormalPosition.Left,
		Top:    placement.RcNormalPosition.Top,
		Right:  placement.RcNormalPosition.Right,
		Bottom: placement.RcNormalPosition.Bottom,
	}
	switch placement.ShowCmd {
	case win.SW_SHOWMINIMIZED, win.SW_MINIMIZE, win.SW_FORCEMINIMIZE, win.SW_SHOWMAXIMIZED:
		// Keep WINDOWPLACEMENT's normal rect for minimized/maximized windows.
	default:
		var rect win.RECT
		if win.GetWindowRect(hwnd, &rect) && validWindowRect(rect) {
			result.NormalPosition = persistedRect{
				Left:   rect.Left,
				Top:    rect.Top,
				Right:  rect.Right,
				Bottom: rect.Bottom,
			}
		}
	}
	if !validPersistedRect(result.NormalPosition) {
		log.Printf("window placement capture ignored small normal rect=%d,%d,%d,%d",
			result.NormalPosition.Left,
			result.NormalPosition.Top,
			result.NormalPosition.Right,
			result.NormalPosition.Bottom,
		)
		return persistedWindowPlacement{}
	}
	log.Printf("window placement captured showCmd=%d rect=%d,%d,%d,%d",
		result.ShowCmd,
		result.NormalPosition.Left,
		result.NormalPosition.Top,
		result.NormalPosition.Right,
		result.NormalPosition.Bottom,
	)
	return result
}

func (ui *fyneUI) restoreWindowPlacement() {
	if ui == nil || ui.persistedState == nil || !ui.persistedState.UI.Window.Valid {
		return
	}
	state := ui.persistedState.UI.Window
	if !validPersistedRect(state.NormalPosition) {
		log.Printf("window placement restore skipped invalid rect=%d,%d,%d,%d",
			state.NormalPosition.Left,
			state.NormalPosition.Top,
			state.NormalPosition.Right,
			state.NormalPosition.Bottom,
		)
		return
	}
	showCmd := state.ShowCmd
	switch showCmd {
	case win.SW_SHOWMINIMIZED, win.SW_MINIMIZE, win.SW_FORCEMINIMIZE:
		// Reopen in a visible state even if the app was closed while minimized.
		showCmd = win.SW_RESTORE
	}
	hwnd := ui.nativeWindowHandle()
	if hwnd == 0 {
		log.Printf("window placement restore deferred: native window not ready")
		return
	}
	width := state.NormalPosition.Right - state.NormalPosition.Left
	height := state.NormalPosition.Bottom - state.NormalPosition.Top
	if width > 0 && height > 0 {
		win.SetWindowPos(hwnd, 0, state.NormalPosition.Left, state.NormalPosition.Top, width, height, win.SWP_NOZORDER|win.SWP_NOACTIVATE)
		log.Printf("window placement moved rect=%d,%d,%d,%d",
			state.NormalPosition.Left,
			state.NormalPosition.Top,
			state.NormalPosition.Right,
			state.NormalPosition.Bottom,
		)
	}
	if showCmd != win.SW_SHOWMAXIMIZED && showCmd != win.SW_MAXIMIZE {
		return
	}
	var placement win.WINDOWPLACEMENT
	placement.Length = uint32(unsafe.Sizeof(placement))
	placement.Flags = state.Flags
	placement.ShowCmd = showCmd
	placement.PtMinPosition = win.POINT{X: state.MinPosition.X, Y: state.MinPosition.Y}
	placement.PtMaxPosition = win.POINT{X: state.MaxPosition.X, Y: state.MaxPosition.Y}
	placement.RcNormalPosition = win.RECT{
		Left:   state.NormalPosition.Left,
		Top:    state.NormalPosition.Top,
		Right:  state.NormalPosition.Right,
		Bottom: state.NormalPosition.Bottom,
	}
	if win.SetWindowPlacement(hwnd, &placement) {
		log.Printf("window placement restored showCmd=%d rect=%d,%d,%d,%d",
			showCmd,
			state.NormalPosition.Left,
			state.NormalPosition.Top,
			state.NormalPosition.Right,
			state.NormalPosition.Bottom,
		)
		return
	}
}

func (ui *fyneUI) hideNativeWindowForRestore() {
	if ui == nil || ui.persistedState == nil || !ui.persistedState.UI.Window.Valid {
		return
	}
	hwnd := ui.nativeWindowHandle()
	if hwnd == 0 {
		return
	}
	win.ShowWindow(hwnd, win.SW_HIDE)
}

func (ui *fyneUI) showNativeWindowAfterRestore() {
	if ui == nil || ui.persistedState == nil || !ui.persistedState.UI.Window.Valid {
		return
	}
	time.AfterFunc(80*time.Millisecond, func() {
		if ui == nil || ui.closing.Load() || ui.shutdownActive.Load() {
			return
		}
		hwnd := ui.nativeWindowHandle()
		if hwnd == 0 {
			ui.runOnMain(func() {
				if ui.window != nil {
					ui.window.Show()
				}
			})
			return
		}
		ui.restoreWindowPlacement()
		win.ShowWindow(hwnd, win.SW_SHOW)
	})
}

func (ui *fyneUI) placeWindowBeforeShow() {
	if ui == nil || ui.window == nil || ui.persistedState == nil || !ui.persistedState.UI.Window.Valid {
		return
	}
	hwnd := ui.nativeWindowHandle()
	if hwnd == 0 {
		return
	}
	state := ui.persistedState.UI.Window
	if !validPersistedRect(state.NormalPosition) {
		return
	}
	width := state.NormalPosition.Right - state.NormalPosition.Left
	height := state.NormalPosition.Bottom - state.NormalPosition.Top
	win.SetWindowPos(hwnd, 0, state.NormalPosition.Left, state.NormalPosition.Top, width, height, win.SWP_NOZORDER|win.SWP_NOACTIVATE|win.SWP_NOREDRAW)
	log.Printf("window placement pre-show rect=%d,%d,%d,%d",
		state.NormalPosition.Left,
		state.NormalPosition.Top,
		state.NormalPosition.Right,
		state.NormalPosition.Bottom,
	)
}

func (ui *fyneUI) scheduleRestoreWindowPlacement() {
	if ui == nil || ui.persistedState == nil || !ui.persistedState.UI.Window.Valid {
		return
	}
	for _, delay := range []time.Duration{250 * time.Millisecond, 900 * time.Millisecond} {
		time.AfterFunc(delay, func() {
			if ui == nil || ui.closing.Load() || ui.shutdownActive.Load() {
				return
			}
			ui.restoreWindowPlacement()
		})
	}
}

func (ui *fyneUI) runOnNativeWindow(fn func(win.HWND)) {
	if ui == nil || fn == nil || ui.window == nil {
		return
	}
	native, ok := ui.window.(driver.NativeWindow)
	if !ok {
		return
	}
	native.RunNative(func(context any) {
		ctx, ok := context.(driver.WindowsWindowContext)
		if !ok || ctx.HWND == 0 {
			return
		}
		fn(win.HWND(ctx.HWND))
	})
}

func (ui *fyneUI) nativeWindowHandle() win.HWND {
	return currentProcessMainWindow()
}

func currentProcessMainWindow() win.HWND {
	var result win.HWND
	var resultArea int64
	pid := uint32(os.Getpid())
	callback := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		handle := win.HWND(hwnd)
		var windowPID uint32
		win.GetWindowThreadProcessId(handle, &windowPID)
		if windowPID != pid {
			return 1
		}
		if !win.IsWindowVisible(handle) {
			return 1
		}
		if win.GetParent(handle) != 0 {
			return 1
		}
		var rect win.RECT
		if !win.GetWindowRect(handle, &rect) || !validWindowRect(rect) {
			return 1
		}
		width := int64(rect.Right - rect.Left)
		height := int64(rect.Bottom - rect.Top)
		area := width * height
		if area > resultArea {
			result = handle
			resultArea = area
		}
		return 1
	})
	enumWindowsProc.Call(callback, 0)
	return result
}

func validWindowRect(rect win.RECT) bool {
	return rect.Right-rect.Left >= 300 && rect.Bottom-rect.Top >= 200
}

func validPersistedRect(rect persistedRect) bool {
	return rect.Right-rect.Left >= 300 && rect.Bottom-rect.Top >= 200
}

func (ui *fyneUI) applySnapshot(snapshot AppState) {
	ui.opsValue1.SetText(fmt.Sprintf("\u961f\u5217 %d | \u8fd0\u884c %d | \u5b8c\u6210 %d | \u5931\u8d25 %d", snapshot.Counts.Queued, snapshot.Counts.Running, snapshot.Counts.Done, snapshot.Counts.Error))
	ui.opsValue2.SetText(fmt.Sprintf("\u65e5\u5fd7 %d | \u6392\u961f %d", len(snapshot.Logs), snapshot.Queue))
	ui.themeValue1.SetText("\u6df1\u8272\u4e3b\u9898")
	ui.themeValue2.SetText("Fyne + Go")
}

func (ui *fyneUI) refreshSummaryMulti(snapshot AppState) {
	selectedCount := len(ui.selectedTaskIDsList())
	selected := "\u65e0"
	if selectedCount > 0 {
		selected = fmt.Sprintf("%d \u9879", selectedCount)
	}
	status := ui.buildStatusText(snapshot, selected)
	if status != ui.lastStatusSig {
		ui.statusLabel.SetText(status)
		ui.lastStatusSig = status
	}
}

func (ui *fyneUI) buildStatusText(snapshot AppState, selected string) string {
	return fmt.Sprintf("\u5c31\u7eea | \u961f\u5217 %d | \u8fd0\u884c %d | \u5b8c\u6210 %d | \u5931\u8d25 %d | \u5df2\u9009 %s",
		snapshot.Queue, snapshot.Counts.Running, snapshot.Counts.Done, snapshot.Counts.Error, selected)
}
func (ui *fyneUI) refreshTaskButtonsMulti() {
	ui.pauseBtn.Disable()
	ui.resumeBtn.Disable()
	ui.retryBtn.Disable()
	ui.cancelBtn.Disable()
	ui.deleteBtn.Disable()
	for _, id := range ui.selectedTaskIDsList() {
		task := ui.taskByID(id)
		if canTaskAction(task, "pause") {
			ui.pauseBtn.Enable()
		}
		if canTaskAction(task, "resume") {
			ui.resumeBtn.Enable()
		}
		if canTaskAction(task, "retry") {
			ui.retryBtn.Enable()
		}
		if canTaskAction(task, "cancel") {
			ui.cancelBtn.Enable()
		}
		if canTaskAction(task, "delete") {
			ui.deleteBtn.Enable()
		}
	}
}

func (ui *fyneUI) runSelectedActionMulti(action string) {
	ids := ui.selectedTaskIDsList()
	if len(ids) == 0 {
		ui.setStatus("鏈€夋嫨浠诲姟")
		return
	}
	ui.setStatus(fmt.Sprintf("%s 鎵ц涓?..", actionName(action)))
	go func(ids []int, action string) {
		okCount := ui.manager.ApplyTaskActionBatch(ids, action)
		ui.runOnMain(func() {
			if okCount > 0 {
				ui.setStatus(fmt.Sprintf("%s: %d/%d", actionName(action), okCount, len(ids)))
			} else {
				ui.setStatus(fmt.Sprintf("%s \u5931\u8d25", actionName(action)))
			}
			ui.requestRefresh()
		})
	}(append([]int(nil), ids...), action)
}

func (ui *fyneUI) refreshSummary(snapshot AppState) {
	ui.refreshSummaryMulti(snapshot)
}

func (ui *fyneUI) refreshTaskList(snapshot AppState) {
	prevCount := len(ui.rowTaskIDs)
	ui.rowTaskIDs = ui.rowTaskIDs[:0]
	ui.rowTaskIDs = make([]int, 0, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		if task == nil {
			continue
		}
		ui.rowTaskIDs = append(ui.rowTaskIDs, task.ID)
	}
	ui.syncSelectionWithSnapshot()
	if ui.taskList != nil && len(ui.rowTaskIDs) != prevCount {
		ui.taskList.Refresh()
		return
	}
	ui.refreshVisibleTaskCards()
}

func (ui *fyneUI) applySelectionState() {
	ui.refreshVisibleTaskCards()
}

func (ui *fyneUI) refreshTaskSelectionState() {
	if ui == nil {
		return
	}
	ui.refreshVisibleTaskCards()
}

func (ui *fyneUI) refreshVisibleTaskCards() {
	if ui == nil {
		return
	}
	if ui.taskList == nil {
		return
	}
	start, end := ui.visibleTaskRowRange()
	for row := start; row <= end; row++ {
		ui.taskList.RefreshItem(widget.ListItemID(row))
	}
}

func (ui *fyneUI) visibleTaskRowRange() (int, int) {
	if ui == nil || ui.taskList == nil {
		return 0, -1
	}
	total := len(ui.rowTaskIDs)
	if total == 0 {
		return 0, -1
	}
	offset := ui.taskList.GetScrollOffset()
	height := ui.taskList.Size().Height
	if height <= 0 {
		height = taskCardItemHeight * 4
	}
	start := int(offset / taskCardItemHeight)
	if start > 0 {
		start--
	}
	if start < 0 {
		start = 0
	}
	end := int((offset + height) / taskCardItemHeight)
	if end < total-1 {
		end++
	}
	if end >= total {
		end = total - 1
	}
	if end < start {
		end = start
	}
	return start, end
}

func (ui *fyneUI) refreshAdblock(snapshot AppState) {
	ui.adSource.SetText(blankIfEmpty(snapshot.Adblock.Source))
	ui.adURL.SetText(blankIfEmpty(snapshot.Adblock.URL))
	ui.adDate.SetText(blankIfEmpty(snapshot.Adblock.UpdatedAt))
	ui.adPattern.SetText(fmt.Sprintf("%d", snapshot.Adblock.PatternCount))
	if snapshot.Adblock.Ready {
		ui.adReady.SetText("\u5df2\u52a0\u8f7d")
	} else {
		ui.adReady.SetText("\u672a\u5c31\u7eea")
	}
}
func (ui *fyneUI) addTaskFromInput() {
	url := strings.TrimSpace(ui.urlEntry.Text)
	if url == "" {
		ui.setStatus("璇疯緭鍏ョ綉鍧€")
		return
	}
	downloadRoot := strings.TrimSpace(ui.downloadRootDraft)
	ui.addTaskWithValuesPrompted(url, downloadRoot)
}

func (ui *fyneUI) addTaskWithValuesPrompted(url, downloadRoot string) {
	if ui == nil || ui.manager == nil {
		return
	}
	if existing := ui.manager.FindTaskByURL(url); existing != nil {
		ui.showDuplicateTaskPrompt(url, downloadRoot, existing)
		return
	}
	ui.addTaskWithValues(url, downloadRoot)
}

func (ui *fyneUI) addTaskWithValuesForced(url, downloadRoot string) {
	if ui == nil || ui.manager == nil {
		return
	}
	ui.addTaskWithValues(url, downloadRoot)
}

func (ui *fyneUI) addTaskWithValues(url, downloadRoot string) {
	if ui == nil || ui.manager == nil {
		return
	}
	if !ui.ensureChromiumReady(func() { ui.addTaskWithValues(url, downloadRoot) }) {
		return
	}
	headless := defaultHeadlessForRoute(prefilterSiteRoute(url), url)
	task := ui.manager.AddTask(url, "", downloadRoot, headless, false)
	ui.urlEntry.SetText("")
	ui.setStatus(fmt.Sprintf("\u4efb\u52a1 #%d \u5df2\u6dfb\u52a0", task.ID))
	log.Printf("ui add task id=%d url=%s worker=%s route=%s downloadRoot=%s", task.ID, url, task.Worker, task.WorkerSource, task.DownloadRoot)
	ui.requestPersist()
	ui.requestRefresh()
}

func (ui *fyneUI) showImportHistoryDialog() {
	if ui == nil || ui.window == nil {
		return
	}
	var owner win.HWND
	ui.runOnNativeWindow(func(hwnd win.HWND) {
		owner = hwnd
	})
	path, ok, err := openNativeHistoryStateFile(owner)
	if err != nil {
		ui.setStatus(fmt.Sprintf("\u5bfc\u5165\u5386\u53f2\u8bb0\u5f55\u5931\u8d25\uff1a%v", err))
		return
	}
	if !ok {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		ui.setStatus(fmt.Sprintf("\u5bfc\u5165\u5386\u53f2\u8bb0\u5f55\u5931\u8d25\uff1a%v", err))
		return
	}
	state, err := parsePersistedAppState(data)
	if err != nil {
		ui.setStatus(fmt.Sprintf("\u5bfc\u5165\u5386\u53f2\u8bb0\u5f55\u5931\u8d25\uff1a%v", err))
		return
	}
	result := ui.manager.ImportHistoryTasks(state.Tasks)
	if len(result.ErrorIDs) > 0 {
		if ui.autoRetryTaskIDs == nil {
			ui.autoRetryTaskIDs = make(map[int]bool, len(result.ErrorIDs))
		}
		for _, id := range result.ErrorIDs {
			if id > 0 {
				ui.autoRetryTaskIDs[id] = true
			}
		}
	}
	ui.setStatus(fmt.Sprintf("\u5bfc\u5165\u5386\u53f2\u8bb0\u5f55\u5b8c\u6210\uff1a\u5bfc\u5165 %d\uff0c\u8df3\u8fc7 %d", result.Imported, result.Skipped))
	ui.requestPersist()
	ui.requestRefresh()
}

func openNativeHistoryStateFile(owner win.HWND) (string, bool, error) {
	fileBuffer := make([]uint16, 4096)
	copy(fileBuffer, syscall.StringToUTF16("comic_downloader_state.json"))
	filter := nativeFileDialogFilter(
		"JSON \u6587\u4ef6 (*.json)", "*.json",
		"\u6240\u6709\u6587\u4ef6 (*.*)", "*.*",
	)
	title, _ := syscall.UTF16PtrFromString("\u5bfc\u5165\u5386\u53f2\u8bb0\u5f55")
	defExt, _ := syscall.UTF16PtrFromString("json")

	ofn := win.OPENFILENAME{
		LStructSize: uint32(unsafe.Sizeof(win.OPENFILENAME{})),
		HwndOwner:   owner,
		LpstrFilter: &filter[0],
		LpstrFile:   &fileBuffer[0],
		NMaxFile:    uint32(len(fileBuffer)),
		LpstrTitle:  title,
		LpstrDefExt: defExt,
		Flags:       win.OFN_EXPLORER | win.OFN_FILEMUSTEXIST | win.OFN_PATHMUSTEXIST | win.OFN_HIDEREADONLY | win.OFN_NOCHANGEDIR,
	}
	if win.GetOpenFileName(&ofn) {
		path := syscall.UTF16ToString(fileBuffer)
		return strings.TrimSpace(path), true, nil
	}
	if code := win.CommDlgExtendedError(); code != 0 {
		return "", false, fmt.Errorf("Windows \u6587\u4ef6\u9009\u62e9\u5668\u9519\u8bef 0x%X", code)
	}
	return "", false, nil
}

func nativeFileDialogFilter(parts ...string) []uint16 {
	result := make([]uint16, 0, 128)
	for _, part := range parts {
		encoded := syscall.StringToUTF16(part)
		result = append(result, encoded[:len(encoded)-1]...)
		result = append(result, 0)
	}
	result = append(result, 0)
	return result
}
func (ui *fyneUI) showDownloadRootDialog() {
	if ui == nil || ui.window == nil {
		return
	}
	entry := widget.NewEntry()
	entry.SetPlaceHolder("\u4e0b\u8f7d\u76ee\u5f55\uff08\u7559\u7a7a\u5219\u4f7f\u7528 ./download\uff09")
	entry.SetText(strings.TrimSpace(ui.downloadRootDraft))
	form := dialog.NewForm("\u4e0b\u8f7d\u76ee\u5f55", "\u4fdd\u5b58", "\u53d6\u6d88", []*widget.FormItem{widget.NewFormItem("\u8def\u5f84", entry)}, func(ok bool) {
		if !ok {
			return
		}
		text := strings.TrimSpace(entry.Text)
		ui.downloadRootDraft = text
		if text == "" {
			ui.setStatus("\u4e0b\u8f7d\u76ee\u5f55\u5df2\u6062\u590d\u4e3a\u9ed8\u8ba4\u503c")
		} else {
			ui.setStatus("\u4e0b\u8f7d\u76ee\u5f55\u5df2\u66f4\u65b0")
		}
		ui.requestPersist()
	}, ui.window)
	form.Resize(fyne.NewSize(520, 180))
	form.Show()
}
func (ui *fyneUI) showConcurrencyDialog() {
	if ui == nil || ui.window == nil {
		return
	}
	entry := widget.NewEntry()
	entry.SetPlaceHolder("1-16")
	entry.SetText(strings.TrimSpace(ui.concurrencyEntry.Text))
	form := dialog.NewForm("\u5e76\u53d1\u6570", "\u5e94\u7528", "\u53d6\u6d88", []*widget.FormItem{widget.NewFormItem("\u5de5\u4f5c\u7ebf\u7a0b", entry)}, func(ok bool) {
		if !ok {
			return
		}
		raw := strings.TrimSpace(entry.Text)
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 16 {
			ui.setStatus("\u5e76\u53d1\u6570\u5fc5\u987b\u5728 1 \u5230 16 \u4e4b\u95f4")
			return
		}
		ui.concurrencyEntry.SetText(fmt.Sprintf("%d", value))
		ui.applyConcurrencyFromInput()
	}, ui.window)
	form.Resize(fyne.NewSize(420, 180))
	form.Show()
}
func (ui *fyneUI) updateAdblockRulesFromMenu() {
	if ui == nil || ui.window == nil {
		return
	}
	ui.setStatus("\u6b63\u5728\u66f4\u65b0\u5e7f\u544a\u62e6\u622a\u89c4\u5219...")
	go func() {
		err := updateAdblockRulesNow()
		ui.runOnMain(func() {
			if err != nil {
				log.Printf("adblock manual refresh failed: %v", err)
				dialog.ShowError(err, ui.window)
				ui.setStatus("\u5e7f\u544a\u62e6\u622a\u89c4\u5219\u66f4\u65b0\u5931\u8d25")
				ui.requestRefresh()
				return
			}
			ui.setStatus("\u5e7f\u544a\u62e6\u622a\u89c4\u5219\u5df2\u66f4\u65b0")
			ui.requestRefresh()
			dialog.ShowInformation("\u5e7f\u544a\u62e6\u622a\u5df2\u66f4\u65b0", "\u5e7f\u544a\u62e6\u622a\u89c4\u5219\u5df2\u5237\u65b0\u6210\u529f\u3002", ui.window)
		})
	}()
}
func (ui *fyneUI) applyConcurrencyFromInput() {
	if ui == nil || ui.manager == nil {
		return
	}
	raw := strings.TrimSpace(ui.concurrencyEntry.Text)
	if raw == "" {
		ui.setStatus("璇疯緭鍏ュ苟鍙戞暟")
		return
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		ui.setStatus("骞跺彂鏁板繀椤绘槸鏁板瓧")
		return
	}
	value := ui.manager.SetConcurrency(n)
	ui.concurrencyEntry.SetText(fmt.Sprintf("%d", value))
	ui.setStatus(fmt.Sprintf("骞跺彂鏁板凡璁句负 %d", value))
	ui.requestPersist()
	ui.requestRefresh()
}

func (ui *fyneUI) chromiumPathValue() string {
	if ui == nil {
		return ""
	}
	if path := strings.TrimSpace(ui.chromiumPath); path != "" {
		if resolved := resolveChromiumExecutablePath(path); resolved != "" {
			return resolved
		}
	}
	if resolved := currentChromiumPath(); resolved != "" {
		ui.chromiumPath = resolved
		setChromiumPath(resolved)
		return resolved
	}
	return ""
}

func (ui *fyneUI) rememberChromiumPath(path string) {
	if ui == nil {
		return
	}
	resolved := resolveChromiumExecutablePath(path)
	if resolved == "" {
		resolved = resolveChromiumExecutableInDir(path)
	}
	if resolved == "" {
		return
	}
	ui.chromiumPath = resolved
	setChromiumPath(resolved)
	if ui.persistedState != nil {
		ui.persistedState.UI.ChromiumPath = resolved
	}
	ui.requestPersist()
}

func (ui *fyneUI) chromiumReady() bool {
	return ui.chromiumPathValue() != ""
}

func (ui *fyneUI) ensureChromiumReady(onReady func()) bool {
	if ui == nil {
		return true
	}
	if ui.chromiumReady() {
		if onReady != nil {
			onReady()
		}
		return true
	}
	ui.showChromiumSetupDialog(onReady)
	return false
}

func (ui *fyneUI) showChromiumSetupDialog(onReady func()) {
	if ui == nil || ui.window == nil {
		return
	}
	if !ui.chromiumSetupActive.CompareAndSwap(false, true) {
		return
	}
	info := widget.NewLabel("\u672a\u627e\u5230 Chromium\u3002\u53ef\u81ea\u52a8\u4e0b\u8f7d\u5230\u672c\u9879\u76ee\u8fd0\u884c\u76ee\u5f55\uff0c\u6216\u9009\u62e9\u672c\u5730\u5df2\u6709\u7684 Chromium \u76ee\u5f55\u3002")
	info.Alignment = fyne.TextAlignLeading
	info.Wrapping = fyne.TextWrapWord
	current := widget.NewLabelWithStyle("\u5f53\u524d\u8def\u5f84\uff1a"+func() string {
		if path := ui.chromiumPathValue(); path != "" {
			return path
		}
		return "<\u672a\u627e\u5230>"
	}(), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	autoBtn := widget.NewButton("\u81ea\u52a8\u4e0b\u8f7d", func() { go ui.runChromiumAutoDownload(onReady) })
	chooseBtn := widget.NewButton("\u9009\u62e9\u5df2\u6709 Chromium", func() { ui.chooseChromiumDirectory(onReady) })
	cancelBtn := widget.NewButton("\u5173\u95ed", func() { ui.chromiumSetupActive.Store(false) })
	content := container.NewVBox(info, current, widget.NewSeparator(), container.NewHBox(autoBtn, chooseBtn, cancelBtn))
	dlg := dialog.NewCustom("\u9700\u8981 Chromium", "", content, ui.window)
	dlg.Resize(fyne.NewSize(680, 220))
	dlg.SetOnClosed(func() { ui.chromiumSetupActive.Store(false) })
	dlg.Show()
}
func (ui *fyneUI) runChromiumAutoDownload(onReady func()) {
	runtimeRoot := runtimeRootDir()
	ui.runOnMain(func() { ui.setStatus("\u6b63\u5728\u4e0b\u8f7d Chromium...") })
	chromiumExe, err := downloadChromiumToRuntime(runtimeRoot)
	if err != nil {
		ui.runOnMain(func() {
			ui.chromiumSetupActive.Store(false)
			dialog.ShowError(err, ui.window)
			ui.setStatus("Chromium \u4e0b\u8f7d\u5931\u8d25")
		})
		return
	}
	ui.runOnMain(func() {
		ui.rememberChromiumPath(chromiumExe)
		ui.chromiumSetupActive.Store(false)
		ui.setStatus("Chromium \u5df2\u5c31\u7eea")
		if onReady != nil {
			onReady()
		}
		dialog.ShowInformation("Chromium \u5df2\u5c31\u7eea", "Chromium \u5df2\u4e0b\u8f7d\u5e76\u9a8c\u8bc1\u6210\u529f\u3002", ui.window)
	})
}
func (ui *fyneUI) chooseChromiumDirectory(onReady func()) {
	if ui == nil || ui.window == nil {
		return
	}
	dlg := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		ui.chromiumSetupActive.Store(false)
		if err != nil {
			dialog.ShowError(err, ui.window)
			return
		}
		if uri == nil {
			return
		}
		selected := strings.TrimSpace(uri.Path())
		resolved := resolveChromiumExecutablePath(selected)
		if resolved == "" {
			resolved = resolveChromiumExecutableInDir(selected)
		}
		if resolved == "" {
			dialog.ShowError(fmt.Errorf("chrome.exe was not found in %s", selected), ui.window)
			return
		}
		if err := verifyChromiumExecutable(resolved); err != nil {
			dialog.ShowError(err, ui.window)
			return
		}
		ui.rememberChromiumPath(resolved)
		ui.setStatus("Chromium \u8def\u5f84\u5df2\u66f4\u65b0")
		if onReady != nil {
			onReady()
		}
		dialog.ShowInformation("Chromium \u5df2\u5c31\u7eea", "\u6240\u9009 Chromium \u8def\u5f84\u9a8c\u8bc1\u6210\u529f\u3002", ui.window)
	}, ui.window)
	dlg.SetOnClosed(func() { ui.chromiumSetupActive.Store(false) })
	dlg.Show()
}
func (ui *fyneUI) runSelectedAction(action string) {
	ui.runSelectedActionMulti(action)
}

func actionName(action string) string {
	switch action {
	case "pause":
		return "\u6682\u505c"
	case "resume":
		return "\u7ee7\u7eed"
	case "retry":
		return "\u91cd\u8bd5"
	case "cancel":
		return "\u53d6\u6d88"
	default:
		return action
	}
}
func (ui *fyneUI) setStatus(text string) {
	ui.statusLabel.SetText(text)
}

func (ui *fyneUI) setExitProgress(logText, stepText string) {
	if ui == nil {
		return
	}
	if ui.statusLabel != nil {
		text := strings.TrimSpace(logText)
		step := strings.TrimSpace(stepText)
		switch {
		case text != "" && step != "":
			ui.statusLabel.SetText(text + " | " + step)
		case text != "":
			ui.statusLabel.SetText(text)
		default:
			ui.statusLabel.SetText(step)
		}
	}
}

func formatTaskPercent(percent float64) string {
	if percent <= 0 {
		return "0%"
	}
	normalized := normalizedTaskPercent(percent)
	if normalized >= 1 {
		return "100%"
	}
	value := normalized * 100
	if value == float64(int(value)) {
		return fmt.Sprintf("%.0f%%", value)
	}
	return fmt.Sprintf("%.1f%%", value)
}

func normalizedTaskPercent(percent float64) float64 {
	if percent <= 0 {
		return 0
	}
	if percent <= 1 {
		return percent
	}
	if percent >= 100 {
		return 1
	}
	return percent / 100
}

func blankIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
