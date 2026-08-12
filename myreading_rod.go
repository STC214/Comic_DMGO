package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	"github.com/lxn/win"
)

type rodMyreadingBrowser struct {
	browser *rod.Browser
	page    *rod.Page
	process *manualChromiumProcess
	close   sync.Once
}

type manualChromiumProcess struct {
	cmd      *exec.Cmd
	exit     <-chan error
	port     int
	control  string
	profile  string
	close    sync.Once
	windowMu sync.Mutex
	window   win.HWND
}

func newRodMyreadingBrowser(bin, profile string, headless bool, proxy string) (myreadingBrowser, error) {
	process, err := startManualChromium(bin, profile, headless, proxy)
	if err != nil {
		return nil, err
	}
	// Rod otherwise injects its 1280x800 laptop device emulation even into an
	// already-open headed window. Clear it so layout, UA and viewport are the
	// native Chromium values.
	b := rod.New().NoDefaultDevice().ControlURL(process.control).Timeout(90 * time.Second)
	if err = b.Connect(); err != nil {
		process.Close()
		return nil, err
	}
	var p *rod.Page
	if headless {
		p, err = stealth.Page(b)
	} else {
		// Attach to the tab created by the normal Chrome launch. Creating another
		// CDP target changes the native window/viewport and no longer resembles a
		// browser opened manually by the user.
		var pages rod.Pages
		pages, err = b.Pages()
		if err == nil {
			if len(pages) == 0 {
				err = fmt.Errorf("manual Chromium opened without a page")
			} else {
				// Chrome appends the command-line URL tab after any profile-restored
				// tabs. Use that native tab instead of an older background target.
				p = pages[len(pages)-1]
			}
		}
	}
	if err != nil {
		b.Close()
		process.Close()
		return nil, err
	}
	return &rodMyreadingBrowser{browser: b, page: p, process: process}, nil
}

func startManualChromium(bin, profile string, headless bool, proxy string) (*manualChromiumProcess, error) {
	port, err := freeLoopbackPort()
	if err != nil {
		return nil, fmt.Errorf("allocate browser port: %w", err)
	}
	args := []string{
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-session-crashed-bubble",
	}
	if headless {
		args = append(args, "--headless=new")
	} else {
		// Create the headed browser outside the desktop before its first frame.
		// Once CDP is ready we hide the native HWND as a second barrier. If a
		// challenge is found SetVisible(true) restores and maximizes this same real
		// browser, so the verification viewport remains identical to manual Chrome.
		width := int(win.GetSystemMetrics(win.SM_CXSCREEN))
		height := int(win.GetSystemMetrics(win.SM_CYSCREEN))
		if width < 1280 {
			width = 1920
		}
		if height < 720 {
			height = 1080
		}
		args = append(args,
			"--window-position=-32000,-32000",
			fmt.Sprintf("--window-size=%d,%d", width, height),
			"--disable-background-timer-throttling",
			"--disable-backgrounding-occluded-windows",
			"--disable-renderer-backgrounding",
		)
	}
	if strings.TrimSpace(proxy) != "" {
		args = append(args, "--proxy-server="+strings.TrimSpace(proxy))
	}
	args = append(args, "about:blank")
	cmd := exec.Command(strings.TrimSpace(bin), args...)
	cmd.SysProcAttr = hiddenSysProcAttr()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	log.Printf("myreading Rod direct Chromium start bin=%s headless=%v profile=%s port=%d", bin, headless, profile, port)
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start real Chromium: %w", err)
	}
	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait() }()
	control, err := waitForRodControlURL(port, exit, 20*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	process := &manualChromiumProcess{cmd: cmd, exit: exit, port: port, control: control, profile: profile}
	if !headless {
		deadline := time.Now().Add(3 * time.Second)
		for {
			if hideErr := process.SetVisible(false); hideErr == nil {
				log.Printf("myreading Chromium native window hidden before browser attach pid=%d", cmd.Process.Pid)
				break
			}
			if time.Now().After(deadline) {
				process.Close()
				return nil, fmt.Errorf("hide Chromium before attach: window for pid %d not found", cmd.Process.Pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return process, nil
}

func (p *manualChromiumProcess) Close() {
	if p == nil {
		return
	}
	p.close.Do(func() {
		if p.cmd != nil && p.cmd.Process != nil {
			select {
			case <-p.exit:
			case <-time.After(2 * time.Second):
				_ = p.cmd.Process.Kill()
			}
		}
		if err := quiesceBrowserProfile(p.profile); err != nil {
			log.Printf("myreading Chromium process-tree release warning profile=%s err=%v", p.profile, err)
		} else {
			log.Printf("myreading Chromium process tree released profile=%s", p.profile)
		}
	})
}

func (p *manualChromiumProcess) SetVisible(visible bool) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return errBrowserClosed
	}
	pid := uint32(p.cmd.Process.Pid)
	windows := p.topLevelWindows()
	if len(windows) == 0 {
		return fmt.Errorf("Chromium window for pid %d not found", pid)
	}
	p.windowMu.Lock()
	primary := p.window
	foundPrimary := false
	for _, handle := range windows {
		if handle == primary {
			foundPrimary = true
			break
		}
	}
	if primary == 0 || !foundPrimary {
		primary = windows[0]
		p.window = primary
	}
	p.windowMu.Unlock()
	// Initial hiding covers every restored window. When verification is needed,
	// keep any unexpected extras hidden and reveal only the recorded primary HWND.
	for _, handle := range windows {
		if visible && handle == primary {
			win.ShowWindow(handle, win.SW_SHOW)
			win.ShowWindow(handle, win.SW_MAXIMIZE)
			win.SetForegroundWindow(handle)
		} else {
			win.ShowWindow(handle, win.SW_HIDE)
		}
	}
	return nil
}

func (p *manualChromiumProcess) topLevelWindows() []win.HWND {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	pid := uint32(p.cmd.Process.Pid)
	var windows []win.HWND
	var browserWindows []win.HWND
	callback := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		handle := win.HWND(hwnd)
		var windowPID uint32
		win.GetWindowThreadProcessId(handle, &windowPID)
		if windowPID != pid || win.GetParent(handle) != 0 {
			return 1
		}
		windows = append(windows, handle)
		className := make([]uint16, 128)
		if count, _ := win.GetClassName(handle, &className[0], len(className)); count > 0 && syscall.UTF16ToString(className[:count]) == "Chrome_WidgetWin_1" {
			browserWindows = append(browserWindows, handle)
		}
		return 1
	})
	enumWindowsProc.Call(callback, 0)
	if len(browserWindows) > 0 {
		return browserWindows
	}
	return windows
}

