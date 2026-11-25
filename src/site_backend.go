package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type siteAdapter struct {
	name          string
	hostSuffix    string
	label         string
	daemonFactory func() *siteWorkerDaemon
	scriptPath    []string
	startDetail   string
	daemonPrefix  string
	adblock       bool
}

type siteConfig struct {
	name          string
	hostSuffix    string
	label         string
	daemonFactory func() *siteWorkerDaemon
	scriptPath    []string
	startDetail   string
	daemonPrefix  string
	adblock       bool
}

type siteRoute struct {
	adapter siteAdapter
	source  string
	ok      bool
}

// prefilterSiteRoute performs the first-pass site routing for a URL.
// It decides which worker should handle the task before execution starts.
func prefilterSiteRoute(rawURL string) siteRoute {
	return resolveSiteRoute(rawURL, "")
}

var siteConfigs = []siteConfig{
	{
		name:          "myreadingmanga",
		hostSuffix:    "myreadingmanga.info",
		label:         "myreadingmanga",
		daemonFactory: func() *siteWorkerDaemon { return &siteWorkerDaemon{label: "myreadingmanga"} },
		scriptPath:    []string{"workers", "myreadingmanga_drissionpage", "main.py"},
		startDetail:   "starting myreadingmanga worker",
		daemonPrefix:  "myreadingmanga",
		adblock:       true,
	},
}

var siteAdapters = buildSiteAdapters(siteConfigs)
var siteAdapterIndex = buildSiteAdapterIndex(siteAdapters)
var myreadingVerificationMu sync.Mutex

func buildSiteAdapters(configs []siteConfig) []siteAdapter {
	adapters := make([]siteAdapter, 0, len(configs))
	for _, config := range configs {
		adapters = append(adapters, config.adapter())
	}
	return adapters
}

func buildSiteAdapterIndex(adapters []siteAdapter) map[string]siteAdapter {
	index := make(map[string]siteAdapter, len(adapters)*2)
	for _, adapter := range adapters {
		index[strings.ToLower(adapter.name)] = adapter
		index[strings.ToLower(adapter.label)] = adapter
	}
	return index
}

func (c siteConfig) adapter() siteAdapter {
	return siteAdapter{
		name:          c.name,
		hostSuffix:    c.hostSuffix,
		label:         c.label,
		daemonFactory: c.daemonFactory,
		scriptPath:    append([]string(nil), c.scriptPath...),
		startDetail:   c.startDetail,
		daemonPrefix:  c.daemonPrefix,
		adblock:       c.adblock,
	}
}

func detectSiteAdapter(rawURL string) (siteAdapter, bool) {
	for _, adapter := range siteAdapters {
		if adapter.supports(rawURL) {
			log.Printf("site route matched by host url=%s adapter=%s hostSuffix=%s", rawURL, adapter.name, adapter.hostSuffix)
			return adapter, true
		}
	}
	return siteAdapter{}, false
}

func siteAdapterByName(name string) (siteAdapter, bool) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return siteAdapter{}, false
	}
	if adapter, ok := siteAdapterIndex[name]; ok {
		return adapter, true
	}
	return siteAdapter{}, false
}

func resolveSiteRoute(rawURL, worker string) siteRoute {
	if adapter, ok := siteAdapterByName(worker); ok {
		log.Printf("site route matched by worker url=%s worker=%s adapter=%s", rawURL, worker, adapter.name)
		return siteRoute{adapter: adapter, source: "task.worker", ok: true}
	}
	host := taskHost(rawURL)
	if isMyreadingHost(host) {
		if adapter, ok := siteAdapterByName("myreadingmanga"); ok {
			log.Printf("site route matched myreading host url=%s host=%s adapter=%s", rawURL, host, adapter.name)
			return siteRoute{adapter: adapter, source: "site:myreadingmanga", ok: true}
		}
	}
	log.Printf("site route unresolved url=%s host=%s", rawURL, host)
	return siteRoute{source: "default", ok: false}
}

func isMyreadingHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "myreadingmanga.info" || strings.HasSuffix(host, ".myreadingmanga.info")
}

func (a siteAdapter) supports(rawURL string) bool {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(taskHost(rawURL))), ".")
	suffix := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(a.hostSuffix)), ".")
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

