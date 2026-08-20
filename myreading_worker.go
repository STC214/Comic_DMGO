package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const myreadingDOMSnapshotJS = `() => {
	const srcsetURL = (value) => {
	  const item = (value || '').split(',').map(x => x.trim()).filter(Boolean).pop() || '';
	  return item.split(/\s+/)[0] || '';
	};
  const pick = (el) => el.getAttribute('data-src') || el.getAttribute('data-lazy-src') ||
	  el.getAttribute('data-original') || srcsetURL(el.getAttribute('data-srcset')) ||
	  srcsetURL(el.getAttribute('data-lazy-srcset')) || el.currentSrc || srcsetURL(el.getAttribute('srcset')) || el.src || '';
  const root = document.querySelector('article, main, .entry-content, .post-content') || document.body;
  const images = [...root.querySelectorAll('img')].map(pick).filter(Boolean).map(x => new URL(x, location.href).href);
  const nextEl = document.querySelector('a[rel="next"], a.next, .post-page-numbers.next, .pagination a.next');
  const text = (document.body && document.body.innerText || '').toLowerCase();
  const pageTitle = (document.title || '').trim();
  const challenge = !!document.querySelector(
    'iframe[src*="challenge"], input[name="cf-turnstile-response"], .cf-turnstile, #challenge-running, #challenge-stage, form#challenge-form, script[src*="/cdn-cgi/challenge-platform/"]'
  ) || /^just a moment(?:\.{3})?$/i.test(pageTitle) ||
    /attention required.*cloudflare/i.test(pageTitle) ||
    text.includes('verification required') || text.includes('verify you are human') ||
    text.includes('checking your browser') || text.includes('performing security verification') ||
    text.includes('enable javascript and cookies to continue');
  return {
    url: location.href,
    title: (document.querySelector('h1.entry-title, article h1, main h1, h1') || {}).textContent || document.title || '',
    images,
    next: nextEl ? new URL(nextEl.href, location.href).href : '',
    verification: challenge
  };
}`

const myreadingLoadImagesJS = `async () => {
	let stable = 0, previousHeight = 0, previousCount = 0;
	for (let step = 0; step < 80 && stable < 3; step++) {
	  const height = Math.max(document.body.scrollHeight, document.documentElement.scrollHeight);
	  const count = document.querySelectorAll('article img, main img, .entry-content img, .post-content img').length;
	  window.scrollBy(0, Math.max(320, Math.floor(window.innerHeight * 0.75)));
	  await new Promise(resolve => setTimeout(resolve, 180));
	  const atBottom = window.scrollY + window.innerHeight >= height - 8;
	  if (atBottom && height === previousHeight && count === previousCount) stable++; else stable = 0;
	  previousHeight = height; previousCount = count;
	}
	return {height: document.body.scrollHeight, count: document.images.length};
}`

type myreadingSnapshot struct {
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	Images       []string `json:"images"`
	Next         string   `json:"next"`
	Verification bool     `json:"verification"`
}

type browserCookie struct {
	Name, Value, Domain, Path string
	Secure                    bool
}

type myreadingBrowser interface {
	Snapshot(ctx context.Context, target string) (myreadingSnapshot, error)
	CurrentSnapshot(ctx context.Context) (myreadingSnapshot, error)
	Resource(ctx context.Context, rawURL string, transferred func(int64)) ([]byte, string, error)
	SetVisible(visible bool) error
	Cookies(ctx context.Context) ([]browserCookie, error)
	Close()
}

type goMyreadingControl struct {
	once    sync.Once
	cancel  context.CancelFunc
	mu      sync.Mutex
	browser myreadingBrowser
}

func (c *goMyreadingControl) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		c.ReleaseBrowser()
	})
}

func (c *goMyreadingControl) ReleaseBrowser() {
	c.mu.Lock()
	browser := c.browser
	c.browser = nil
	c.mu.Unlock()
	if browser != nil {
		browser.Close()
	}
}

func (c *goMyreadingControl) SetBrowser(browser myreadingBrowser) {
	c.mu.Lock()
	c.browser = browser
	c.mu.Unlock()
}

type myreadingConfig struct {
	Engine       string `json:"engine"`
	Proxy        string `json:"proxy"`
	MaxPages     int    `json:"maxPages"`
	Retries      int    `json:"retries"`
	RequestDelay int    `json:"requestDelayMs"`
	PageTimeout  int    `json:"pageTimeoutSeconds"`
}