func (p *manualChromiumProcess) WindowVisible() bool {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	for _, handle := range p.topLevelWindows() {
		if win.IsWindowVisible(handle) {
			return true
		}
	}
	return false
}

func (p *manualChromiumProcess) VisibleWindowCount() int {
	count := 0
	for _, handle := range p.topLevelWindows() {
		if win.IsWindowVisible(handle) {
			count++
		}
	}
	return count
}

func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func waitForRodControlURL(port int, exit <-chan error, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-exit:
			return "", fmt.Errorf("real Chromium exited before CDP was ready: %v", err)
		default:
		}
		if control, err := launcher.ResolveURL(strconv.Itoa(port)); err == nil {
			return control, nil
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return "", fmt.Errorf("real Chromium CDP timeout: %w", lastErr)
}

func (b *rodMyreadingBrowser) Snapshot(ctx context.Context, target string) (myreadingSnapshot, error) {
	if b.page == nil {
		return myreadingSnapshot{}, errBrowserClosed
	}
	p := b.page.Context(ctx).Timeout(180 * time.Second)
	if err := p.Navigate(target); err != nil {
		return myreadingSnapshot{}, err
	}
	_ = p.WaitLoad()
	first, err := b.currentSnapshot(p)
	if err != nil {
		return myreadingSnapshot{}, err
	}
	if first.Verification || isMyreadingVerificationSnapshot(first) {
		return first, nil
	}
	_, _ = p.Eval(myreadingLoadImagesJS)
	return b.currentSnapshot(p)
}

func (b *rodMyreadingBrowser) CurrentSnapshot(ctx context.Context) (myreadingSnapshot, error) {
	if b.page == nil {
		return myreadingSnapshot{}, errBrowserClosed
	}
	return b.currentSnapshot(b.page.Context(ctx).Timeout(90 * time.Second))
}