func (a siteAdapter) script() string {
	return resourcePath(a.scriptPath...)
}

func (a siteAdapter) outputDir() string {
	return filepath.Join(projectRootDir(), "download")
}

func taskScopedOutputDir(base string, taskID int) string {
	_ = taskID
	return strings.TrimSpace(base)
}

func (a siteAdapter) progressFile(id int) string {
	return filepath.Join(projectRootDir(), ".tmp", "progress", fmt.Sprintf("%s-task-%d-%d.jsonl", a.daemonPrefix, id, os.Getpid()))
}

func (a siteAdapter) payload(task Task, progressFile string, runtimeRoot string) map[string]any {
	downloadRoot := strings.TrimSpace(task.DownloadRoot)
	if downloadRoot == "" {
		downloadRoot = a.outputDir()
	}
	downloadRoot = taskScopedOutputDir(downloadRoot, task.ID)
	payload := map[string]any{
		"url":          task.URL,
		"title":        taskWorkerTitle(task),
		"outputDir":    downloadRoot,
		"downloadRoot": downloadRoot,
		"runtimeRoot":  runtimeRoot,
		"headless":     task.Headless,
		"httpOnly":     task.HttpOnly,
		"download":     true,
		"probe":        false,
	}
	if progressFile != "" {
		payload["progressFile"] = progressFile
	}
	if a.adblock {
		payload["adblock"] = true
	}
	return payload
}

func (a siteAdapter) summary(result map[string]any, fallbackOutput string) string {
	title := resultString(result, "title")
	downloaded := intFromAny(result["downloaded_pages"])
	expected := intFromAny(result["expected_pages"])
	if expected == 0 {
		expected = intFromAny(result["page_count"])
	}
	target := resultString(result, "output_dir")
	if target == "" {
		target = fallbackOutput
	}
	return fmt.Sprintf("done | 完成 | title=%s | pages=%d/%d | output=%s", title, downloaded, expected, target)
}

func normalizeWorkerTitle(worker, title string) string {
	text := strings.TrimSpace(title)
	if text == "" {
		return ""
	}
	return strings.Trim(text, " -_")
}

func (a siteAdapter) applyProgressHook(daemon *siteWorkerDaemon, m *Manager, id int) {
	if daemon == nil {
		return
	}
	daemon.setProgressHook(func(percent float64, detail string) {
		_ = m.setState(id, TaskRunning, clampTaskProgress(percent), detail)
	})
}

type workerJSONLProgressEvent struct {
	Phase   string  `json:"phase"`
	Stage   string  `json:"stage"`
	Done    int     `json:"done"`
	Total   int     `json:"total"`
	Percent float64 `json:"percent"`
	Detail  string  `json:"detail"`
	Ok      bool    `json:"ok"`
	Title   string  `json:"title"`
	Source  string  `json:"source_url"`
	Output  string  `json:"output_dir"`
	URL     string  `json:"url"`
	Path    string  `json:"path"`
	Error   string  `json:"error"`
	Bytes   int64   `json:"bytes"`
}

func downloadPhasePercent(stage string, percent float64, done, total int) float64 {
	if total <= 0 {
		total = 1
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	frac := float64(done) / float64(total)
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "resolve_start":
		return 0.36
	case "queue_start":
		return 0.70
	case "queued":
		return 0.70 + 0.05*frac
	case "saved", "failed":
		return 0.75 + 0.24*(percent/100.0)
	case "done":
		return 0.99
	default:
		if percent > 0 {
			return 0.75 + 0.24*(percent/100.0)
		}
		return 0.75 + 0.24*frac
	}
}

func clampTaskProgress(percent float64) float64 {
	if percent < 0 {
		return 0
	}
	if percent > 1 {
		return 1
	}
	return percent
}

func downloadDetailForEvent(ev workerJSONLProgressEvent) string {
	stage := strings.ToLower(strings.TrimSpace(ev.Stage))
	switch stage {
	case "resolve_start":
		return "resolving image URLs"
	case "queue_start":
		return "preparing downloads"
	case "queued":
		if ev.Total > 0 {
			return fmt.Sprintf("queued %d/%d", ev.Done, ev.Total)
		}
		return "queuing downloads"
	case "saved":
		if ev.Total > 0 {
			return fmt.Sprintf("downloading images %d/%d", ev.Done, ev.Total)
		}
		return "downloading images"
	case "failed":
		if ev.Total > 0 {
			return fmt.Sprintf("download failed %d/%d", ev.Done, ev.Total)
		}
		return "download failed"
	case "done":
		if ev.Ok {
			return "download complete"
		}
		return "download finished with errors"
	default:
		if ev.Detail != "" {
			return ev.Detail
		}
		return "downloading images"
	}
}

