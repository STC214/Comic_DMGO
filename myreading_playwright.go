package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	playwright "github.com/mxschmitt/playwright-go"
)

type playwrightMyreadingBrowser struct {
	pw      *playwright.Playwright
	browser playwright.Browser
	ctx     playwright.BrowserContext
	page    playwright.Page
	cdp     playwright.CDPSession
	process *manualChromiumProcess
	close   sync.Once
}

func newPlaywrightMyreadingBrowser(bin, profile string, headless bool, proxy string) (myreadingBrowser, error) {
	runOptions := []*playwright.RunOptions(nil)
	if driver := resourcePath("runtime", "playwright", "driver"); playwrightDriverReady(driver) {
		runOptions = append(runOptions, &playwright.RunOptions{DriverDirectory: driver})
	}
	pw, err := playwright.Run(runOptions...)
	if err != nil {
		return nil, fmt.Errorf("driver missing (run setup-playwright.ps1): %w", err)
	}
	process, err := startManualChromium(bin, profile, headless, proxy)
	if err != nil {
		pw.Stop()
		return nil, err
	}
	browser, err := pw.Chromium.ConnectOverCDP(fmt.Sprintf("http://127.0.0.1:%d", process.port), playwright.BrowserTypeConnectOverCDPOptions{NoDefaults: playwright.Bool(true), IsLocal: playwright.Bool(true)})
	if err != nil {
		process.Close()
		pw.Stop()
		return nil, err
	}
	contexts := browser.Contexts()
	if len(contexts) == 0 {
		_ = browser.Close()
		process.Close()
		pw.Stop()
		return nil, fmt.Errorf("manual Chromium opened without a browser context")
	}
	ctx := contexts[0]
	pages := ctx.Pages()
	var page playwright.Page
	if len(pages) > 0 {
		page = pages[len(pages)-1]
	} else {
		_ = browser.Close()
		process.Close()
		pw.Stop()
		return nil, fmt.Errorf("manual Chromium opened without a page")
	}
	cdp, err := ctx.NewCDPSession(page)
	if err != nil {
		_ = browser.Close()
		process.Close()
		_ = pw.Stop()
		return nil, fmt.Errorf("attach page CDP session: %w", err)
	}
	return &playwrightMyreadingBrowser{pw: pw, browser: browser, ctx: ctx, page: page, cdp: cdp, process: process}, nil
}

func playwrightDriverReady(driver string) bool {
	for _, required := range []string{"node.exe", filepath.Join("package", "cli.js")} {
		if info, err := os.Stat(filepath.Join(driver, required)); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}
func (b *playwrightMyreadingBrowser) Snapshot(ctx context.Context, target string) (myreadingSnapshot, error) {
	select {
	case <-ctx.Done():
		return myreadingSnapshot{}, ctx.Err()
	default:
	}
	if b.page == nil {
		return myreadingSnapshot{}, errBrowserClosed
	}
	if _, err := b.page.Goto(target, playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded, Timeout: playwright.Float(90000)}); err != nil {
		return myreadingSnapshot{}, err
	}
	first, err := b.CurrentSnapshot(ctx)
	if err != nil {
		return myreadingSnapshot{}, err
	}
	if first.Verification || isMyreadingVerificationSnapshot(first) {
		return first, nil
	}
	_, _ = b.page.Evaluate(myreadingLoadImagesJS)
	return b.CurrentSnapshot(ctx)
}
func (b *playwrightMyreadingBrowser) CurrentSnapshot(ctx context.Context) (myreadingSnapshot, error) {
	select {
	case <-ctx.Done():
		return myreadingSnapshot{}, ctx.Err()
	default:
	}
	if b.page == nil {
		return myreadingSnapshot{}, errBrowserClosed
	}
	v, err := b.page.Evaluate(myreadingDOMSnapshotJS)
	if err != nil {
		return myreadingSnapshot{}, err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return myreadingSnapshot{}, err
	}
	var snap myreadingSnapshot
	if err = json.Unmarshal(data, &snap); err != nil {
		return snap, err
	}
	return snap, nil
}

