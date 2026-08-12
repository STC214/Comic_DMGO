package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDownloadMyreadingImagesEndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/one.jpg", func(w http.ResponseWriter, r *http.Request) {
		if r.Referer() == "" {
			t.Error("missing referer")
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(testPNGBytes(t))
	})
	mux.HandleFunc("/two.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNGBytes(t))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	out := t.TempDir()
	done, bytes, err := downloadMyreadingImages(context.Background(), nil, []string{srv.URL + "/one.jpg", srv.URL + "/two.png"}, srv.URL+"/reader", out, nil, 2, func(int, int, int64, int64) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done != 2 || bytes <= 0 {
		t.Fatalf("done=%d bytes=%d", done, bytes)
	}
	for _, name := range []string{"0001.jpg", "0002.png"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDownloadMyreadingImagesRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>verification required</html>"))
	}))
	defer srv.Close()
	out := t.TempDir()
	_, _, err := downloadMyreadingImages(context.Background(), nil, []string{srv.URL + "/page.jpg"}, srv.URL, out, nil, 1, func(int, int, int64, int64) {}, nil)
	if err == nil {
		t.Fatal("expected HTML response to be rejected")
	}
	if matches, _ := filepath.Glob(filepath.Join(out, "0001*")); len(matches) != 0 {
		t.Fatalf("unexpected output files: %v", matches)
	}
}

func TestDownloadResumeDoesNotCountReusedBytesAsNetworkSpeed(t *testing.T) {
	payload := testPNGBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	out := t.TempDir()
	url := srv.URL + "/page.png"
	if _, _, err := downloadMyreadingImages(context.Background(), nil, []string{url}, srv.URL, out, nil, 1, func(int, int, int64, int64) {}, nil); err != nil {
		t.Fatal(err)
	}
	var transferred int64 = -1
	if _, _, err := downloadMyreadingImages(context.Background(), nil, []string{url}, srv.URL, out, nil, 1, func(_, _ int, _ int64, networkBytes int64) {
		transferred = networkBytes
	}, nil); err != nil {
		t.Fatal(err)
	}
	if transferred != 0 {
		t.Fatalf("reused file counted as transferred bytes: %d", transferred)
	}
}

func TestDownloadMyreadingImagesHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := downloadMyreadingImages(ctx, nil, []string{srv.URL + "/slow.jpg"}, srv.URL, t.TempDir(), nil, 2, func(int, int, int64, int64) {}, nil)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestDownloadMyreadingImagesReportsBytesBeforeImageCompletes(t *testing.T) {
	payload := testPNGBytes(t)
	firstChunkSent := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		half := len(payload) / 2
		_, _ = w.Write(payload[:half])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(firstChunkSent)
		<-release
		_, _ = w.Write(payload[half:])
	}))
	defer srv.Close()

	transferSeen := make(chan int64, 1)
	done := make(chan error, 1)
	go func() {
		_, _, err := downloadMyreadingImages(context.Background(), nil, []string{srv.URL + "/slow.png"}, srv.URL, t.TempDir(), nil, 1,
			func(int, int, int64, int64) {},
			func(bytes int64) {
				select {
				case transferSeen <- bytes:
				default:
				}
			})
		done <- err
	}()
	<-firstChunkSent
	select {
	case bytes := <-transferSeen:
		if bytes <= 0 {
			t.Fatalf("reported bytes=%d", bytes)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("no transfer update before the image completed")
	}
	select {
	case err := <-done:
		t.Fatalf("download completed before second chunk: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPlaywrightDriverReadyRequiresPortableNodeAndCLI(t *testing.T) {
	driver := t.TempDir()
	if playwrightDriverReady(driver) {
		t.Fatal("empty driver directory reported ready")
	}
	if err := os.WriteFile(filepath.Join(driver, "node.exe"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(driver, "package"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(driver, "package", "cli.js"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !playwrightDriverReady(driver) {
		t.Fatal("complete portable driver reported unavailable")
	}
}

func TestSameComicPage(t *testing.T) {
	base := "https://myreadingmanga.info/work-title/"
	for _, candidate := range []string{
		"https://myreadingmanga.info/work-title/page/2/",
		"https://myreadingmanga.info/work-title/2/",
		"https://myreadingmanga.info/work-title/?page=2",
	} {
		if !sameComicPage(base, candidate) {
			t.Errorf("expected same comic: %s", candidate)
		}
	}
	for _, candidate := range []string{
		"https://myreadingmanga.info/another-work/",
		"https://example.test/work-title/page/2/",
	} {
		if sameComicPage(base, candidate) {
			t.Errorf("expected out of scope: %s", candidate)
		}
	}
}

func TestMyreadingVerificationSnapshotDetection(t *testing.T) {
	for _, title := range []string{"Just a moment...", "just a moment", "Attention Required! | Cloudflare"} {
		if !isMyreadingVerificationSnapshot(myreadingSnapshot{Title: title}) {
			t.Errorf("expected verification title: %q", title)
		}
	}
	if isMyreadingVerificationSnapshot(myreadingSnapshot{Title: "Fixture Comic"}) {
		t.Fatal("reader title was classified as verification")
	}
	if isMyreadingVerificationSnapshot(myreadingSnapshot{Title: "Just a moment...", Images: []string{"page.jpg"}}) {
		t.Fatal("snapshot with reader images was classified as verification")
	}
}

func TestFindProjectRootUpwardFromBin(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin", "nested")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findProjectRootUpward(bin); filepath.Clean(got) != filepath.Clean(root) {
		t.Fatalf("root=%q want=%q", got, root)
	}
}

func TestSourceProjectChromiumIsPreferred(t *testing.T) {
	preferred := sourceProjectChromiumPath()
	if preferred == "" {
		t.Skip("source project Chromium is not installed")
	}
	old := currentChromiumPath()
	t.Cleanup(func() { setChromiumPath(old) })
	setChromiumPath(`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`)
	if got := currentChromiumPath(); !strings.EqualFold(filepath.Clean(got), filepath.Clean(preferred)) {
		t.Fatalf("active browser=%q want source Chromium=%q", got, preferred)
	}
}

type fakeMyreadingBrowser struct {
	mu           sync.Mutex
	verification bool
	imageURL     string
	closed       bool
}

func (b *fakeMyreadingBrowser) SetVisible(bool) error { return nil }

func (b *fakeMyreadingBrowser) Snapshot(context.Context, string) (myreadingSnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return myreadingSnapshot{Title: "Fixture Verified", Images: func() []string {
		if b.verification {
			return nil
		}
		return []string{b.imageURL}
	}(), Verification: b.verification}, nil
}
func (b *fakeMyreadingBrowser) CurrentSnapshot(context.Context) (myreadingSnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.verification = false
	return myreadingSnapshot{Title: "Fixture Verified", Images: []string{b.imageURL}}, nil
}
func (b *fakeMyreadingBrowser) Resource(ctx context.Context, rawURL string, transferred func(int64)) ([]byte, string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err == nil && transferred != nil {
		transferred(int64(len(payload)))
	}
	return payload, resp.Header.Get("Content-Type"), err
}
func (b *fakeMyreadingBrowser) Cookies(context.Context) ([]browserCookie, error) { return nil, nil }
func (b *fakeMyreadingBrowser) Close()                                           { b.mu.Lock(); b.closed = true; b.mu.Unlock() }

func TestGoMyreadingVerificationToDownloadFlow(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNGBytes(t))
	}))
	defer srv.Close()
	oldFactory := createMyreadingBrowser
	oldBrowserPath := currentChromiumPath()
	t.Cleanup(func() { createMyreadingBrowser = oldFactory; setChromiumPath(oldBrowserPath) })
	var launches []bool
	createMyreadingBrowser = func(engine, bin, profile string, headless bool, proxy string) (myreadingBrowser, error) {
		launches = append(launches, headless)
		// Both the initial headless browser and the newly opened visible browser
		// start on the challenge. CurrentSnapshot simulates the user completing it.
		return &fakeMyreadingBrowser{verification: true, imageURL: srv.URL + "/page.png"}, nil
	}
	fakeBin := filepath.Join(t.TempDir(), "chromium.exe")
	if err := os.WriteFile(fakeBin, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	setChromiumPath(fakeBin)
	m := NewManager()
	task := m.AddTask("https://myreadingmanga.info/fixture-work/", "", t.TempDir(), true, false)
	workerName := fmt.Sprintf("myreading-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Dir(verifiedBrowserProfileDir(workerName)))
		_ = os.RemoveAll(filepath.Join(runtimeRootDir(), "thumb", workerName))
	})
	m.runGoMyreadingTask(task.ID, *task, siteAdapter{name: workerName, daemonPrefix: workerName})
	got, ok := m.taskCopy(task.ID)
	if !ok || got.State != TaskDone {
		t.Fatalf("task=%+v", got)
	}
	if len(launches) != 1 || launches[0] {
		t.Fatalf("launch headless sequence=%v", launches)
	}
	if _, err := os.Stat(filepath.Join(got.OutputDir, "0001.png")); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserEnginesCollectReaderFixture(t *testing.T) {
	if os.Getenv("MYREADING_BROWSER_SMOKE") != "1" {
		t.Skip("set MYREADING_BROWSER_SMOKE=1")
	}
	bin := os.Getenv("MYREADING_BROWSER_BIN")
	if bin == "" {
		bin = `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("browser missing: %s", bin)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/reader", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><main><h1>Fixture Comic</h1><img data-src="/page-1.jpg"><a rel="next" href="/reader-2">next</a></main></body></html>`)
	})
	mux.HandleFunc("/challenge", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Just a moment...</title></head><body>Checking your browser</body></html>`)
	})
	mux.HandleFunc("/reader-2", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><article><img src="/page-2.png"></article></body></html>`)
	})
	mux.HandleFunc("/page-1.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNGBytes(t))
	})
	mux.HandleFunc("/page-2.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNGBytes(t))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	factories := map[string]func(string, string, bool, string) (myreadingBrowser, error){"rod": newRodMyreadingBrowser, "playwright": newPlaywrightMyreadingBrowser}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			b, err := factory(bin, t.TempDir(), true, "")
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			challenge, err := b.Snapshot(context.Background(), srv.URL+"/challenge")
			if err != nil {
				t.Fatal(err)
			}
			if !challenge.Verification {
				t.Fatalf("challenge snapshot=%+v", challenge)
			}
			snap, err := b.Snapshot(context.Background(), srv.URL+"/reader")
			if err != nil {
				t.Fatal(err)
			}
			if snap.Title != "Fixture Comic" || len(snap.Images) != 1 || snap.Next == "" {
				t.Fatalf("snapshot=%+v", snap)
			}
			snap2, err := b.Snapshot(context.Background(), snap.Next)
			if err != nil {
				t.Fatal(err)
			}
			if len(snap2.Images) != 1 {
				t.Fatalf("snapshot2=%+v", snap2)
			}
			var liveBytes int64
			resource, _, err := b.Resource(context.Background(), snap2.Images[0], func(delta int64) { liveBytes += delta })
			if err != nil || len(resource) == 0 {
				t.Fatalf("browser resource bytes=%d err=%v", len(resource), err)
			}
			if liveBytes <= 0 {
				t.Fatal("browser resource did not report live network bytes")
			}
			cookies, err := b.Cookies(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			done, _, err := downloadMyreadingImages(context.Background(), nil, append(snap.Images, snap2.Images...), srv.URL+"/reader", out, cookies, 1, func(int, int, int64, int64) {}, nil)
			if err != nil || done != 2 {
				t.Fatalf("download done=%d err=%v", done, err)
			}
		})
	}
}