func formatByteRate(bytesPerSecond float64) string {
	if bytesPerSecond <= 0 {
		return ""
	}
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
	value := bytesPerSecond
	unit := units[0]
	for _, next := range units[1:] {
		if value < 1024 {
			break
		}
		value /= 1024
		unit = next
	}
	if value >= 10 || unit == units[0] {
		return fmt.Sprintf("%.0f %s", value, unit)
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

func formatAverageByteRate(bytesPerSecond float64) string {
	rate := formatByteRate(bytesPerSecond)
	if rate == "" {
		return ""
	}
	return "\u5e73\u5747 " + rate
}

func formatRealtimeByteRate(bytesPerSecond float64) string {
	rate := formatByteRate(bytesPerSecond)
	if rate == "" {
		return ""
	}
	return "\u5b9e\u65f6 " + rate
}

func watchProgressFile(m *Manager, id int, progressFile string, stop <-chan struct{}, trace []string, label string) {
	if progressFile == "" {
		return
	}
	path := progressFile
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.Remove(path)
	log.Printf("%s progress watcher start id=%d path=%s", label, id, path)
	var offset int64
	var tail string
	seenEvent := false
	var downloadedBytes int64
	var speedStartedAt time.Time
	var lastSpeedBytes int64
	var lastSpeedAt time.Time
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		if int64(len(data)) < offset {
			offset = 0
			tail = ""
		}
		if offset > int64(len(data)) {
			offset = 0
			tail = ""
		}
		chunk := tail + string(data[offset:])
		if chunk == "" {
			offset = int64(len(data))
			continue
		}
		lines := strings.Split(chunk, "\n")

		if !strings.HasSuffix(chunk, "\n") {

			tail = lines[len(lines)-1]
			lines = lines[:len(lines)-1]
		} else {
			tail = ""
		}
		offset = int64(len(data))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var ev workerJSONLProgressEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			if strings.ToLower(strings.TrimSpace(ev.Phase)) != "download" {
				continue
			}
			if !seenEvent {
				seenEvent = true
				log.Printf("%s progress watcher first event id=%d stage=%s done=%d total=%d", label, id, ev.Stage, ev.Done, ev.Total)
			}
			now := time.Now()
			stage := strings.ToLower(strings.TrimSpace(ev.Stage))
			if stage == "resolve_start" || stage == "queue_start" {
				if downloadedBytes == 0 {
					speedStartedAt = now
					lastSpeedAt = now
					lastSpeedBytes = 0
				}
			}
			if title := strings.TrimSpace(ev.Title); title != "" {
				_ = m.setTitle(id, title)
			}
			if output := strings.TrimSpace(ev.Output); output != "" {
				_ = m.setOutputDir(id, output)
			}
			if ev.Bytes > 0 {
				downloadedBytes += ev.Bytes
			}
			if speedStartedAt.IsZero() && downloadedBytes > 0 {
				speedStartedAt = now
				lastSpeedAt = now
				lastSpeedBytes = downloadedBytes
			}
			if !speedStartedAt.IsZero() && downloadedBytes > 0 {
				elapsed := now.Sub(lastSpeedAt).Seconds()
				if elapsed >= 0.5 && downloadedBytes > lastSpeedBytes {
					_ = m.setTaskSpeed(id, formatRealtimeByteRate(float64(downloadedBytes-lastSpeedBytes)/elapsed))
					lastSpeedBytes = downloadedBytes
					lastSpeedAt = now
				}
			}
			detail := downloadDetailForEvent(ev)
			percent := downloadPhasePercent(ev.Stage, ev.Percent, ev.Done, ev.Total)
			if stage == "done" {
				if ev.Ok {
					_ = m.setState(id, TaskDone, 1, detail)
				} else {
					_ = m.setState(id, TaskError, percent, detail)
				}
				if !speedStartedAt.IsZero() && downloadedBytes > 0 {
					if elapsed := now.Sub(speedStartedAt).Seconds(); elapsed > 0 {
						_ = m.setTaskSpeed(id, formatAverageByteRate(float64(downloadedBytes)/elapsed))
					}
				}
				if trace != nil {
					log.Printf("%s progress done: %s", label, path)
				}
				continue
			}
			_ = m.setState(id, TaskRunning, percent, detail)
		}
	}
}

