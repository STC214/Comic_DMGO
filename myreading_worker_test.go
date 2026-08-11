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
	done, bytes, err := downloadMyreadingImages(context.Background(), nil, []string{srv.URL + "/one.jpg", srv.URL + "/two.png"}, srv.URL+"/reader", out, nil, 2, func(int, int, int64) {})
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
	_, _, err := downloadMyreadingImages(context.Background(), nil, []string{srv.URL + "/page.jpg"}, srv.URL, out, nil, 1, func(int, int, int64) {})
	if err == nil {
		t.Fatal("expected HTML response to be rejected")
	}
	if matches, _ := filepath.Glob(filepath.Join(out, "0001*")); len(matches) != 0 {
		t.Fatalf("unexpected output files: %v", matches)
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
		_, _, err := downloadMyreadingImages(ctx, nil, []string{srv.URL + "/slow.jpg"}, srv.URL, t.TempDir(), nil, 2, func(int, int, int64) {})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected cancellation error")
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
func (b *fakeMyreadingBrowser) Resource(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
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
			resource, _, err := b.Resource(context.Background(), snap2.Images[0])
			if err != nil || len(resource) == 0 {
				t.Fatalf("browser resource bytes=%d err=%v", len(resource), err)
			}
			cookies, err := b.Cookies(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			done, _, err := downloadMyreadingImages(context.Background(), nil, append(snap.Images, snap2.Images...), srv.URL+"/reader", out, cookies, 1, func(int, int, int64) {})
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
	browser, err := newRodMyreadingBrowser(bin, t.TempDir(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	rodBrowser := browser.(*rodMyreadingBrowser)
	defer browser.Close()
	joined := strings.Join(rodBrowser.process.cmd.Args, " ")
	if strings.Contains(joined, "--headless") || strings.Contains(joined, "--enable-automation") || strings.Contains(joined, "--window-size") || strings.Contains(joined, "--lang=") {
		t.Fatalf("headed real-browser arguments contain automation mode: %s", joined)
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
	time.Sleep(time.Second)
	if rodBrowser.process.cmd.ProcessState != nil && rodBrowser.process.cmd.ProcessState.Exited() {
		t.Fatal("headed browser exited while waiting for verification")
	}
}

func TestRealMyreadingFirstBrowserResourceSmoke(t *testing.T) {
	if os.Getenv("MYREADING_REAL_RESOURCE_SMOKE") != "1" {
		t.Skip("set MYREADING_REAL_RESOURCE_SMOKE=1")
	}
	bin := os.Getenv("MYREADING_BROWSER_BIN")
	profile := os.Getenv("MYREADING_BROWSER_PROFILE")
	target := os.Getenv("MYREADING_REAL_URL")
	browser, err := newRodMyreadingBrowser(bin, profile, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	snap, err := browser.Snapshot(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Verification || len(snap.Images) == 0 {
		t.Fatalf("real snapshot title=%q verification=%v images=%d", snap.Title, snap.Verification, len(snap.Images))
	}
	payload, contentType, err := browser.Resource(context.Background(), snap.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "first.part")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateDownloadedImage(path, contentType); err != nil {
		t.Fatalf("browser resource bytes=%d contentType=%q err=%v", len(payload), contentType, err)
	}
	t.Logf("real browser resource title=%q images=%d firstBytes=%d contentType=%q", snap.Title, len(snap.Images), len(payload), contentType)
}