func loadMyreadingConfig() myreadingConfig {
	cfg := myreadingConfig{Engine: "rod", MaxPages: 100, Retries: 3, RequestDelay: 250, PageTimeout: 90}
	if data, err := os.ReadFile(resourcePath("config.json")); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if v := strings.TrimSpace(os.Getenv("MYREADING_ENGINE")); v != "" {
		cfg.Engine = v
	}
	cfg.Engine = strings.ToLower(strings.TrimSpace(cfg.Engine))
	if cfg.Engine != "playwright" {
		cfg.Engine = "rod"
	}
	if cfg.MaxPages < 1 {
		cfg.MaxPages = 100
	}
	if cfg.Retries < 1 {
		cfg.Retries = 3
	}
	if cfg.PageTimeout < 10 {
		cfg.PageTimeout = 90
	}
	return cfg
}

func (m *Manager) runGoMyreadingTask(id int, task Task, adapter siteAdapter) {
	if !m.setState(id, TaskRunning, .03, "starting Go browser engine") {
		return
	}
	cfg := loadMyreadingConfig()
	browserPath := currentChromiumPath()
	if strings.TrimSpace(browserPath) == "" {
		m.setState(id, TaskError, .03, "chromium executable not configured")
		return
	}
	// Match the established desktop flow: seed every task profile from the real
	// Chromium user data (or the verified fallback) instead of starting empty.
	profile, profileErr := ensureTaskBrowserProfile(id, adapter.name, cfg.Engine, browserPath, "", false)
	if profileErr != nil {
		m.setState(id, TaskError, .03, "profile: "+profileErr.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.PageTimeout*cfg.MaxPages)*time.Second)
	defer cancel()
	// The source desktop implementation succeeds by opening an ordinary visible
	// Chromium window first. Keep the UI's historical headless field for task
	// compatibility, but run myreading in the real headed browser from launch.
	browserHeadless := false
	log.Printf("myreading real-browser mode id=%d requestedHeadless=%v effectiveHeadless=%v browser=%s", id, task.Headless, browserHeadless, browserPath)
	browser, err := createMyreadingBrowser(cfg.Engine, browserPath, profile, browserHeadless, cfg.Proxy)
	if err != nil {
		if cleanupErr := cleanupTaskBrowserProfiles(id, adapter.name); cleanupErr != nil {
			log.Printf("myreading failed-start resource cleanup warning id=%d err=%v", id, cleanupErr)
		}
		m.setState(id, TaskError, .04, cfg.Engine+" start: "+err.Error())
		return
	}
	// Keep the real headed Chrome process (and therefore its verified rendering
	// behavior), but hide its native window until a challenge actually needs the
	// user's input. Hiding does not change the page viewport or browser mode.
	if hideErr := browser.SetVisible(false); hideErr != nil {
		log.Printf("myreading initial browser hide warning id=%d err=%v", id, hideErr)
	}
	control := &goMyreadingControl{cancel: cancel, browser: browser}
	m.registerActiveWorker(id, control)
	defer m.unregisterActiveWorker(id, control)
	defer func() {
		control.Close()
		if cleanupErr := cleanupTaskBrowserProfiles(id, adapter.name); cleanupErr != nil {
			log.Printf("myreading deferred task resource cleanup warning id=%d err=%v", id, cleanupErr)
		} else {
			log.Printf("myreading task resources released id=%d profile=%s", id, profile)
		}
	}()

	m.setState(id, TaskRunning, .08, "collecting reader pages with "+cfg.Engine)
	seenPages, seenImages := map[string]bool{}, map[string]bool{}
	var images []string
	current, title := task.URL, strings.TrimSpace(task.Title)
	if isMyreadingChallengeTitle(title) {
		title = ""
	}
	verificationCompleted := false
	for pageNo := 1; current != "" && pageNo <= cfg.MaxPages; pageNo++ {
		if m.isStopped(id) {
			return
		}
		key := canonicalTaskURL(current)
		if seenPages[key] {
			break
		}
		seenPages[key] = true
		snap, snapErr := browser.Snapshot(ctx, current)
		if snapErr != nil {
			m.setState(id, TaskError, .1, "page collection: "+snapErr.Error())
			return
		}
		snap.Verification = snap.Verification || isMyreadingVerificationSnapshot(snap)
		if snap.Verification {
			if showErr := browser.SetVisible(true); showErr != nil {
				log.Printf("myreading verification browser show warning id=%d err=%v", id, showErr)
			}
			m.setState(id, TaskWaitingVerification, .1, "complete verification in the visible browser")
			if browserHeadless {
				control.ReleaseBrowser()
				if err := quiesceBrowserProfile(profile); err != nil {
					m.setStateIfCurrent(id, TaskWaitingVerification, TaskError, .1, "profile release: "+err.Error())
					return
				}
				browser, err = createMyreadingBrowser(cfg.Engine, browserPath, profile, false, cfg.Proxy)
				if err != nil {
					m.setStateIfCurrent(id, TaskWaitingVerification, TaskError, .1, "visible browser: "+err.Error())
					return
				}
				control.SetBrowser(browser)
				if snap, snapErr = browser.Snapshot(ctx, current); snapErr != nil {
					if m.isStopped(id) {
						return
					}
					m.setStateIfCurrent(id, TaskWaitingVerification, TaskError, .1, "visible browser: "+snapErr.Error())
					return
				}
				snap.Verification = snap.Verification || isMyreadingVerificationSnapshot(snap)
			}
			deadline := time.Now().Add(10 * time.Minute)
			for snap.Verification && time.Now().Before(deadline) {
				if m.isStopped(id) {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				snap, snapErr = browser.CurrentSnapshot(ctx)
				if snapErr != nil {
					if m.isStopped(id) {
						return
					}
					m.setStateIfCurrent(id, TaskWaitingVerification, TaskError, .1, "verification browser: "+snapErr.Error())
					return
				}
				snap.Verification = snap.Verification || isMyreadingVerificationSnapshot(snap)
			}
			if snap.Verification {
				m.setStateIfCurrent(id, TaskWaitingVerification, TaskError, .1, "verification timed out")
				return
			}
			m.setStateIfCurrent(id, TaskWaitingVerification, TaskRunning, .12, "verification complete; collecting reader")
			if hideErr := browser.SetVisible(false); hideErr != nil {
				log.Printf("myreading verified browser hide warning id=%d err=%v", id, hideErr)
			}
			verificationCompleted = true
			snap, snapErr = browser.Snapshot(ctx, current)
			if snapErr != nil {
				m.setState(id, TaskError, .12, "reader after verification: "+snapErr.Error())
				return
			}
			snap.Verification = snap.Verification || isMyreadingVerificationSnapshot(snap)
			if snap.Verification {
				m.setState(id, TaskWaitingVerification, .12, "verification resumed; complete it in the visible browser")
				return
			}
		}
		if title == "" {
			title = strings.TrimSpace(snap.Title)
			if title != "" {
				m.setTitle(id, title)
			}
		}
		for _, raw := range snap.Images {
			if u := normalizeImageURL(raw); u != "" && isProbablyMyreadingContentImage(u) && !seenImages[u] {
				seenImages[u] = true
				images = append(images, u)
			}
		}
		m.setState(id, TaskRunning, minFloat(.45, .10+float64(pageNo)*.03), fmt.Sprintf("collected page %d, images %d", pageNo, len(images)))
		next := strings.TrimSpace(snap.Next)
		if next != "" && !sameComicPage(task.URL, next) {
			log.Printf("myreading ignored out-of-scope next page id=%d current=%s next=%s", id, current, next)
			next = ""
		}
		current = next
		if cfg.RequestDelay > 0 {
			time.Sleep(time.Duration(cfg.RequestDelay) * time.Millisecond)
		}
	}
	if len(images) == 0 {
		m.setState(id, TaskError, .15, "no reader images found")
		return
	}
	if title == "" {
		title = "myreading-" + time.Now().Format("20060102-150405")
	}
	root := strings.TrimSpace(task.DownloadRoot)
	if root == "" {
		root = adapter.outputDir()
	}
	out := filepath.Join(root, sanitizePathComponent(title))
	if err = os.MkdirAll(out, 0o755); err != nil {
		m.setState(id, TaskError, .45, "output: "+err.Error())
		return
	}
	m.setOutputDir(id, out)
	cookies, _ := browser.Cookies(ctx)
	speed := newDownloadSpeedTracker(time.Now())
	var liveTransferred atomic.Int64
	speedStop := make(chan struct{})
	speedStopped := make(chan struct{})
	go func() {
		defer close(speedStopped)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-speedStop:
				return
			case now := <-ticker.C:
				if !m.isStopped(id) {
					if text := speed.Update(now, liveTransferred.Load()); text != "" {
						m.setTaskSpeed(id, text)
					}
				}
			}
		}
	}()
	m.setTaskSpeed(id, "实时 计算中")
	m.setState(id, TaskRunning, .45, fmt.Sprintf("downloading images 0/%d", len(images)))
	downloaded, totalBytes, err := downloadMyreadingImages(ctx, browser, images, task.URL, out, cookies, cfg.Retries, func(done, total int, bytes, transferred int64) {
		if m.isStopped(id) {
			cancel()
			return
		}
		if text := speed.Update(time.Now(), transferred); text != "" {
			m.setTaskSpeed(id, text)
		}
		m.setState(id, TaskRunning, .45+.53*float64(done)/float64(total), fmt.Sprintf("downloading images %d/%d", done, total))
	}, func(transferred int64) {
		liveTransferred.Store(transferred)
		if text := speed.Update(time.Now(), transferred); text != "" {
			m.setTaskSpeed(id, text)
		}
	})
	close(speedStop)
	<-speedStopped
	if err != nil {
		if text := speed.Average(time.Now()); text != "" {
			m.setTaskSpeed(id, text)
		}
		m.setState(id, TaskError, .5, err.Error())
		return
	}
	if text := speed.Average(time.Now()); text != "" {
		m.setTaskSpeed(id, text)
	}
	control.ReleaseBrowser()
	if releaseErr := quiesceBrowserProfile(profile); releaseErr != nil {
		log.Printf("myreading profile release warning id=%d err=%v", id, releaseErr)
	} else if _, updateErr := updateVerifiedBrowserProfile(adapter.name, profile); updateErr != nil {
		log.Printf("myreading profile baseline warning id=%d verified=%v err=%v", id, verificationCompleted, updateErr)
	}
	result := map[string]any{"ok": true, "worker": "go-" + cfg.Engine, "title": title, "display_title": title,
		"output_dir": out, "expected_pages": len(images), "downloaded_pages": downloaded, "bytes": totalBytes}
	adapter.handleResult(m, id, task, result, out)
	log.Printf("myreading Go engine complete id=%d engine=%s images=%d bytes=%d output=%s", id, cfg.Engine, downloaded, totalBytes, out)
}