func (a siteAdapter) handleResult(m *Manager, id int, task Task, result map[string]any, fallbackOutput string) bool {
	if a.name == "myreadingmanga" && isMyreadingVerificationResult(result) {
		note := strings.TrimSpace(fmt.Sprint(result["note"]))
		if note == "" {
			note = "verification required"
		}
		m.setState(id, TaskWaitingVerification, clampTaskProgress(task.Percent), "waiting for verification")
		log.Printf("%s worker verification pending id=%d worker=%s title=%s pageType=%s blocked=%v verificationNeeded=%v matched=%s url=%s note=%s",
			a.name,
			id,
			strings.TrimSpace(fmt.Sprint(result["worker"])),
			strings.TrimSpace(fmt.Sprint(result["title"])),
			strings.TrimSpace(fmt.Sprint(result["page_type"])),
			result["blocked"],
			result["verificationNeeded"],
			strings.TrimSpace(fmt.Sprint(result["matched_marker"])),
			strings.TrimSpace(fmt.Sprint(result["url"])),
			note,
		)
		return false
	}

	if okValue, exists := result["ok"]; exists {
		if ok, _ := okValue.(bool); !ok {
			note := strings.TrimSpace(fmt.Sprint(result["note"]))
			if note == "" {
				note = strings.TrimSpace(fmt.Sprint(result["kind"]))
			}
			if note == "" {
				note = "worker returned failure"
			}
			log.Printf("%s worker failure detail id=%d worker=%s title=%s pageType=%s blocked=%v verificationNeeded=%v matched=%s url=%s note=%s",
				a.name,
				id,
				strings.TrimSpace(fmt.Sprint(result["worker"])),
				strings.TrimSpace(fmt.Sprint(result["title"])),
				strings.TrimSpace(fmt.Sprint(result["page_type"])),
				result["blocked"],
				result["verificationNeeded"],
				strings.TrimSpace(fmt.Sprint(result["matched_marker"])),
				strings.TrimSpace(fmt.Sprint(result["url"])),
				note,
			)
			m.setState(id, TaskError, 0.12, note)
			log.Printf("%s worker returned failure id=%d note=%s", a.name, id, note)
			return false
		}
	}
	if ver, _ := result["verification"].(bool); ver {
		note := fmt.Sprint(result["note"])
		if note == "" {
			note = "verification page"
		}
		log.Printf("%s worker verification detail id=%d worker=%s title=%s pageType=%s blocked=%v verificationNeeded=%v matched=%s url=%s note=%s",
			a.name,
			id,
			strings.TrimSpace(fmt.Sprint(result["worker"])),
			strings.TrimSpace(fmt.Sprint(result["title"])),
			strings.TrimSpace(fmt.Sprint(result["page_type"])),
			result["blocked"],
			result["verificationNeeded"],
			strings.TrimSpace(fmt.Sprint(result["matched_marker"])),
			strings.TrimSpace(fmt.Sprint(result["url"])),
			note,
		)
		m.setState(id, TaskError, 0.12, note)
		log.Printf("%s worker verification page id=%d note=%s", a.name, id, note)
		return false
	}

	displayTitle := resultString(result, "display_title")
	title := resultString(result, "title")
	if displayTitle != "" {
		m.setTitle(id, displayTitle)
	} else if title != "" {
		title = normalizeWorkerTitle(a.name, title)
		if title != "" {
			m.setTitle(id, title)
		}
	}
	actualOutput := resultString(result, "output_dir")
	if actualOutput == "" {
		actualOutput = strings.TrimSpace(fallbackOutput)
	}
	if actualOutput != "" {
		m.setOutputDir(id, actualOutput)
	}
	thumbnailTitle := displayTitle
	if thumbnailTitle == "" {
		thumbnailTitle = title
	}
	if thumbnailTitle == "" {
		thumbnailTitle = titleFromOutputDir(actualOutput)
	}
	if thumbPath, err := buildTaskThumbnailFromResult(id, a.name, thumbnailTitle, actualOutput, result); err == nil && thumbPath != "" {
		m.setThumbnailPath(id, thumbPath)
	} else if err != nil {
		log.Printf("%s thumbnail failed id=%d title=%s output=%s err=%v", a.name, id, thumbnailTitle, actualOutput, err)
	}
	summary := a.summary(result, fallbackOutput)
	m.setState(id, TaskDone, 1, summary)
	finalTitle := displayTitle
	if finalTitle == "" {
		finalTitle = title
	}
	if finalTitle == "" {
		finalTitle = titleFromOutputDir(actualOutput)
	}
	expectedPages := intFromAny(result["expected_pages"])
	if expectedPages == 0 {
		expectedPages = intFromAny(result["page_count"])
	}
	log.Printf("%s worker completed id=%d title=%s pages=%d/%d output=%s", a.name, id, finalTitle, intFromAny(result["downloaded_pages"]), expectedPages, actualOutput)
	return true
}