func (b *rodMyreadingBrowser) Resource(ctx context.Context, rawURL string, transferred func(int64)) ([]byte, string, error) {
	if b.page == nil {
		return nil, "", errBrowserClosed
	}
	p := b.page.Context(ctx).Timeout(180 * time.Second)
	// Page.getResourceContent only works while Chromium still retains the body.
	// Reload this exact IMG request immediately before reading it, just like the
	// original incremental reader-page/network-cache downloader.
	_ = proto.NetworkSetCacheDisabled{CacheDisabled: true}.Call(p)
	var requestID proto.NetworkRequestID
	eventsDone := make(chan struct{})
	eventPage, stopEvents := p.WithCancel()
	waitEvents := eventPage.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			if e.Request != nil && e.Request.URL == rawURL {
				requestID = e.RequestID
			}
		},
		func(e *proto.NetworkDataReceived) {
			if requestID != "" && e.RequestID == requestID && e.EncodedDataLength > 0 && transferred != nil {
				transferred(int64(e.EncodedDataLength))
			}
		},
		func(e *proto.NetworkLoadingFinished) bool {
			return requestID != "" && e.RequestID == requestID
		},
		func(e *proto.NetworkLoadingFailed) bool {
			return requestID != "" && e.RequestID == requestID
		},
	)
	go func() {
		waitEvents()
		close(eventsDone)
	}()
	defer func() {
		stopEvents()
		<-eventsDone
	}()
	loaded, err := p.Eval(`async (url) => {
		const absolute = value => { try { return new URL(value || '', location.href).href; } catch (_) { return ''; } };
		const attrs = el => ['data-src','data-lazy-src','data-original','src'].map(name => absolute(el.getAttribute(name)));
		const image = Array.from(document.images).find(el => absolute(el.currentSrc) === url || attrs(el).includes(url));
		if (!image) return {ok:false, error:'reader image element not found'};
		image.loading = 'eager'; image.decoding = 'sync';
		image.scrollIntoView({block:'center', inline:'nearest'});
		// requestAnimationFrame is deliberately suspended for a hidden native
		// window. A timer keeps this path operational without exposing the window.
		await new Promise(resolve => setTimeout(resolve, 50));
		image.removeAttribute('srcset'); image.removeAttribute('data-srcset'); image.removeAttribute('data-lazy-srcset');
		image.src = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==';
		await new Promise(resolve => setTimeout(resolve, 30));
		const result = await new Promise(resolve => {
			const timer = setTimeout(() => resolve({ok:false, error:'reader image load timeout'}), 30000);
			image.onload = () => { clearTimeout(timer); resolve({ok:image.naturalWidth>1, error:image.naturalWidth>1?'':'empty reader image'}); };
			image.onerror = () => { clearTimeout(timer); resolve({ok:false, error:'reader image load failed'}); };
			image.src = url;
		});
		return result;
	}`, rawURL)
	if err != nil {
		return nil, "", err
	}
	select {
	case <-eventsDone:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case <-time.After(2 * time.Second):
	}
	if loaded == nil || !loaded.Value.Get("ok").Bool() {
		message := "reader image load failed"
		if loaded != nil {
			message = loaded.Value.Get("error").Str()
		}
		return nil, "", fmt.Errorf("%s", message)
	}
	payload, err := p.GetResource(rawURL)
	if err != nil {
		return nil, "", err
	}
	return payload, "", nil
}

func (b *rodMyreadingBrowser) SetVisible(visible bool) error {
	return b.process.SetVisible(visible)
}

func (b *rodMyreadingBrowser) currentSnapshot(p *rod.Page) (myreadingSnapshot, error) {
	obj, err := p.Eval(myreadingDOMSnapshotJS)
	if err != nil {
		return myreadingSnapshot{}, err
	}
	var snap myreadingSnapshot
	if err = obj.Value.Unmarshal(&snap); err != nil {
		return snap, fmt.Errorf("decode DOM snapshot: %w", err)
	}
	return snap, nil
}

func (b *rodMyreadingBrowser) Cookies(ctx context.Context) ([]browserCookie, error) {
	cs, err := b.browser.Context(ctx).GetCookies()
	if err != nil {
		return nil, err
	}
	out := make([]browserCookie, 0, len(cs))
	for _, c := range cs {
		out = append(out, browserCookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure})
	}
	return out, nil
}
func (b *rodMyreadingBrowser) Close() {
	if b == nil {
		return
	}
	b.close.Do(func() {
		if b.page != nil {
			_ = b.page.Close()
		}
		if b.browser != nil {
			_ = b.browser.Close()
		}
		if b.process != nil {
			b.process.Close()
		}
	})
}
