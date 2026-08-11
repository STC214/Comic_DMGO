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
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/stealth"
)

type rodMyreadingBrowser struct {
	browser *rod.Browser
	page    *rod.Page
	process *manualChromiumProcess
}

type manualChromiumProcess struct {
	cmd     *exec.Cmd
	exit    <-chan error
	port    int
	control string
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
	}
	if headless {
		args = append(args, "--headless=new")
	} else {
		// Use the native maximized window and its real content viewport. This is
		// the same display mode as manually maximizing the standalone browser and
		// avoids a stale copied-profile window rectangle clipping the challenge.
		args = append(args, "--start-maximized")
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
	return &manualChromiumProcess{cmd: cmd, exit: exit, port: port, control: control}, nil
}

func (p *manualChromiumProcess) Close() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	select {
	case <-p.exit:
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
	}
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
	p := b.page.Context(ctx).Timeout(90 * time.Second)
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

func (b *rodMyreadingBrowser) Resource(ctx context.Context, rawURL string) ([]byte, string, error) {
	if b.page == nil {
		return nil, "", errBrowserClosed
	}
	payload, err := b.page.Context(ctx).Timeout(90 * time.Second).GetResource(rawURL)
	if err != nil {
		return nil, "", err
	}
	return payload, "", nil
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
	if b.page != nil {
		_ = b.page.Close()
	}
	if b.browser != nil {
		_ = b.browser.Close()
	}
	b.process.Close()
}