func isMyreadingVerificationResult(result map[string]any) bool {
	if result == nil {
		return false
	}
	if verificationNeeded, _ := result["verificationNeeded"].(bool); verificationNeeded {
		return true
	}
	if verification, _ := result["verification"].(bool); verification {
		return true
	}
	pageType := strings.ToLower(strings.TrimSpace(fmt.Sprint(result["page_type"])))
	return pageType == "verification"
}

func logWorkerResultTrace(label string, id int, result map[string]any) {
	if result == nil {
		return
	}
	trace, ok := result["trace"]
	if !ok || trace == nil {
		return
	}
	switch items := trace.(type) {
	case []any:
		for index, item := range items {
			line := strings.TrimSpace(fmt.Sprint(item))
			if line != "" {
				log.Printf("%s worker trace id=%d step=%d %s", label, id, index+1, line)
			}
		}
	case []string:
		for index, line := range items {
			line = strings.TrimSpace(line)
			if line != "" {
				log.Printf("%s worker trace id=%d step=%d %s", label, id, index+1, line)
			}
		}
	default:
		line := strings.TrimSpace(fmt.Sprint(trace))
		if line != "" && !strings.EqualFold(line, "<nil>") {
			log.Printf("%s worker trace id=%d %s", label, id, line)
		}
	}
}