type downloadSpeedTracker struct {
	mu        sync.Mutex
	startedAt time.Time
	lastAt    time.Time
	lastBytes int64
	bytes     int64
}

func newDownloadSpeedTracker(now time.Time) *downloadSpeedTracker {
	return &downloadSpeedTracker{startedAt: now, lastAt: now}
}

func (s *downloadSpeedTracker) Update(now time.Time, totalBytes int64) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if totalBytes < s.lastBytes {
		return ""
	}
	s.bytes = totalBytes
	elapsed := now.Sub(s.lastAt).Seconds()
	delta := totalBytes - s.lastBytes
	if elapsed < 0.25 {
		return ""
	}
	s.lastAt, s.lastBytes = now, totalBytes
	if delta == 0 {
		return "实时 0 B/s"
	}
	return formatRealtimeByteRate(float64(delta) / elapsed)
}

func (s *downloadSpeedTracker) Average(now time.Time) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bytes <= 0 {
		return ""
	}
	elapsed := now.Sub(s.startedAt).Seconds()
	if elapsed <= 0 {
		return ""
	}
	return formatAverageByteRate(float64(s.bytes) / elapsed)
}

func isProbablyMyreadingContentImage(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	// This reader's comic pages are WebP. Restricting collection here prevents
	// tracking GIFs, logos and ad resources from shifting page numbering.
	return strings.EqualFold(filepath.Ext(u.Path), ".webp")
}

