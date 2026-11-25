//go:build windows

package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var chromiumPathState struct {
	mu   sync.RWMutex
	path string
}

func setChromiumPath(path string) {
	chromiumPathState.mu.Lock()
	chromiumPathState.path = strings.TrimSpace(path)
	chromiumPathState.mu.Unlock()
}

func configuredChromiumPath() string {
	// A portable bundle must remain self-contained even when this development
	// machine also has the legacy source checkout available.
	if bundled := bundledChromiumPath(); bundled != "" {
		return bundled
	}
	// Keep the refactored Go worker on the same portable Chromium build as the
	// proven desktop implementation. This takes precedence over stale UI state
	// that may still point at Edge.
	if legacy := sourceProjectChromiumPath(); legacy != "" {
		return legacy
	}
	chromiumPathState.mu.RLock()
	override := strings.TrimSpace(chromiumPathState.path)
	chromiumPathState.mu.RUnlock()
	if override != "" {
		if resolved := resolveChromiumExecutablePath(override); resolved != "" {
			return resolved
		}
	}
	return detectSystemChromiumPath()
}

func sourceProjectChromiumPath() string {
	return resolveChromiumExecutablePath(`F:\Project\01_Comics_Images\comic_downloader\runtime\chromium\chrome.exe`)
}

func detectSystemChromiumPath() string {
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	programFiles := strings.TrimSpace(os.Getenv("ProgramFiles"))
	programFilesX86 := strings.TrimSpace(os.Getenv("ProgramFiles(x86)"))
	candidates := []string{
		filepath.Join(localAppData, "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(programFilesX86, "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(programFiles, "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(programFiles, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(programFilesX86, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(localAppData, "Chromium", "Application", "chrome.exe"),
		filepath.Join(programFiles, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
		filepath.Join(localAppData, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
	}
	for _, candidate := range candidates {
		if resolved := resolveChromiumExecutablePath(candidate); resolved != "" {
			return resolved
		}
	}
	for _, name := range []string{"msedge.exe", "chrome.exe", "chromium.exe", "brave.exe"} {
		if candidate, err := exec.LookPath(name); err == nil {
			if resolved := resolveChromiumExecutablePath(candidate); resolved != "" {
				return resolved
			}
		}
	}
	return ""
}

func currentChromiumPath() string {
	return configuredChromiumPath()
}

func chromiumPathReady() bool {
	return configuredChromiumPath() != ""
}

func resolveChromiumExecutablePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			if resolved := resolveChromiumExecutableInDir(path); resolved != "" {
				return resolved
			}
			return ""
		}
		if strings.EqualFold(filepath.Base(path), "chrome.exe") || strings.EqualFold(filepath.Base(path), "chrome") || strings.EqualFold(filepath.Base(path), "Chrome.exe") {
			return path
		}
	}
	if filepath.Ext(path) == ".exe" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func resolveChromiumExecutableInDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	for _, relative := range []string{
		"chrome.exe",
		"Chrome.exe",
		"chrome",
	} {
		candidate := filepath.Join(dir, relative)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	found := ""
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d == nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if name == "chrome.exe" || name == "chrome" {
			found = path
		}
		return nil
	})
	return found
}

func verifyChromiumExecutable(chromiumPath string) error {
	chromiumPath = resolveChromiumExecutablePath(chromiumPath)
	if chromiumPath == "" {
		return errors.New("chromium executable not found")
	}
	cmd := exec.Command(chromiumPath, "--version")
	cmd.SysProcAttr = hiddenSysProcAttr()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("chromium verification failed: %w", err)
		}
		return nil
	case <-time.After(30 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return errors.New("chromium verification timed out")
	}
}