func (a siteAdapter) recoverMyreadingVerification(m *Manager, id int, task Task) {
	if a.name != "myreadingmanga" {
		return
	}
	if current, ok := m.taskCopy(id); !ok || current.State != TaskWaitingVerification {
		return
	}
	m.setStateIfCurrent(id, TaskWaitingVerification, TaskWaitingVerification, clampTaskProgress(task.Percent), "waiting for site verification window")
	myreadingVerificationMu.Lock()
	defer myreadingVerificationMu.Unlock()
	if current, ok := m.taskCopy(id); !ok || current.State != TaskWaitingVerification {
		return
	}
	script := a.script()
	chromiumPath := currentChromiumPath()
	if chromiumPath == "" {
		m.setStateIfCurrent(id, TaskWaitingVerification, TaskError, clampTaskProgress(task.Percent), "chromium executable not configured")
		log.Printf("%s verification recovery failed id=%d err=chromium executable not configured", a.name, id)
		return
	}
	runtimeRoot := runtimeRootDir()
	daemon := a.daemonFactory()
	if daemon == nil {
		m.setStateIfCurrent(id, TaskWaitingVerification, TaskError, clampTaskProgress(task.Percent), "verification helper unavailable")
		log.Printf("%s verification recovery failed id=%d err=verification helper unavailable", a.name, id)
		return
	}
	defer daemon.Close()
	m.registerActiveWorker(id, daemon)
	defer m.unregisterActiveWorker(id, daemon)

	contentProfile, err := ensureTaskBrowserProfile(id, a.name, "content", chromiumPath, "", false)
	if err != nil {
		m.setStateIfCurrent(id, TaskWaitingVerification, TaskError, clampTaskProgress(task.Percent), "verification helper profile unavailable: "+err.Error())
		log.Printf("%s verification recovery failed id=%d err=%v", a.name, id, err)
		if cleanupErr := cleanupTaskBrowserProfiles(id, a.name); cleanupErr != nil {
			log.Printf("%s verification recovery cleanup failed id=%d err=%v", a.name, id, cleanupErr)
		}
		return
	}
	if err := quiesceBrowserProfile(contentProfile); err != nil {
		m.setStateIfCurrent(id, TaskWaitingVerification, TaskError, clampTaskProgress(task.Percent), "verification helper profile still active: "+err.Error())
		log.Printf("%s verification recovery profile release failed id=%d err=%v", a.name, id, err)
		return
	}
	verificationURL := strings.TrimSpace(task.URL)
	progressFile := a.progressFile(id)
	payload := a.payload(task, progressFile, runtimeRoot)
	payload["headless"] = false
	payload["download"] = false
	payload["verificationOnly"] = true
	payload["waitSeconds"] = 600
	if current, ok := m.taskCopy(id); !ok || current.State != TaskWaitingVerification {
		return
	}
	daemon.setProgressHook(func(percent float64, detail string) {
		current, ok := m.taskCopy(id)
		if !ok {
			return
		}
		if current.State == TaskWaitingVerification {
			if detail == "checking verification" || detail == "waiting for verification" {
				m.setStateIfCurrent(id, TaskWaitingVerification, TaskWaitingVerification, clampTaskProgress(percent), detail)
				return
			}
			m.setStateIfCurrent(id, TaskWaitingVerification, TaskRunning, clampTaskProgress(percent), detail)
			return
		}
		if current.State == TaskRunning {
			m.setState(id, TaskRunning, clampTaskProgress(percent), detail)
		}
	})
	log.Printf("%s native browser recovery start id=%d url=%s profile=%s", a.name, id, verificationURL, contentProfile)
	result, err := daemon.Run(script, payload, chromiumPath, runtimeRoot, false, contentProfile)
	logWorkerResultTrace(a.name+" native recovery", id, result)
	daemon.Close()
	if releaseErr := quiesceBrowserProfile(contentProfile); releaseErr != nil {
		m.setState(id, TaskError, clampTaskProgress(task.Percent), "native browser profile still active: "+releaseErr.Error())
		log.Printf("%s native recovery profile release failed id=%d err=%v", a.name, id, releaseErr)
		return
	}
	if err != nil {
		m.setState(id, TaskError, clampTaskProgress(task.Percent), "native browser recovery failed: "+err.Error())
		log.Printf("%s native recovery failed id=%d err=%v", a.name, id, err)
		if cleanupErr := cleanupTaskBrowserProfiles(id, a.name); cleanupErr != nil {
			log.Printf("%s native recovery cleanup failed id=%d err=%v", a.name, id, cleanupErr)
		}
		return
	}
	if isMyreadingVerificationResult(result) {
		note := strings.TrimSpace(fmt.Sprint(result["note"]))
		if note == "" {
			note = "native browser verification was not completed"
		}
		m.setState(id, TaskError, clampTaskProgress(task.Percent), note)
		log.Printf("%s native recovery remained blocked id=%d note=%s", a.name, id, note)
		return
	}
	if okValue, _ := result["ok"].(bool); okValue {
		if machineProfile, machineErr := updateMachineBrowserProfile(chromiumPath, contentProfile); machineErr != nil {
			m.setState(id, TaskError, clampTaskProgress(task.Percent), "machine browser profile update failed: "+machineErr.Error())
			log.Printf("%s native recovery machine profile update failed id=%d err=%v", a.name, id, machineErr)
			return
		} else {
			log.Printf("%s native recovery machine profile updated id=%d path=%s", a.name, id, machineProfile)
		}
		if baselineProfile, baselineErr := updateVerifiedBrowserProfile(a.name, contentProfile); baselineErr != nil {
			m.setState(id, TaskError, clampTaskProgress(task.Percent), "verified browser baseline update failed: "+baselineErr.Error())
			log.Printf("%s native recovery baseline update failed id=%d err=%v", a.name, id, baselineErr)
			return
		} else {
			log.Printf("%s native recovery baseline updated id=%d path=%s", a.name, id, baselineProfile)
		}
	}

	// The headed browser was only a verification handoff and is already closed.
	// Resume the original task in a new headless browser with the captured profile.
	m.setState(id, TaskRunning, clampTaskProgress(task.Percent), "verification complete; resuming headless download")
	resumeDaemon := a.daemonFactory()
	if resumeDaemon == nil {
		m.setState(id, TaskError, clampTaskProgress(task.Percent), "headless resume helper unavailable")
		return
	}
	m.registerActiveWorker(id, resumeDaemon)
	defer m.unregisterActiveWorker(id, resumeDaemon)
	defer resumeDaemon.Close()
	a.applyProgressHook(resumeDaemon, m, id)
	resumePayload := a.payload(task, progressFile, runtimeRoot)
	resumePayload["headless"] = true
	resumePayload["download"] = true
	resumePayload["verificationOnly"] = false
	log.Printf("%s headless resume start id=%d url=%s profile=%s", a.name, id, verificationURL, contentProfile)
	result, err = resumeDaemon.Run(script, resumePayload, chromiumPath, runtimeRoot, true, contentProfile)
	logWorkerResultTrace(a.name+" headless resume", id, result)
	resumeDaemon.Close()
	if releaseErr := quiesceBrowserProfile(contentProfile); releaseErr != nil {
		m.setState(id, TaskError, clampTaskProgress(task.Percent), "headless resume profile still active: "+releaseErr.Error())
		return
	}
	if err != nil {
		m.setState(id, TaskError, clampTaskProgress(task.Percent), "headless resume failed: "+err.Error())
		return
	}
	outputDir := a.outputDir()
	if trimmed := strings.TrimSpace(task.DownloadRoot); trimmed != "" {
		outputDir = trimmed
	}
	outputDir = taskScopedOutputDir(outputDir, task.ID)
	completed := a.handleResult(m, id, task, result, outputDir)
	if completed {
		log.Printf("%s native browser recovery completed id=%d", a.name, id)
	}
	if cleanupErr := cleanupTaskBrowserProfiles(id, a.name); cleanupErr != nil {
		log.Printf("%s native recovery cleanup failed id=%d err=%v", a.name, id, cleanupErr)
	}
}