func isMyreadingChallengeTitle(title string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	return title == "just a moment" || title == "just a moment..." ||
		(strings.Contains(title, "attention required") && strings.Contains(title, "cloudflare"))
}

func isMyreadingVerificationSnapshot(snap myreadingSnapshot) bool {
	return len(snap.Images) == 0 && isMyreadingChallengeTitle(snap.Title)
}

func sameComicPage(initial, candidate string) bool {
	base, err := url.Parse(strings.TrimSpace(initial))
	if err != nil {
		return false
	}
	next, err := url.Parse(strings.TrimSpace(candidate))
	if err != nil || !strings.EqualFold(base.Hostname(), next.Hostname()) {
		return false
	}
	basePath := strings.TrimSuffix(filepath.ToSlash(base.EscapedPath()), "/")
	nextPath := strings.TrimSuffix(filepath.ToSlash(next.EscapedPath()), "/")
	if basePath == nextPath {
		return true
	}
	pageSuffix := regexp.MustCompile(`(?i)/(?:page/)?\d+$`)
	root := pageSuffix.ReplaceAllString(basePath, "")
	if root == "" {
		return false
	}
	return nextPath == root || strings.HasPrefix(nextPath, root+"/")
}

func newMyreadingBrowser(engine, bin, profile string, headless bool, proxy string) (myreadingBrowser, error) {
	if engine == "playwright" {
		return newPlaywrightMyreadingBrowser(bin, profile, headless, proxy)
	}
	return newRodMyreadingBrowser(bin, profile, headless, proxy)
}