func TestRodHeadedRealChromeSmoke(t *testing.T) {
	if os.Getenv("MYREADING_HEADED_SMOKE") != "1" {
		t.Skip("set MYREADING_HEADED_SMOKE=1")
	}
	bin := os.Getenv("MYREADING_BROWSER_BIN")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("browser missing: %s", bin)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Just a moment...</title></head><body>Checking your browser</body></html>`)
	}))
	defer srv.Close()
	profile := t.TempDir()
	browser, err := newRodMyreadingBrowser(bin, profile, false, "")
	if err != nil {
		t.Fatal(err)
	}
	rodBrowser := browser.(*rodMyreadingBrowser)
	defer browser.Close()
	joined := strings.Join(rodBrowser.process.cmd.Args, " ")
	if strings.Contains(joined, "--headless") || strings.Contains(joined, "--enable-automation") || strings.Contains(joined, "--lang=") {
		t.Fatalf("headed real-browser arguments contain automation mode: %s", joined)
	}
	for _, required := range []string{"--window-position=-32000,-32000", "--disable-background-timer-throttling", "--disable-backgrounding-occluded-windows", "--disable-renderer-backgrounding"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("headed hidden-start argument missing %s: %s", required, joined)
		}
	}
	if rodBrowser.process.WindowVisible() {
		t.Fatal("headed browser was visible before verification")
	}
	metrics, err := rodBrowser.page.Eval(`() => ({outerWidth, outerHeight, innerWidth, innerHeight, devicePixelRatio})`)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native viewport=%s", metrics.Value.String())
	nativeViewport, err := rodBrowser.page.Eval(`() => Math.abs(outerWidth - innerWidth) <= 32 && innerHeight > 700`)
	if err != nil || !nativeViewport.Value.Bool() {
		t.Fatalf("browser content area is still emulated: metrics=%s err=%v", metrics.Value.String(), err)
	}
	snap, err := browser.Snapshot(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Verification {
		t.Fatalf("challenge snapshot=%+v", snap)
	}
	if err := browser.SetVisible(true); err != nil {
		t.Fatal(err)
	}
	if !rodBrowser.process.WindowVisible() {
		t.Fatal("verification browser did not become visible")
	}
	time.Sleep(time.Second)
	if rodBrowser.process.cmd.ProcessState != nil && rodBrowser.process.cmd.ProcessState.Exited() {
		t.Fatal("headed browser exited while waiting for verification")
	}
	browser.Close()
	browser.Close() // Close is intentionally idempotent.
	if rodBrowser.process.cmd.ProcessState == nil || !rodBrowser.process.cmd.ProcessState.Exited() {
		t.Fatal("Chromium parent process was not reaped")
	}
	probe := filepath.Join(profile, "resource-release.probe")
	if err := os.WriteFile(probe, []byte("released"), 0o644); err != nil {
		t.Fatalf("browser profile still locked after close: %v", err)
	}
}

func TestRealMyreadingFirstBrowserResourceSmoke(t *testing.T) {
	if os.Getenv("MYREADING_REAL_RESOURCE_SMOKE") != "1" {
		t.Skip("set MYREADING_REAL_RESOURCE_SMOKE=1")
	}
	bin := os.Getenv("MYREADING_BROWSER_BIN")
	profile := os.Getenv("MYREADING_BROWSER_PROFILE")
	target := os.Getenv("MYREADING_REAL_URL")
	engine := strings.TrimSpace(os.Getenv("MYREADING_REAL_ENGINE"))
	if engine == "" {
		engine = "rod"
	}
	browser, err := newMyreadingBrowser(engine, bin, profile, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	switch current := browser.(type) {
	case *rodMyreadingBrowser:
		if current.process.WindowVisible() {
			t.Fatal("Rod browser window became visible during hidden startup")
		}
	case *playwrightMyreadingBrowser:
		if current.process.WindowVisible() {
			t.Fatal("Playwright browser window became visible during hidden startup")
		}
	}
	if err := browser.SetVisible(false); err != nil {
		t.Fatalf("hide real browser: %v", err)
	}
	snap, err := browser.Snapshot(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Verification || len(snap.Images) == 0 {
		t.Fatalf("real snapshot title=%q verification=%v images=%d", snap.Title, snap.Verification, len(snap.Images))
	}
	limit := 1
	if os.Getenv("MYREADING_REAL_ALL_RESOURCES") == "1" {
		limit = len(snap.Images)
	}
	temp := t.TempDir()
	var total int64
	for i, rawURL := range snap.Images[:limit] {
		payload, contentType, resourceErr := browser.Resource(context.Background(), rawURL, nil)
		if resourceErr != nil {
			t.Fatalf("resource %d/%d: %v", i+1, limit, resourceErr)
		}
		path := filepath.Join(temp, fmt.Sprintf("%04d.part", i+1))
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := validateDownloadedImage(path, contentType); err != nil {
			t.Fatalf("resource %d/%d bytes=%d contentType=%q err=%v", i+1, limit, len(payload), contentType, err)
		}
		total += int64(len(payload))
		t.Logf("real browser resource %d/%d bytes=%d", i+1, limit, len(payload))
	}
	t.Logf("real browser resource engine=%s title=%q images=%d verified=%d totalBytes=%d", engine, snap.Title, len(snap.Images), limit, total)
}