func (m *Manager) runSiteTask(id int, task Task, adapter siteAdapter) {
	// The myreading adapter is now implemented end-to-end in Go.  The legacy
	// JSONL/Python daemon remains below only for other adapters during migration.
	if adapter.name == "myreadingmanga" {
		m.runGoMyreadingTask(id, task, adapter)
		return
	}
	if !m.setState(id, TaskRunning, 0.08, adapter.startDetail) {
		return
	}
	log.Printf("%s worker start id=%d url=%s", adapter.label, id, task.URL)
	daemon := adapter.daemonFactory()
	if daemon == nil {
		m.setState(id, TaskError, 0, "worker unavailable")
		log.Printf("%s worker unavailable id=%d", adapter.label, id)
		return
	}
	m.registerActiveWorker(id, daemon)
	defer m.unregisterActiveWorker(id, daemon)
	defer daemon.Close()
	adapter.applyProgressHook(daemon, m, id)

	script := adapter.script()
	if _, err := os.Stat(script); err != nil {
		m.setState(id, TaskError, task.Percent, "worker missing: "+err.Error())
		log.Printf("%s worker missing id=%d err=%v", adapter.label, id, err)
		return
	}

	outputDir := adapter.outputDir()
	if trimmed := strings.TrimSpace(task.DownloadRoot); trimmed != "" {
		outputDir = trimmed
	}
	outputDir = taskScopedOutputDir(outputDir, task.ID)
	progressFile := adapter.progressFile(id)
	headless := task.Headless
	runtimeRoot := runtimeRootDir()
	payload := adapter.payload(task, progressFile, runtimeRoot)
	chromiumPath := currentChromiumPath()
	browserUserDataPath := ""
	if prepared, prepErr := ensureTaskBrowserProfile(id, adapter.name, "content", chromiumPath, "", false); prepErr == nil {
		browserUserDataPath = prepared
	} else {
		m.setState(id, TaskError, 0, "browser profile unavailable: "+prepErr.Error())
		log.Printf("%s browser profile prepare failed id=%d err=%v", adapter.label, id, prepErr)
		if cleanupErr := cleanupTaskBrowserProfiles(id, adapter.name); cleanupErr != nil {
			log.Printf("%s browser profile cleanup failed id=%d err=%v", adapter.label, id, cleanupErr)
		}
		return
	}
	log.Printf("%s worker dispatch id=%d script=%s outputDir=%s progressFile=%s headless=%v download=%v workerSource=%s",
		adapter.label, id, script, outputDir, progressFile, headless, true, task.WorkerSource)
	if browserUserDataPath != "" {
		log.Printf("%s worker browser profile id=%d path=%s", adapter.label, id, browserUserDataPath)
	}
	stopProgress := make(chan struct{})
	var progressWG sync.WaitGroup
	progressWG.Add(1)
	go func() {
		defer progressWG.Done()
		watchProgressFile(m, id, progressFile, stopProgress, nil, adapter.label)
	}()
	defer func() {
		close(stopProgress)
		progressWG.Wait()
	}()

	if chromiumPath == "" {
		m.setState(id, TaskError, 0, "chromium executable not configured")
		log.Printf("%s chromium missing id=%d", adapter.label, id)
		if cleanupErr := cleanupTaskBrowserProfiles(id, adapter.name); cleanupErr != nil {
			log.Printf("%s browser profile cleanup failed id=%d err=%v", adapter.label, id, cleanupErr)
		}
		return
	}
	result, err := daemon.Run(script, payload, chromiumPath, runtimeRoot, headless, browserUserDataPath)
	logWorkerResultTrace(adapter.label, id, result)
	if err != nil {
		if current, ok := m.taskCopy(id); ok && current.State == TaskDone {
			daemon.Close()
			if thumbPath, thumbErr := buildTaskThumbnailFromResult(id, adapter.name, current.Title, outputDir, result); thumbErr == nil && thumbPath != "" {
				_ = m.setThumbnailPath(id, thumbPath)
			} else if thumbErr != nil {
				log.Printf("%s thumbnail failed id=%d title=%s output=%s err=%v", adapter.label, id, current.Title, outputDir, thumbErr)
			}
			if cleanupErr := cleanupTaskBrowserProfiles(id, adapter.name); cleanupErr != nil {
				log.Printf("%s browser profile cleanup failed id=%d err=%v", adapter.label, id, cleanupErr)
			}
			log.Printf("%s worker ended after completion id=%d err=%v", adapter.label, id, err)
			return
		}
		m.setState(id, TaskError, 0, "worker failed: "+err.Error())
		log.Printf("%s worker failed id=%d err=%v", adapter.label, id, err)
		daemon.Close()
		if cleanupErr := cleanupTaskBrowserProfiles(id, adapter.name); cleanupErr != nil {
			log.Printf("%s browser profile cleanup failed id=%d err=%v", adapter.label, id, cleanupErr)
		}
		return
	}

	if !adapter.handleResult(m, id, task, result, outputDir) {
		current, ok := m.taskCopy(id)
		if ok && current.State == TaskWaitingVerification {
			// Native verification reuses the content profile. Stop the headless worker
			// first so Chromium flushes and unlocks it before the visible run starts.
			daemon.Close()
			if err := quiesceBrowserProfile(browserUserDataPath); err != nil {
				m.setStateIfCurrent(id, TaskWaitingVerification, TaskError, clampTaskProgress(task.Percent), "verification helper profile still active: "+err.Error())
				log.Printf("%s verification profile release failed id=%d err=%v", adapter.label, id, err)
				return
			}
			// Keep recovery in this runTask call so the visible verification
			// window continues to occupy the task's scheduler slot.
			adapter.recoverMyreadingVerification(m, id, task)
		} else if ok && current.State != TaskPaused {
			daemon.Close()
			if cleanupErr := cleanupTaskBrowserProfiles(id, adapter.name); cleanupErr != nil {
				log.Printf("%s browser profile cleanup failed id=%d err=%v", adapter.label, id, cleanupErr)
			}
		}
		return
	}
	daemon.Close()
	if cleanupErr := cleanupTaskBrowserProfiles(id, adapter.name); cleanupErr != nil {
		log.Printf("%s browser profile cleanup failed id=%d err=%v", adapter.label, id, cleanupErr)
	}
}