var createMyreadingBrowser = newMyreadingBrowser

func normalizeImageURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	lower := strings.ToLower(u.Path)
	for _, marker := range []string{"avatar", "logo", "icon", "emoji", "smilies", "gravatar"} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	return u.String()
}

var unsafePathChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)

func sanitizePathComponent(s string) string {
	s = strings.Trim(strings.TrimSpace(unsafePathChars.ReplaceAllString(s, "_")), ". ")
	if len([]rune(s)) > 120 {
		s = string([]rune(s)[:120])
	}
	// Truncation can expose a trailing space or dot even when the original
	// value was already trimmed. Windows removes those characters when it
	// creates the directory, so retaining them here makes subsequent file
	// opens address a different path.
	s = strings.TrimRight(s, ". ")
	if s == "" {
		return "untitled"
	}
	return s
}

func downloadMyreadingImages(ctx context.Context, browser myreadingBrowser, images []string, referer, out string, cookies []browserCookie, retries int, progress func(int, int, int64, int64), transferProgress func(int64)) (int, int64, error) {
	jar, _ := cookiejar.New(nil)
	byHost := map[string][]*http.Cookie{}
	for _, c := range cookies {
		domain := strings.TrimPrefix(c.Domain, ".")
		if domain == "" {
			continue
		}
		scheme := "http"
		if c.Secure {
			scheme = "https"
		}
		u, _ := url.Parse(scheme + "://" + domain)
		byHost[u.String()] = append(byHost[u.String()], &http.Cookie{Name: c.Name, Value: c.Value, Path: c.Path, Domain: c.Domain, Secure: c.Secure})
	}
	for raw, cs := range byHost {
		u, _ := url.Parse(raw)
		jar.SetCookies(u, cs)
	}
	client := &http.Client{Jar: jar, Timeout: 90 * time.Second}
	var totalBytes int64
	var transferredBytes atomic.Int64
	reportTransfer := func(delta int64) {
		if delta <= 0 {
			return
		}
		current := transferredBytes.Add(delta)
		if transferProgress != nil {
			transferProgress(current)
		}
	}
	for i, raw := range images {
		if err := ctx.Err(); err != nil {
			return i, totalBytes, err
		}
		var last error
		// Match the established downloader: a complete existing page is reusable,
		// so a retry resumes at the first missing/corrupt image.
		existing := filepath.Join(out, fmt.Sprintf("%04d%s", i+1, imageExtension(raw, "")))
		if info, statErr := os.Stat(existing); statErr == nil && info.Mode().IsRegular() {
			if validationErr := validateDownloadedImage(existing, ""); validationErr == nil {
				totalBytes += info.Size()
				progress(i+1, len(images), totalBytes, transferredBytes.Load())
				log.Printf("myreading browser resource reused image=%d/%d bytes=%d", i+1, len(images), info.Size())
				continue
			}
			_ = os.Remove(existing)
		}
		if browser != nil {
			var browserTransferred atomic.Int64
			payload, contentType, resourceErr := browser.Resource(ctx, raw, func(delta int64) {
				browserTransferred.Add(delta)
				reportTransfer(delta)
			})
			if resourceErr == nil {
				n, saveErr := saveMyreadingImageBytes(payload, contentType, raw, out, i)
				if saveErr == nil {
					totalBytes += n
					if observed := browserTransferred.Load(); observed < n {
						reportTransfer(n - observed)
					}
					progress(i+1, len(images), totalBytes, transferredBytes.Load())
					continue
				}
				resourceErr = saveErr
			}
			last = resourceErr
			log.Printf("myreading browser resource fallback image=%d/%d url=%s err=%v", i+1, len(images), raw, resourceErr)
		}
		for attempt := 1; attempt <= retries; attempt++ {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
			req.Header.Set("Referer", referer)
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/145 Safari/537.36")
			resp, err := client.Do(req)
			if err != nil {
				last = err
				select {
				case <-ctx.Done():
					return i, totalBytes, ctx.Err()
				case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
				}
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				last = fmt.Errorf("image %d HTTP %d", i+1, resp.StatusCode)
				resp.Body.Close()
				continue
			}
			ext := imageExtension(raw, resp.Header.Get("Content-Type"))
			name := fmt.Sprintf("%04d%s", i+1, ext)
			tmp := filepath.Join(out, name+".part")
			f, ferr := os.Create(tmp)
			if ferr != nil {
				resp.Body.Close()
				return i, totalBytes, ferr
			}
			n, cerr := io.Copy(&transferCountingWriter{writer: f, report: reportTransfer}, resp.Body)
			closeErr := f.Close()
			resp.Body.Close()
			if cerr == nil {
				cerr = closeErr
			}
			if cerr != nil {
				os.Remove(tmp)
				last = cerr
				continue
			}
			if validationErr := validateDownloadedImage(tmp, resp.Header.Get("Content-Type")); validationErr != nil {
				_ = os.Remove(tmp)
				last = fmt.Errorf("image %d validation: %w", i+1, validationErr)
				continue
			}
			if err = os.Rename(tmp, filepath.Join(out, name)); err != nil {
				os.Remove(tmp)
				return i, totalBytes, err
			}
			totalBytes += n
			last = nil
			break
		}
		if last != nil {
			return i, totalBytes, fmt.Errorf("download image %d/%d: %w", i+1, len(images), last)
		}
		progress(i+1, len(images), totalBytes, transferredBytes.Load())
	}
	return len(images), totalBytes, nil
}