func downloadChromiumToRuntime(runtimeRoot string) (string, error) {
	runtimeRoot = strings.TrimSpace(runtimeRoot)
	if runtimeRoot == "" {
		runtimeRoot = runtimeRootDir()
	}
	targetRoot := filepath.Join(runtimeRoot, "chromium")
	tempRoot := filepath.Join(projectRootDir(), ".chromium-download")
	if err := os.MkdirAll(tempRoot, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(runtimeRoot, 0o755); err != nil {
		return "", err
	}

	mirrors := []string{
		"https://repo.huaweicloud.com/chromium-browser-snapshots",
		"https://commondatastorage.googleapis.com/chromium-browser-snapshots",
	}
	revision, err := chromiumSnapshotRevision(mirrors)
	if err != nil {
		return "", err
	}

	zipPath := filepath.Join(tempRoot, "chromium-win.zip")
	if err := chromiumSnapshotDownload(zipPath, mirrors, revision); err != nil {
		return "", err
	}

	extractRoot := filepath.Join(tempRoot, "extract")
	if err := os.RemoveAll(extractRoot); err != nil {
		return "", err
	}
	if err := os.MkdirAll(extractRoot, 0o755); err != nil {
		return "", err
	}
	if err := unzipToDir(zipPath, extractRoot); err != nil {
		return "", err
	}

	chromeRoot, err := chromiumPackageRoot(extractRoot)
	if err != nil {
		return "", err
	}

	if err := os.RemoveAll(targetRoot); err != nil {
		return "", err
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return "", err
	}
	if err := copyDirContents(chromeRoot, targetRoot); err != nil {
		return "", err
	}

	chromiumExe := resolveChromiumExecutablePath(filepath.Join(targetRoot, "chrome.exe"))
	if chromiumExe == "" {
		chromiumExe = resolveChromiumExecutableInDir(targetRoot)
	}
	if chromiumExe == "" {
		return "", errors.New("chromium download finished but chrome.exe was not found")
	}
	if err := verifyChromiumExecutable(chromiumExe); err != nil {
		return "", err
	}
	return chromiumExe, nil
}

func chromiumSnapshotRevision(mirrors []string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	for _, mirror := range mirrors {
		mirror = strings.TrimRight(strings.TrimSpace(mirror), "/")
		if mirror == "" {
			continue
		}
		req, err := http.NewRequest(http.MethodGet, mirror+"/Win_x64/LAST_CHANGE", nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			continue
		}
		revision := strings.TrimSpace(string(body))
		if revision != "" {
			return revision, nil
		}
	}
	return "", errors.New("unable to resolve chromium revision from mirrors")
}

func chromiumSnapshotDownload(zipPath string, mirrors []string, revision string) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	rel := fmt.Sprintf("/Win_x64/%s/chrome-win.zip", revision)
	for i, mirror := range mirrors {
		mirror = strings.TrimRight(strings.TrimSpace(mirror), "/")
		if mirror == "" {
			continue
		}
		url := mirror + rel
		if i == 0 {
			log.Printf("chromium download start: %s", url)
		} else {
			log.Printf("chromium download fallback: %s", url)
		}
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			continue
		}
		tmpPath := zipPath + ".tmp"
		file, err := os.Create(tmpPath)
		if err != nil {
			_ = resp.Body.Close()
			return err
		}
		_, copyErr := io.Copy(file, resp.Body)
		closeErr := file.Close()
		_ = resp.Body.Close()
		if copyErr != nil {
			_ = os.Remove(tmpPath)
			continue
		}
		if closeErr != nil {
			_ = os.Remove(tmpPath)
			return closeErr
		}
		if err := os.Rename(tmpPath, zipPath); err != nil {
			_ = os.Remove(tmpPath)
			return err
		}
		return nil
	}
	return errors.New("failed to download chromium package from mirrors")
}

func unzipToDir(zipPath, dstDir string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		targetPath, err := safeZipTargetPath(dstDir, file.Name)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		dst, err := os.Create(targetPath)
		if err != nil {
			_ = src.Close()
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			_ = src.Close()
			_ = dst.Close()
			return err
		}
		_ = src.Close()
		if err := dst.Close(); err != nil {
			return err
		}
	}
	return nil
}

func safeZipTargetPath(dstDir, name string) (string, error) {
	dstDir = filepath.Clean(dstDir)
	targetPath := filepath.Join(dstDir, name)
	cleanTarget := filepath.Clean(targetPath)
	rel, err := filepath.Rel(dstDir, cleanTarget)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("zip entry escapes target directory: %s", name)
	}
	return cleanTarget, nil
}

func chromiumPackageRoot(extractRoot string) (string, error) {
	candidates := []string{
		filepath.Join(extractRoot, "chrome-win"),
		filepath.Join(extractRoot, "chrome-win64"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, "chrome.exe")); err == nil {
			return candidate, nil
		}
	}
	found := ""
	err := filepath.WalkDir(extractRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d == nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), "chrome.exe") {
			found = filepath.Dir(path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("chrome.exe not found in downloaded chromium package")
	}
	return found, nil
}

func copyDirContents(srcDir, dstDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			_ = dst.Close()
			return err
		}
		return dst.Close()
	})
}