func (b *playwrightMyreadingBrowser) Resource(ctx context.Context, rawURL string, transferred func(int64)) ([]byte, string, error) {
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	default:
	}
	if b.page == nil {
		return nil, "", errBrowserClosed
	}
	if b.cdp != nil {
		if _, err := b.cdp.Send("Network.enable", map[string]any{}); err != nil {
			return nil, "", err
		}
		if _, err := b.cdp.Send("Network.setCacheDisabled", map[string]any{"cacheDisabled": true}); err != nil {
			return nil, "", err
		}
	}
	var requestID string
	handler := func(event map[string]any) {
		method, _ := event["method"].(string)
		params, _ := event["params"].(map[string]any)
		switch method {
		case "Network.requestWillBeSent":
			request, _ := params["request"].(map[string]any)
			if requestURL, _ := request["url"].(string); requestURL == rawURL {
				requestID, _ = params["requestId"].(string)
			}
		case "Network.dataReceived":
			id, _ := params["requestId"].(string)
			if id == requestID && id != "" && transferred != nil {
				if n, ok := params["encodedDataLength"].(float64); ok && n > 0 {
					transferred(int64(n))
				}
			}
		}
	}
	if b.cdp != nil {
		b.cdp.On("event", handler)
		defer b.cdp.RemoveListener("event", handler)
	}
	var value any
	response, err := b.page.ExpectResponse(func(responseURL string) bool {
		return responseURL == rawURL
	}, func() error {
		var evaluateErr error
		value, evaluateErr = b.page.Evaluate(`async (url) => {
		const absolute = value => { try { return new URL(value || '', location.href).href; } catch (_) { return ''; } };
		const attrs = el => ['data-src','data-lazy-src','data-original','src'].map(name => absolute(el.getAttribute(name)));
		const image = Array.from(document.images).find(el => absolute(el.currentSrc) === url || attrs(el).includes(url));
		if (!image) return {ok:false, error:'reader image element not found'};
		image.loading='eager'; image.scrollIntoView({block:'center', inline:'nearest'});
		image.removeAttribute('srcset'); image.removeAttribute('data-srcset'); image.removeAttribute('data-lazy-srcset');
		image.src='data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==';
		await new Promise(resolve => setTimeout(resolve, 30));
		const loaded = await new Promise(resolve => { const timer=setTimeout(()=>resolve(false),30000); image.onload=()=>{clearTimeout(timer);resolve(image.naturalWidth>1)}; image.onerror=()=>{clearTimeout(timer);resolve(false)}; image.src=url; });
		return loaded ? {ok:true} : {ok:false, error:'reader image load failed'};
	}`, rawURL)
		return evaluateErr
	}, playwright.PageExpectResponseOptions{Timeout: playwright.Float(45_000)})
	if err != nil {
		return nil, "", err
	}
	encoded, _ := json.Marshal(value)
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, "", err
	}
	if !result.OK {
		return nil, "", fmt.Errorf("browser fetch: %s", result.Error)
	}
	payload, err := response.Body()
	return payload, response.Headers()["content-type"], err
}
func (b *playwrightMyreadingBrowser) SetVisible(visible bool) error {
	return b.process.SetVisible(visible)
}
func (b *playwrightMyreadingBrowser) Cookies(ctx context.Context) ([]browserCookie, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	cs, err := b.ctx.Cookies()
	if err != nil {
		return nil, err
	}
	out := make([]browserCookie, 0, len(cs))
	for _, c := range cs {
		out = append(out, browserCookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure})
	}
	return out, nil
}
func (b *playwrightMyreadingBrowser) Close() {
	if b == nil {
		return
	}
	b.close.Do(func() {
		if b.cdp != nil {
			_ = b.cdp.Detach()
		}
		if b.browser != nil {
			_ = b.browser.Close()
		}
		if b.pw != nil {
			_ = b.pw.Stop()
		}
		if b.process != nil {
			b.process.Close()
		}
	})
}