type transferCountingWriter struct {
	writer io.Writer
	report func(int64)
}

func (w *transferCountingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 && w.report != nil {
		w.report(int64(n))
	}
	return n, err
}

func saveMyreadingImageBytes(payload []byte, contentType, rawURL, out string, index int) (int64, error) {
	if len(payload) == 0 {
		return 0, fmt.Errorf("empty browser resource")
	}
	if len(payload) > 80*1024*1024 {
		return 0, fmt.Errorf("image response too large: %d bytes", len(payload))
	}
	ext := imageExtension(rawURL, contentType)
	name := fmt.Sprintf("%04d%s", index+1, ext)
	tmp := filepath.Join(out, name+".part")
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return 0, err
	}
	if err := validateDownloadedImage(tmp, contentType); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, filepath.Join(out, name)); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return int64(len(payload)), nil
}

func validateDownloadedImage(path, contentType string) error {
	if mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType)); err == nil {
		if strings.HasPrefix(mediaType, "text/") || strings.Contains(mediaType, "html") || strings.Contains(mediaType, "json") {
			return fmt.Errorf("unexpected content type %s", mediaType)
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	config, format, err := image.DecodeConfig(f)
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}
	if strings.TrimSpace(format) == "" || config.Width < 1 || config.Height < 1 {
		return fmt.Errorf("invalid image dimensions %dx%d", config.Width, config.Height)
	}
	return nil
}

func imageExtension(raw, contentType string) string {
	if u, err := url.Parse(raw); err == nil {
		e := strings.ToLower(filepath.Ext(u.Path))
		if len(e) >= 2 && len(e) <= 6 {
			return e
		}
	}
	if exts, _ := mime.ExtensionsByType(strings.Split(contentType, ";")[0]); len(exts) > 0 {
		sort.Strings(exts)
		return exts[0]
	}
	return ".jpg"
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

var errBrowserClosed = errors.New("browser closed")
