package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	playwright "github.com/mxschmitt/playwright-go"
)

type playwrightMyreadingBrowser struct {
	pw      *playwright.Playwright
	browser playwright.Browser
	ctx     playwright.BrowserContext
	page    playwright.Page
	process *manualChromiumProcess
}

func newPlaywrightMyreadingBrowser(bin, profile string, headless bool, proxy string) (myreadingBrowser, error) {
	pw, err := playwright.Run()
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
	return &playwrightMyreadingBrowser{pw: pw, browser: browser, ctx: ctx, page: page, process: process}, nil
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

func (b *playwrightMyreadingBrowser) Resource(ctx context.Context, rawURL string) ([]byte, string, error) {
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	default:
	}
	value, err := b.page.Evaluate(`async (url) => {
		const response = await fetch(url, {credentials:'include', cache:'force-cache', redirect:'follow'});
		if (!response.ok) return {ok:false, error:'HTTP '+response.status};
		const bytes = new Uint8Array(await response.arrayBuffer());
		let binary = ''; const chunk = 0x8000;
		for (let i=0; i<bytes.length; i+=chunk) binary += String.fromCharCode.apply(null, bytes.subarray(i,i+chunk));
		return {ok:true, contentType:response.headers.get('content-type')||'', base64:btoa(binary)};
	}`, rawURL)
	if err != nil {
		return nil, "", err
	}
	encoded, _ := json.Marshal(value)
	var result struct {
		OK          bool   `json:"ok"`
		Error       string `json:"error"`
		ContentType string `json:"contentType"`
		Base64      string `json:"base64"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, "", err
	}
	if !result.OK {
		return nil, "", fmt.Errorf("browser fetch: %s", result.Error)
	}
	payload, err := base64.StdEncoding.DecodeString(result.Base64)
	return payload, result.ContentType, err
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
	if b.browser != nil {
		_ = b.browser.Close()
	}
	if b.pw != nil {
		_ = b.pw.Stop()
	}
	b.process.Close()
}
