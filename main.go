package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/url"
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
)

type TaskState string

const (
	TaskQueued              TaskState = "queued"
	TaskRunning             TaskState = "running"
	TaskWaitingVerification TaskState = "waiting_verification"
	TaskPaused              TaskState = "paused"
	TaskDone                TaskState = "done"
	TaskError               TaskState = "error"
)

type Task struct {
	ID            int       `json:"id"`
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	DownloadRoot  string    `json:"downloadRoot"`
	OutputDir     string    `json:"outputDir"`
	Headless      bool      `json:"headless"`
	HttpOnly      bool      `json:"httpOnly"`
	ThumbnailPath string    `json:"thumbnailPath"`
	Worker        string    `json:"worker"`
	WorkerSource  string    `json:"workerSource"`
	State         TaskState `json:"state"`
	Detail        string    `json:"detail"`
	SpeedText     string    `json:"speedText,omitempty"`
	Percent       float64   `json:"percent"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type AppState struct {
	Tasks       []*Task       `json:"tasks"`
	Queue       int           `json:"queueLen"`
	Concurrency int           `json:"concurrency"`
	Counts      Counts        `json:"counts"`
	Logs        []string      `json:"logs"`
	Adblock     AdblockStatus `json:"adblock"`
	NextID      int           `json:"nextId"`
}

type Counts struct {
	Queued  int `json:"queued"`
	Running int `json:"running"`
	Paused  int `json:"paused"`
	Done    int `json:"done"`
	Error   int `json:"error"`
}

type ImportHistoryResult struct {
	Imported int
	Skipped  int
	Errors   int
	ErrorIDs []int
}

type AdblockStatus struct {
	Source       string `json:"source"`
	URL          string `json:"url"`
	Date         string `json:"date"`
	UpdatedAt    string `json:"updatedAt"`
	PatternCount int    `json:"patternCount"`
	CacheExists  bool   `json:"cacheExists"`
	CacheMTime   string `json:"cacheMtime"`
	CacheFile    string `json:"cacheFile"`
	Ready        bool   `json:"ready"`
}

type LogBuffer struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial string
}

type refreshHookFunc func()

func NewLogBuffer(max int) *LogBuffer {
	if max <= 0 {
		max = 200
	}
	return &LogBuffer{max: max}
}

var appLogs = NewLogBuffer(300)
var appLogFile *os.File
var appRefreshHook atomic.Pointer[refreshHookFunc]
var bootstrapTraceMu sync.Mutex

func setAppRefreshHook(fn func()) {
	if fn == nil {
		appRefreshHook.Store(nil)
		return
	}
	hook := refreshHookFunc(fn)
	appRefreshHook.Store(&hook)
}

func signalAppRefresh() {
	hook := appRefreshHook.Load()
	if hook == nil || *hook == nil {
		return
	}
	(*hook)()
}

func bootstrapTrace(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	path := filepath.Join(projectRootDir(), ".tmp", "bootstrap.log")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	bootstrapTraceMu.Lock()
	defer bootstrapTraceMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, time.Now().Format("2006/01/02 15:04:05.000"), msg)
}

func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	changed := false
	b.partial += string(p)
	for {
		idx := strings.IndexByte(b.partial, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(b.partial[:idx], "\r")
		if line != "" {
			b.lines = append(b.lines, line)
			if len(b.lines) > b.max {
				b.lines = b.lines[len(b.lines)-b.max:]
			}
			changed = true
		}
		b.partial = b.partial[idx+1:]
	}
	b.mu.Unlock()
	if changed {
		signalAppRefresh()
	}
	return len(p), nil
}

func (b *LogBuffer) Snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make([]string, 0, len(b.lines)+1)
	result = append(result, b.lines...)
	if tail := strings.TrimSpace(b.partial); tail != "" {
		result = append(result, tail)
	}
	return result
}

type Manager struct {
	mu            sync.Mutex
	quitOnce      sync.Once
	nextID        int
	concurrency   int
	running       int
	tasks         map[int]*Task
	order         []int
	queue         chan int
	pending       []int
	activeWorkers map[int]activeTaskWorker
	stopped       chan struct{}
	wake          chan struct{}
}

func NewManager() *Manager {
	m := &Manager{
		nextID:        1,
		concurrency:   1,
		tasks:         make(map[int]*Task),
		queue:         make(chan int, 256),
		pending:       make([]int, 0, 32),
		activeWorkers: make(map[int]activeTaskWorker),
		stopped:       make(chan struct{}),
		wake:          make(chan struct{}, 1),
	}
	return m
}

type activeTaskWorker interface {
	Close()
}

func (m *Manager) Start() {
	go m.workerLoop()
}

func (m *Manager) RestoreFromPersistedState(state *persistedAppState) {
	if state == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.tasks = make(map[int]*Task)
	m.order = m.order[:0]
	maxID := 0
	for _, task := range state.Tasks {
		if task == nil {
			continue
		}
		copyTask := *task
		copyTask.URL = strings.TrimSpace(copyTask.URL)
		copyTask.Title = strings.TrimSpace(copyTask.Title)
		copyTask.DownloadRoot = strings.TrimSpace(copyTask.DownloadRoot)
		copyTask.Detail = strings.TrimSpace(copyTask.Detail)
		copyTask.SpeedText = strings.TrimSpace(copyTask.SpeedText)
		switch copyTask.State {
		case TaskQueued, TaskRunning, TaskWaitingVerification:
			copyTask.State = TaskPaused
			if copyTask.Detail == "" || strings.EqualFold(copyTask.Detail, "queued") || strings.EqualFold(copyTask.Detail, "starting task") {
				copyTask.Detail = "restored after restart"
			}
		}
		if copyTask.CreatedAt.IsZero() {
			copyTask.CreatedAt = time.Now()
		}
		if copyTask.UpdatedAt.IsZero() {
			copyTask.UpdatedAt = copyTask.CreatedAt
		}
		copyTask.Headless = defaultHeadlessForTask(copyTask.URL, copyTask.Worker)
		m.tasks[copyTask.ID] = &copyTask
		m.order = append(m.order, copyTask.ID)
		if copyTask.ID > maxID {
			maxID = copyTask.ID
		}
	}

	nextID := state.NextTaskID
	if nextID <= maxID {
		nextID = maxID + 1
	}
	if nextID <= 0 {
		nextID = 1
	}
	m.nextID = nextID
	if state.Concurrency > 0 {
		m.concurrency = clampConcurrency(state.Concurrency)
	}
}

func (m *Manager) AddTask(url, title, downloadRoot string, headless bool, httpOnly bool) *Task {
	m.mu.Lock()
	id := m.nextID
	m.nextID++
	now := time.Now()
	worker := ""
	route := prefilterSiteRoute(url)
	if route.ok {
		worker = route.adapter.name
	}
	task := &Task{
		ID:           id,
		URL:          strings.TrimSpace(url),
		Title:        strings.TrimSpace(title),
		DownloadRoot: strings.TrimSpace(downloadRoot),
		Headless:     headless,
		HttpOnly:     httpOnly,
		Worker:       worker,
		WorkerSource: route.source,
		State:        TaskQueued,
		Detail:       "queued",
		SpeedText:    "",
		Percent:      0,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	m.tasks[id] = task
	m.order = append([]int{id}, m.order...)
	m.mu.Unlock()

	if task.Worker != "" {
		log.Printf("task queued id=%d url=%s worker=%s route=%s downloadRoot=%s headless=%v httpOnly=%v", id, task.URL, task.Worker, route.source, task.DownloadRoot, task.Headless, task.HttpOnly)
	} else {
		log.Printf("task queued id=%d url=%s worker=auto route=%s downloadRoot=%s headless=%v httpOnly=%v", id, task.URL, route.source, task.DownloadRoot, task.Headless, task.HttpOnly)
	}
	signalAppRefresh()
	select {
	case m.queue <- id:
	default:
		go func() { m.queue <- id }()
	}
	m.signalScheduler()
	return task
}

func defaultHeadlessForTask(rawURL, worker string) bool {
	route := resolveSiteRoute(rawURL, worker)
	return defaultHeadlessForRoute(route, rawURL)
}

func defaultHeadlessForRoute(route siteRoute, rawURL string) bool {
	switch route.adapter.name {
	case "myreadingmanga":
		return true
	default:
		_ = rawURL
		return true
	}
}

func canonicalTaskURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return strings.ToLower(trimmed)
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	path := strings.TrimSuffix(parsed.Path, "/")
	if path == "" {
		path = "/"
	}
	parsed.Path = path
	return parsed.String()
}

func (m *Manager) FindTaskByURL(rawURL string) *Task {
	key := canonicalTaskURL(rawURL)
	if key == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.order) - 1; i >= 0; i-- {
		id := m.order[i]
		task, ok := m.tasks[id]
		if !ok || task == nil {
			continue
		}
		if canonicalTaskURL(task.URL) == key {
			copyTask := *task
			return &copyTask
		}
	}
	return nil
}

func taskWorkerTitle(task Task) string {
	title := strings.TrimSpace(task.Title)
	if title == "" || title == "pending" {
		return ""
	}
	if strings.EqualFold(title, strings.TrimSpace(task.URL)) {
		return ""
	}
	lowered := strings.ToLower(title)
	if strings.HasPrefix(lowered, "http://") || strings.HasPrefix(lowered, "https://") {
		return ""
	}
	return title
}

func resultString(result map[string]any, key string) string {
	if len(result) == 0 {
		return ""
	}
	raw, ok := result[key]
	if !ok || raw == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(raw))
	if text == "" || strings.EqualFold(text, "<nil>") {
		return ""
	}
	return text
}

func titleFromOutputDir(outputDir string) string {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return ""
	}
	base := strings.TrimSpace(filepath.Base(filepath.Clean(outputDir)))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

func (m *Manager) ClearCompleted() {
	m.mu.Lock()
	defer m.mu.Unlock()

	nextTasks := make(map[int]*Task)
	var nextOrder []int
	for _, id := range m.order {
		task, ok := m.tasks[id]
		if !ok {
			continue
		}
		if task.State == TaskDone || task.State == TaskError {
			continue
		}
		nextTasks[id] = task
		nextOrder = append(nextOrder, id)
	}
	m.tasks = nextTasks
	m.order = nextOrder
	log.Printf("clear completed tasks remaining=%d", len(nextOrder))
	signalAppRefresh()
}

func (m *Manager) ImportHistoryTasks(tasks []*Task) ImportHistoryResult {
	var result ImportHistoryResult
	if len(tasks) == 0 {
		return result
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	existingURLs := make(map[string]bool, len(m.tasks))
	for _, task := range m.tasks {
		if task == nil {
			continue
		}
		if key := canonicalTaskURL(task.URL); key != "" {
			existingURLs[key] = true
		}
	}

	usedIDs := make(map[int]bool, len(m.tasks)+len(tasks))
	for id := range m.tasks {
		usedIDs[id] = true
	}

	importedIDs := make([]int, 0, len(tasks))
	now := time.Now()
	for _, task := range tasks {
		if task == nil {
			result.Skipped++
			continue
		}
		copyTask := *task
		copyTask.URL = strings.TrimSpace(copyTask.URL)
		if copyTask.URL == "" {
			result.Skipped++
			continue
		}
		route := resolveSiteRoute(copyTask.URL, "")
		if !route.ok || route.adapter.name != "myreadingmanga" {
			result.Skipped++
			continue
		}
		key := canonicalTaskURL(copyTask.URL)
		if key != "" && existingURLs[key] {
			result.Skipped++
			continue
		}

		if copyTask.ID <= 0 || usedIDs[copyTask.ID] {
			copyTask.ID = m.nextID
			for usedIDs[copyTask.ID] {
				copyTask.ID++
			}
		}
		usedIDs[copyTask.ID] = true
		if copyTask.ID >= m.nextID {
			m.nextID = copyTask.ID + 1
		}
		if key != "" {
			existingURLs[key] = true
		}

		copyTask.Title = cleanImportedHistoryText(copyTask.Title)
		copyTask.DownloadRoot = cleanImportedHistoryText(copyTask.DownloadRoot)
		copyTask.OutputDir = cleanImportedHistoryText(copyTask.OutputDir)
		copyTask.Detail = cleanImportedHistoryText(copyTask.Detail)
		copyTask.SpeedText = ""
		copyTask.ThumbnailPath = strings.TrimSpace(copyTask.ThumbnailPath)
		copyTask.Worker = "myreadingmanga"
		copyTask.WorkerSource = route.source
		copyTask.Headless = defaultHeadlessForTask(copyTask.URL, copyTask.Worker)
		copyTask.HttpOnly = false
		switch copyTask.State {
		case TaskDone:
			copyTask.Percent = 1
		case TaskError:
			result.Errors++
			result.ErrorIDs = append(result.ErrorIDs, copyTask.ID)
		case TaskQueued, TaskRunning, TaskWaitingVerification:
			copyTask.State = TaskPaused
			if copyTask.Detail == "" || strings.EqualFold(copyTask.Detail, "queued") || strings.EqualFold(copyTask.Detail, "starting task") {
				copyTask.Detail = "imported history"
			}
		case TaskPaused:
		default:
			copyTask.State = TaskPaused
			if strings.TrimSpace(copyTask.Detail) == "" {
				copyTask.Detail = "imported history"
			}
		}
		if copyTask.CreatedAt.IsZero() {
			copyTask.CreatedAt = copyTask.UpdatedAt
		}
		if copyTask.CreatedAt.IsZero() {
			copyTask.CreatedAt = now
		}
		if copyTask.UpdatedAt.IsZero() {
			copyTask.UpdatedAt = copyTask.CreatedAt
		}

		m.tasks[copyTask.ID] = &copyTask
		importedIDs = append(importedIDs, copyTask.ID)
		result.Imported++
	}
	if len(importedIDs) > 0 {
		m.order = append(importedIDs, m.order...)
		log.Printf("imported history tasks imported=%d skipped=%d errors=%d", result.Imported, result.Skipped, result.Errors)
		signalAppRefresh()
	}
	return result
}

func cleanImportedHistoryText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	replacements := map[string]string{
		"\u7039\u5c7e\u579a":             "完成",
		"\u940e\u7470\u672c\u9368?":      "完成",
		"\u940e\u7470\u672c\u9368\ufffd": "完成",
	}
	for old, replacement := range replacements {
		text = strings.ReplaceAll(text, old, replacement)
	}
	return strings.TrimSpace(text)
}

func (m *Manager) CloseActiveWorkers() {
	m.mu.Lock()
	daemons := make([]activeTaskWorker, 0, len(m.activeWorkers))
	for _, daemon := range m.activeWorkers {
		daemons = append(daemons, daemon)
	}
	m.activeWorkers = make(map[int]activeTaskWorker)
	m.mu.Unlock()
	for _, daemon := range daemons {
		if daemon != nil {
			daemon.Close()
		}
	}
}

func (m *Manager) registerActiveWorker(id int, daemon activeTaskWorker) {
	if daemon == nil {
		return
	}
	m.mu.Lock()
	m.activeWorkers[id] = daemon
	m.mu.Unlock()
}

func (m *Manager) unregisterActiveWorker(id int, daemon activeTaskWorker) {
	m.mu.Lock()
	if current := m.activeWorkers[id]; current == daemon {
		delete(m.activeWorkers, id)
	}
	m.mu.Unlock()
}

func (m *Manager) Snapshot() AppState {
	m.mu.Lock()
	defer m.mu.Unlock()

	tasks := make([]*Task, 0, len(m.order))
	var counts Counts
	for _, id := range m.order {
		task, ok := m.tasks[id]
		if !ok {
			continue
		}
		copyTask := *task
		tasks = append(tasks, &copyTask)
		switch task.State {
		case TaskQueued:
			counts.Queued++
		case TaskRunning:
			counts.Running++
		case TaskPaused, TaskWaitingVerification:
			counts.Paused++
		case TaskDone:
			counts.Done++
		case TaskError:
			counts.Error++
		}
	}

	return AppState{
		Tasks:       tasks,
		Queue:       len(m.queue) + len(m.pending),
		Concurrency: m.concurrency,
		Counts:      counts,
		Logs:        appLogs.Snapshot(),
		Adblock:     loadAdblockStatus(),
		NextID:      m.nextID,
	}
}

func loadAdblockStatus() AdblockStatus {
	cacheDir := resourcePath("runtime", "adblock")
	statePath := filepath.Join(cacheDir, "state.json")
	cachePath := filepath.Join(cacheDir, "AWAvenue-Ads-Rule.txt")

	status := AdblockStatus{
		Source:    "AWAvenue-Ads-Rule",
		URL:       "https://raw.githubusercontent.com/TG-Twilight/AWAvenue-Ads-Rule/main/AWAvenue-Ads-Rule.txt",
		CacheFile: cachePath,
	}

	if data, err := os.ReadFile(statePath); err == nil {
		var payload struct {
			Source       string `json:"source"`
			URL          string `json:"url"`
			Date         string `json:"date"`
			UpdatedAt    string `json:"updated_at"`
			PatternCount int    `json:"pattern_count"`
			CacheFile    string `json:"cache_file"`
		}
		if json.Unmarshal(data, &payload) == nil {
			if payload.Source != "" {
				status.Source = payload.Source
			}
			if payload.URL != "" {
				status.URL = payload.URL
			}
			status.Date = payload.Date
			status.UpdatedAt = payload.UpdatedAt
			status.PatternCount = payload.PatternCount
			if payload.CacheFile != "" {
				status.CacheFile = payload.CacheFile
			}
		}
	}

	if info, err := os.Stat(cachePath); err == nil {
		status.CacheExists = true
		status.PatternCount = max(status.PatternCount, countRulesInFile(cachePath))
		status.CacheMTime = info.ModTime().Format(time.RFC3339)
	}

	status.Ready = status.PatternCount > 0 || status.CacheExists
	return status
}

func countRulesInFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "@@") {
			continue
		}
		count++
	}
	return count
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m *Manager) workerLoop() {
	for {
		m.mu.Lock()
		canStart := len(m.pending) > 0 && m.running < m.concurrency
		var id int
		if canStart {
			id = m.pending[0]
			m.pending = m.pending[1:]
			m.running++
			m.mu.Unlock()
			go m.runTaskAsync(id)
			continue
		}
		m.mu.Unlock()

		select {
		case <-m.stopped:
			return
		case taskID := <-m.queue:
			m.mu.Lock()
			if task, ok := m.tasks[taskID]; ok && task.State == TaskQueued {
				m.pending = append(m.pending, taskID)
			}
			m.mu.Unlock()
		case <-m.wake:
		}
	}
}

func (m *Manager) runTaskAsync(id int) {
	defer func() {
		m.mu.Lock()
		if m.running > 0 {
			m.running--
		}
		m.mu.Unlock()
		m.signalScheduler()
		signalAppRefresh()
	}()
	m.runTask(id)
}

func (m *Manager) signalScheduler() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func clampConcurrency(n int) int {
	if n < 1 {
		return 1
	}
	if n > 16 {
		return 16
	}
	return n
}

func (m *Manager) SetConcurrency(n int) int {
	n = clampConcurrency(n)
	m.mu.Lock()
	m.concurrency = n
	m.mu.Unlock()
	m.signalScheduler()
	signalAppRefresh()
	return n
}

func (m *Manager) Concurrency() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.concurrency
}

func (m *Manager) runTask(id int) {
	task, ok := m.taskCopy(id)
	if !ok {
		return
	}
	if task.State == TaskPaused || task.State == TaskWaitingVerification || task.State == TaskDone || task.State == TaskError {
		return
	}

	if route := resolveTaskRoute(task); route.ok {
		adapter := route.adapter
		m.runSiteTask(id, task, adapter)
		return
	}

	m.setState(id, TaskError, 0, "unsupported site: only myreadingmanga.info is configured")
}

func resolveTaskRoute(task Task) siteRoute {
	return resolveSiteRoute(task.URL, "")
}

func (m *Manager) isStopped(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[id]
	if !ok {
		return true
	}
	// WaitingVerification is an active worker state: the visible browser must
	// stay alive while the user completes the challenge.
	return task.State == TaskPaused || task.State == TaskError || task.State == TaskDone
}

func canTaskAction(task *Task, action string) bool {
	if task == nil {
		return false
	}
	switch action {
	case "pause":
		return task.State == TaskQueued || task.State == TaskRunning || task.State == TaskWaitingVerification
	case "resume":
		return task.State == TaskPaused
	case "retry":
		return task.State == TaskPaused || task.State == TaskDone || task.State == TaskError
	case "cancel":
		return task.State == TaskQueued || task.State == TaskRunning || task.State == TaskWaitingVerification || task.State == TaskPaused
	case "delete":
		return true
	default:
		return false
	}
}

func (m *Manager) taskCopy(id int) (Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[id]
	if !ok {
		return Task{}, false
	}
	return *task, true
}

func (m *Manager) setState(id int, state TaskState, percent float64, detail string) bool {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	if task.State == TaskPaused {
		m.mu.Unlock()
		return false
	}
	if task.State == TaskDone || task.State == TaskError {
		m.mu.Unlock()
		return false
	}
	task.State = state
	task.Percent = percent
	task.Detail = detail
	if state != TaskRunning {
		task.SpeedText = ""
	}
	task.UpdatedAt = time.Now()
	m.mu.Unlock()
	log.Printf("task state id=%d state=%s percent=%.2f detail=%s", id, state, percent, detail)
	signalAppRefresh()
	return true
}

func (m *Manager) setStateIfCurrent(id int, current TaskState, state TaskState, percent float64, detail string) bool {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok || task.State != current {
		m.mu.Unlock()
		return false
	}
	task.State = state
	task.Percent = percent
	task.Detail = detail
	if state != TaskRunning {
		task.SpeedText = ""
	}
	task.UpdatedAt = time.Now()
	m.mu.Unlock()
	log.Printf("task state id=%d state=%s percent=%.2f detail=%s", id, state, percent, detail)
	signalAppRefresh()
	return true
}

func (m *Manager) setTaskSpeed(id int, speedText string) bool {
	speedText = strings.TrimSpace(speedText)
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	task.SpeedText = speedText
	task.UpdatedAt = time.Now()
	m.mu.Unlock()
	signalAppRefresh()
	return true
}

func (m *Manager) setTitle(id int, title string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	task.Title = title
	task.UpdatedAt = time.Now()
	m.mu.Unlock()
	log.Printf("task title id=%d title=%s", id, title)
	signalAppRefresh()
	return true
}

func (m *Manager) setOutputDir(id int, outputDir string) bool {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return false
	}
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	task.OutputDir = outputDir
	task.UpdatedAt = time.Now()
	m.mu.Unlock()
	log.Printf("task output dir id=%d output=%s", id, outputDir)
	signalAppRefresh()
	return true
}

func (m *Manager) setThumbnailPath(id int, thumbnailPath string) bool {
	thumbnailPath = portableResourcePath(thumbnailPath)
	if thumbnailPath == "" {
		return false
	}
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	task.ThumbnailPath = thumbnailPath
	task.UpdatedAt = time.Now()
	m.mu.Unlock()
	log.Printf("task thumbnail id=%d thumb=%s", id, thumbnailPath)
	signalAppRefresh()
	return true
}

func (m *Manager) Pause(id int) bool {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok || task.State == TaskPaused || task.State == TaskDone || task.State == TaskError {
		m.mu.Unlock()
		return false
	}
	daemon := m.activeWorkers[id]
	if daemon != nil {
		delete(m.activeWorkers, id)
	}
	task.State = TaskPaused
	task.Detail = "paused"
	task.SpeedText = ""
	task.UpdatedAt = time.Now()
	m.mu.Unlock()
	if daemon != nil {
		daemon.Close()
	}
	log.Printf("task paused id=%d", id)
	signalAppRefresh()
	return true
}

func (m *Manager) Resume(id int) bool {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok || task.State != TaskPaused {
		m.mu.Unlock()
		return false
	}
	task.State = TaskQueued
	task.Detail = "resumed"
	task.SpeedText = ""
	task.UpdatedAt = time.Now()
	m.mu.Unlock()

	log.Printf("task resumed id=%d", id)
	signalAppRefresh()
	select {
	case m.queue <- id:
	default:
		go func() { m.queue <- id }()
	}
	m.signalScheduler()
	return true
}

func (m *Manager) Retry(id int) bool {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	if task.State == TaskQueued || task.State == TaskRunning || task.State == TaskWaitingVerification {
		m.mu.Unlock()
		return false
	}
	daemon := m.activeWorkers[id]
	if daemon != nil {
		delete(m.activeWorkers, id)
	}
	worker := task.Worker
	m.pending = removeTaskID(m.pending, id)
	m.mu.Unlock()

	if daemon != nil {
		daemon.Close()
	}
	if err := cleanupTaskBrowserProfiles(id, worker); err != nil {
		log.Printf("task retry cleanup failed id=%d worker=%s err=%v", id, worker, err)
	}

	m.mu.Lock()
	task, ok = m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	task.State = TaskQueued
	task.Percent = 0
	task.Detail = "retry queued"
	task.SpeedText = ""
	task.UpdatedAt = time.Now()
	m.mu.Unlock()

	log.Printf("task retry queued id=%d", id)
	signalAppRefresh()
	select {
	case m.queue <- id:
	default:
		go func() { m.queue <- id }()
	}
	m.signalScheduler()
	return true
}

func (m *Manager) Cancel(id int) bool {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	daemon := m.activeWorkers[id]
	if daemon != nil {
		delete(m.activeWorkers, id)
	}
	if task.State == TaskDone || task.State == TaskError {
		m.mu.Unlock()
		if daemon != nil {
			daemon.Close()
		}
		return false
	}
	task.State = TaskError
	task.Detail = "cancelled"
	task.UpdatedAt = time.Now()
	task.Percent = 0
	task.SpeedText = ""
	m.mu.Unlock()
	if daemon != nil {
		daemon.Close()
	}
	if err := cleanupTaskBrowserProfiles(id, task.Worker); err != nil {
		log.Printf("task cancel cleanup failed id=%d worker=%s err=%v", id, task.Worker, err)
	}
	log.Printf("task cancelled id=%d", id)
	signalAppRefresh()
	return true
}

func (m *Manager) Delete(id int) bool {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	daemon := m.activeWorkers[id]
	if daemon != nil {
		delete(m.activeWorkers, id)
	}
	delete(m.tasks, id)
	m.order = removeTaskID(m.order, id)
	m.pending = removeTaskID(m.pending, id)
	m.mu.Unlock()
	if daemon != nil {
		daemon.Close()
	}
	if err := cleanupTaskBrowserProfiles(id, task.Worker); err != nil {
		log.Printf("task delete cleanup failed id=%d worker=%s err=%v", id, task.Worker, err)
	}
	log.Printf("task deleted id=%d", id)
	signalAppRefresh()
	m.signalScheduler()
	return true
}

func (m *Manager) applyTaskAction(id int, action string) bool {
	switch action {
	case "pause":
		return m.Pause(id)
	case "resume":
		return m.Resume(id)
	case "retry":
		return m.Retry(id)
	case "cancel":
		return m.Cancel(id)
	case "delete":
		return m.Delete(id)
	default:
		return false
	}
}

func (m *Manager) ApplyTaskActionBatch(ids []int, action string) (okCount int) {
	for _, id := range ids {
		if m.applyTaskAction(id, action) {
			okCount++
		}
	}
	return okCount
}

func (m *Manager) Quit() {
	m.quitOnce.Do(func() {
		m.CloseActiveWorkers()
		close(m.stopped)
	})
}

func setupLogging() {
	bootstrapTrace("setupLogging start")
	logDir := filepath.Join(projectRootDir(), "logs")
	_ = os.MkdirAll(logDir, 0o755)
	// A windowsgui binary has no valid stdout handle. io.MultiWriter stops at
	// the first failed writer, so putting os.Stdout first silently prevented
	// both the in-app buffer and the file log from receiving any entry.
	writers := []io.Writer{appLogs}
	if file, err := os.OpenFile(filepath.Join(logDir, "comic_downloader.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		appLogFile = file
		writers = append(writers, file)
		bootstrapTrace("setupLogging log file ready: %s", file.Name())
	} else {
		bootstrapTrace("setupLogging log file open failed: %v", err)
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(io.MultiWriter(writers...))
	bootstrapTrace("setupLogging done")
}

func main() {
	bootstrapTrace("main start")
	setupLogging()
	logStartupDiagnostics()
	bootstrapTrace("after diagnostics")
	rand.Seed(time.Now().UnixNano())

	persistedState, err := loadPersistedAppState()
	if err != nil {
		log.Printf("state load failed: %v", err)
	}
	if persistedState != nil {
		setChromiumPath(strings.TrimSpace(persistedState.UI.ChromiumPath))
	}
	manager := NewManager()
	defer manager.Quit()
	manager.RestoreFromPersistedState(persistedState)
	manager.Start()

	if err := runNativeUI(manager, persistedState); err != nil {
		bootstrapTrace("runNativeUI error: %v", err)
		log.Fatalf("ui: %v", err)
	}
	bootstrapTrace("main exit clean")
}

func logStartupDiagnostics() {
	wd, _ := os.Getwd()
	exe, _ := os.Executable()
	log.Printf("startup cwd=%s exe=%s", wd, exe)
	log.Printf("startup cmdline=%s", strings.Join(os.Args, " "))
	log.Printf("startup projectRoot=%s runtimeRoot=%s", projectRootDir(), runtimeRootDir())
	log.Printf("startup logFile=%s", filepath.Join(projectRootDir(), "logs", "comic_downloader.log"))
	log.Printf("startup python bundled=%s", bundledPythonExecutable())
	log.Printf("startup chromium bundled=%s", bundledChromiumPath())
	log.Printf("startup chromium active=%s", currentChromiumPath())
	log.Printf("startup python candidates=%v", pythonBinCandidates())
	log.Printf("startup myreading engine=%s config=%s", loadMyreadingConfig().Engine, resourcePath("config.json"))
}

func taskHost(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if u.Hostname() == "" && !strings.Contains(rawURL, "://") {
		if parsed, parseErr := url.Parse("https://" + rawURL); parseErr == nil {
			return strings.ToLower(parsed.Hostname())
		}
	}
	return strings.ToLower(u.Hostname())
}

func intFromAny(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		i, _ := t.Int64()
		return int(i)
	case string:
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(t), "%d", &n)
		return n
	default:
		return 0
	}
}

func runPythonWorkerJSON(script string, payload map[string]any) (map[string]any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	pythonBins := pythonBinCandidates()
	var lastErr error
	for _, bin := range pythonBins {
		if bin == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		cmd := exec.CommandContext(ctx, bin, script, "--input-json", string(data))
		cmd.SysProcAttr = hiddenSysProcAttr()
		cmd.Dir = projectRootDir()
		cmd.Env = append(os.Environ(),
			"PYTHONUTF8=1",
			"PYTHONIOENCODING=utf-8",
		)
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("%s stdout pipe: %w", bin, err)
			continue
		}
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("%s stderr pipe: %w", bin, err)
			continue
		}
		if err := cmd.Start(); err != nil {
			cancel()
			lastErr = fmt.Errorf("%s: %w", bin, err)
			continue
		}

		stderrDone := make(chan struct{})
		go func() {
			defer close(stderrDone)
			scanner := bufio.NewScanner(stderrPipe)
			scanner.Buffer(make([]byte, 1024), 1024*1024)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line != "" {
					log.Printf("[worker] %s", line)
				}
			}
			if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
				log.Printf("[worker] stderr scan error: %v", err)
			}
		}()

		out, readErr := io.ReadAll(stdoutPipe)
		waitErr := cmd.Wait()
		cancel()
		<-stderrDone

		if readErr != nil {
			lastErr = fmt.Errorf("%s stdout read: %w", bin, readErr)
			continue
		}
		if waitErr != nil {
			lastErr = fmt.Errorf("%s: %w; output=%s", bin, waitErr, strings.TrimSpace(string(out)))
			continue
		}
		var result map[string]any
		if err := json.Unmarshal(out, &result); err != nil {
			trimmed := strings.TrimSpace(string(out))
			start := strings.LastIndex(trimmed, "{")
			if start >= 0 {
				if err2 := json.Unmarshal([]byte(trimmed[start:]), &result); err2 == nil {
					return result, nil
				}
			}
			return nil, fmt.Errorf("parse worker output: %w; output=%s", err, trimmed)
		}
		return result, nil
	}
	if lastErr == nil {
		lastErr = errors.New("python worker failed")
	}
	return nil, lastErr
}

type siteWorkerDaemon struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	stderrDone chan struct{}
	bin        string
	label      string
	progress   atomic.Pointer[workerProgressHook]
}

type workerProgressHook func(percent float64, detail string)

func (d *siteWorkerDaemon) setProgressHook(fn workerProgressHook) {
	if fn == nil {
		d.progress.Store(nil)
		return
	}
	hook := fn
	d.progress.Store(&hook)
}

func (d *siteWorkerDaemon) clearProgressHook() {
	d.progress.Store(nil)
}

func (d *siteWorkerDaemon) Run(script string, payload map[string]any, browserPath, runtimeRoot string, headless bool, browserUserDataPath string) (map[string]any, error) {
	defer d.clearProgressHook()
	for attempt := 0; attempt < 2; attempt++ {
		if err := d.ensureStarted(script, browserPath, runtimeRoot, headless, browserUserDataPath); err != nil {
			return nil, err
		}

		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		stdin := d.stdin
		stdout := d.stdout
		d.mu.Unlock()
		if stdin == nil || stdout == nil {
			return nil, errors.New("worker daemon unavailable")
		}
		if _, err := stdin.Write(append(data, '\n')); err != nil {
			d.Close()
			if attempt == 0 {
				continue
			}
			return nil, err
		}

		result, err := d.readWorkerResult(stdout)
		if err != nil {
			d.Close()
			if attempt == 0 {
				continue
			}
			return nil, err
		}
		return result, nil
	}
	return nil, errors.New("worker daemon unavailable")
}

func (d *siteWorkerDaemon) ensureStarted(script, browserPath, runtimeRoot string, headless bool, browserUserDataPath string) error {
	bin := ""
	for _, candidate := range pythonBinCandidates() {
		if candidate != "" {
			bin = candidate
			break
		}
	}
	if bin == "" {
		return errors.New("no python interpreter with DrissionPage available")
	}
	d.mu.Lock()
	if d.cmd != nil {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	label := d.label
	if label == "" {
		label = "worker"
	}
	log.Printf("%s daemon python selected: %s", label, bin)

	if browserUserDataPath != "" {
		log.Printf("%s daemon launching: script=%s browserPath=%s headless=%v browserUserDataPath=%s", label, script, browserPath, headless, browserUserDataPath)
	} else {
		log.Printf("%s daemon launching: script=%s browserPath=%s headless=%v", label, script, browserPath, headless)
	}
	args := []string{script, "--serve"}
	if browserPath != "" {
		args = append(args, "--browser-path", browserPath)
	}
	if runtimeRoot != "" {
		args = append(args, "--runtime-root", runtimeRoot)
	}
	if headless {
		args = append(args, "--headless")
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = projectRootDir()
	cmd.Env = append(os.Environ(),
		"PYTHONUTF8=1",
		"PYTHONIOENCODING=utf-8",
	)
	if browserUserDataPath != "" {
		cmd.Env = append(cmd.Env, "COMIC_DOWNLOADER_BROWSER_USER_DATA_PATH="+browserUserDataPath)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000,
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		return err
	}

	var stderrMu sync.Mutex
	stderrLines := make([]string, 0, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				stderrMu.Lock()
				stderrLines = append(stderrLines, line)
				stderrMu.Unlock()
				d.emitProgressFromLog(line)
				log.Printf("[%s-worker] %s", label, line)
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			log.Printf("[%s-worker] stderr scan error: %v", label, err)
		}
	}()

	reader := bufio.NewReader(stdoutPipe)
	d.mu.Lock()
	if d.cmd != nil {
		d.mu.Unlock()
		_ = stdinPipe.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return errors.New("worker daemon already started")
	}
	d.bin = bin
	d.cmd = cmd
	d.stdin = stdinPipe
	d.stdout = reader
	d.stderrDone = done
	d.mu.Unlock()
	log.Printf("%s daemon started bin=%s script=%s", label, bin, script)
	return nil
}

func (d *siteWorkerDaemon) readWorkerResult(stdout *bufio.Reader) (map[string]any, error) {
	for {
		line, err := stdout.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		result := make(map[string]any)
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			continue
		}
		if ready, _ := result["ready"].(bool); ready {
			continue
		}
		return result, nil
	}
}

func (d *siteWorkerDaemon) emitProgressFromLog(line string) {
	hook := workerProgressHook(nil)
	if ptr := d.progress.Load(); ptr != nil && *ptr != nil {
		hook = *ptr
	}
	if hook == nil {
		return
	}
	percent, detail, ok := parseWorkerProgressLine(line)
	if !ok {
		return
	}
	hook(percent, detail)
}

func parseWorkerProgressLine(line string) (float64, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, "", false
	}
	if percent, detail, ok := parseWorkerTraceProgressLine(line); ok {
		return percent, detail, true
	}
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return 0, "", false
	}
	if fields[0] != "reader" {
		return 0, "", false
	}
	if fields[1] != "saved" && fields[1] != "failed" {
		return 0, "", false
	}
	token := strings.TrimSuffix(fields[2], ":")
	parts := strings.Split(token, "/")
	if len(parts) != 2 {
		return 0, "", false
	}
	done, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	total, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || total <= 0 {
		return 0, "", false
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	frac := float64(done) / float64(total)
	percent := 0.12 + 0.84*frac
	if percent < 0.12 {
		percent = 0.12
	}
	if percent > 0.96 {
		percent = 0.96
	}
	detail := fmt.Sprintf("downloading images %d/%d", done, total)
	return percent, detail, true
}

func parseWorkerTraceProgressLine(line string) (float64, string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.HasPrefix(normalized, "flow content snapshot:"):
		if strings.Contains(normalized, "blocked=true") {
			return 0.08, "checking verification", true
		}
		return 0.12, "verification checked", true
	case strings.HasPrefix(normalized, "flow verification poll:"):
		return 0.10, "waiting for verification", true
	case strings.HasPrefix(normalized, "flow verification cleared"):
		return 0.16, "verification cleared", true
	case strings.HasPrefix(normalized, "summary page get start"):
		return 0.12, "opening summary page", true
	case strings.HasPrefix(normalized, "summary page get done"):
		return 0.16, "summary page loaded", true
	case strings.HasPrefix(normalized, "summary page html captured"):
		return 0.20, "summary page captured", true
	case strings.HasPrefix(normalized, "article page loaded"):
		return 0.18, "article page loaded", true
	case strings.HasPrefix(normalized, "article page parsed"):
		return 0.22, "article page parsed", true
	case strings.HasPrefix(normalized, "reader page loaded"):
		return 0.26, "reader page loaded", true
	case strings.HasPrefix(normalized, "reader page after 100%"):
		return 0.32, "reader page expanded", true
	case strings.HasPrefix(normalized, "reader image scan start"):
		return 0.34, "scanning reader images", true
	case strings.HasPrefix(normalized, "reader warmup start"):
		return 0.36, "warming reader images", true
	case strings.HasPrefix(normalized, "reader warmup round"):
		round, total := parseCounterLine(normalized, "reader warmup round")
		if total > 0 {
			frac := float64(round) / float64(total)
			percent := 0.36 + 0.34*frac
			if percent > 0.70 {
				percent = 0.70
			}
			return percent, fmt.Sprintf("warming reader images %d/%d", round, total), true
		}
		return 0.38, "warming reader images", true
	case strings.HasPrefix(normalized, "reader image urls collected"):
		return 0.70, "reader images collected", true
	case strings.HasPrefix(normalized, "reader download queue start"):
		return 0.70, "preparing downloads", true
	case strings.HasPrefix(normalized, "reader queue "):
		done, total := parseCounterLine(normalized, "reader queue")
		if total > 0 {
			frac := float64(done) / float64(total)
			percent := 0.70 + 0.05*frac
			if percent > 0.75 {
				percent = 0.75
			}
			if percent < 0.70 {
				percent = 0.70
			}
			return percent, fmt.Sprintf("queuing images %d/%d", done, total), true
		}
		return 0.70, "queuing images", true
	case strings.HasPrefix(normalized, "reader saved ") || strings.HasPrefix(normalized, "reader failed "):
		done, total := parseCounterLine(normalized, "reader saved")
		if total <= 0 {
			done, total = parseCounterLine(normalized, "reader failed")
		}
		if total > 0 {
			frac := float64(done) / float64(total)
			percent := 0.75 + 0.24*frac
			if percent > 0.99 {
				percent = 0.99
			}
			if percent < 0.75 {
				percent = 0.75
			}
			if strings.Contains(normalized, "reader failed ") {
				return percent, fmt.Sprintf("downloading images %d/%d", done, total), true
			}
			return percent, fmt.Sprintf("downloading images %d/%d", done, total), true
		}
		return 0.80, "downloading images", true
	case strings.HasPrefix(normalized, "reader done"):
		return 1.0, "download complete", true
	default:
		return 0, "", false
	}
}

func parseCounterLine(line, prefix string) (int, int) {
	idx := strings.Index(strings.ToLower(strings.TrimSpace(line)), prefix)
	if idx < 0 {
		return 0, 0
	}
	rest := strings.TrimSpace(line[idx+len(prefix):])
	if rest == "" {
		return 0, 0
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, 0
	}
	token := strings.Trim(fields[0], ":")
	parts := strings.Split(token, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	done, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	total, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || total <= 0 {
		return 0, 0
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	return done, total
}

func (d *siteWorkerDaemon) closeLocked() {
	if d.stdin != nil {
		_ = d.stdin.Close()
	}
	if d.cmd != nil && d.cmd.Process != nil {
		if runtime.GOOS == "windows" {
			killTree := exec.Command("taskkill", "/PID", strconv.Itoa(d.cmd.Process.Pid), "/T", "/F")
			killTree.SysProcAttr = hiddenSysProcAttr()
			_ = killTree.Run()
		}
		_ = d.cmd.Process.Kill()
		_, _ = d.cmd.Process.Wait()
	}
	d.cmd = nil
	d.stdin = nil
	d.stdout = nil
	d.stderrDone = nil
	d.bin = ""
}

func (d *siteWorkerDaemon) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeLocked()
}

func projectRootDir() string {
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" && looksLikePortableBundle(dir) {
			return dir
		}
	}
	if wd, err := os.Getwd(); err == nil && looksLikeProjectRoot(wd) {
		return wd
	}
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" {
			if root := findProjectRootUpward(dir); root != "" {
				return root
			}
			return dir
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func findProjectRootUpward(start string) string {
	dir := filepath.Clean(strings.TrimSpace(start))
	for dir != "" {
		if looksLikeProjectRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func looksLikeProjectRoot(dir string) bool {
	for _, name := range []string{"go.mod", "workers"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func looksLikePortableBundle(dir string) bool {
	if dir == "" {
		return false
	}
	for _, name := range []string{"Comic_PC.exe", "runtime", "workers"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

func resourcePath(parts ...string) string {
	rel := filepath.Join(parts...)
	if filepath.IsAbs(rel) {
		return rel
	}
	seen := make(map[string]bool)
	bases := []string{}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		if looksLikePortableBundle(exeDir) {
			bases = append(bases, exeDir)
		}
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		bases = append(bases, wd)
	}
	if base := projectRootDir(); base != "" {
		bases = append(bases, base)
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		for exeDir != "" {
			bases = append(bases, exeDir)
			parent := filepath.Dir(exeDir)
			if parent == exeDir {
				break
			}
			exeDir = parent
		}
	}
	for _, base := range bases {
		base = filepath.Clean(base)
		if base == "" || seen[strings.ToLower(base)] {
			continue
		}
		seen[strings.ToLower(base)] = true
		candidate := filepath.Join(base, rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	base := projectRootDir()
	candidate := filepath.Join(base, rel)
	return candidate
}

func runtimeRootDir() string {
	// Runtime state belongs to the selected project/portable root. Resource
	// lookup may discover a legacy bin/runtime directory and must not redirect
	// new state, profiles, or downloads back there.
	return filepath.Join(projectRootDir(), "runtime")
}

func portableResourcePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return path
	}
	root := projectRootDir()
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return filepath.Clean(path)
	}
	return filepath.Clean(rel)
}

func resolveTaskAssetPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		return resourcePath(path)
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for index, part := range parts {
		if strings.EqualFold(part, "runtime") && index+1 < len(parts) {
			return resourcePath(filepath.Join(parts[index:]...))
		}
	}
	return path
}

func defaultBrowserUserDataSource(browserPath string) string {
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if localAppData == "" {
		localAppData = strings.TrimSpace(os.Getenv("APPDATA"))
	}
	if localAppData == "" {
		return ""
	}

	browserText := strings.ToLower(strings.TrimSpace(browserPath))
	browserName := strings.ToLower(filepath.Base(strings.TrimSpace(browserPath)))

	brandParts := []string{"Chromium"}
	switch {
	case strings.Contains(browserText, "msedge") || browserName == "msedge.exe" || strings.Contains(browserText, "microsoft edge"):
		brandParts = []string{"Microsoft", "Edge"}
	case strings.Contains(browserText, "brave") || browserName == "brave.exe":
		brandParts = []string{"BraveSoftware", "Brave-Browser"}
	case (strings.Contains(browserText, "chrome") && !strings.Contains(browserText, "chromium")) || browserName == "google-chrome.exe":
		brandParts = []string{"Google", "Chrome"}
	}

	parts := append([]string{localAppData}, brandParts...)
	parts = append(parts, "User Data")
	return filepath.Join(parts...)
}

func taskBrowserProfileRoot(worker string, taskID int) string {
	return filepath.Join(runtimeRootDir(), "browser-profiles", sanitizePathSegment(worker), fmt.Sprintf("task-%d", taskID))
}

func taskBrowserProfileDir(worker string, taskID int, purpose string) string {
	return filepath.Join(taskBrowserProfileRoot(worker, taskID), sanitizePathSegment(purpose))
}

func verifiedBrowserProfileDir(worker string) string {
	return filepath.Join(runtimeRootDir(), "browser-profiles", sanitizePathSegment(worker), "verified-base")
}

type browserProfileReadyMarker struct {
	Source          string `json:"source"`
	SourceFreshness int64  `json:"sourceFreshness"`
}

func browserProfileSourceExists(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if info, err := os.Stat(filepath.Join(path, "Local State")); err == nil && !info.IsDir() {
		return true
	}
	if info, err := os.Stat(filepath.Join(path, "Default")); err == nil && info.IsDir() {
		return true
	}
	return false
}

func browserProfileFreshness(path string) time.Time {
	var latest time.Time
	for _, rel := range []string{"Local State", filepath.Join("Default", "Network", "Cookies"), filepath.Join("Default", "Cookies")} {
		if info, err := os.Stat(filepath.Join(path, rel)); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest
}

func browserProfileReadyMatches(dest, source string) bool {
	raw, err := os.ReadFile(filepath.Join(dest, ".profile-ready"))
	if err != nil {
		return false
	}
	var marker browserProfileReadyMarker
	if json.Unmarshal(raw, &marker) != nil {
		return false
	}
	if !strings.EqualFold(filepath.Clean(marker.Source), filepath.Clean(source)) {
		return false
	}
	return browserProfileFreshness(source).UnixNano() <= marker.SourceFreshness
}

func browserProfileSeedSource(worker, browserPath string) string {
	machine := defaultBrowserUserDataSource(browserPath)
	if browserProfileSourceExists(machine) {
		return machine
	}
	verified := verifiedBrowserProfileDir(worker)
	if _, err := os.Stat(filepath.Join(verified, ".profile-ready")); err == nil {
		return verified
	}
	return machine
}

var browserProfileOperationMu sync.Mutex

func shouldSkipBrowserProfileEntry(name string, isDir bool) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	if strings.HasPrefix(lower, "singleton") || lower == "devtoolsactiveport" || lower == "lockfile" || lower == ".profile-ready" {
		return true
	}
	if isDir {
		switch lower {
		case "cache", "code cache", "gpucache", "grshadercache", "shadercache", "dawncache", "dawngraphitecache", "graphitedawncache", "gpupersistentcache", "crashpad", "browsermetrics", "component_crx_cache", "extensions_crx_cache":
			return true
		}
	}
	return false
}

func cloneBrowserProfileTree(src, dst string) error {
	src = strings.TrimSpace(src)
	dst = strings.TrimSpace(dst)
	if dst == "" {
		return nil
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if src == "" {
		return nil
	}
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return nil
	}

	if err := filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info == nil {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := info.Name()
		if shouldSkipBrowserProfileEntry(name, info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := copyBrowserProfileFile(path, target, info.Mode().Perm()); err != nil {
			return fmt.Errorf("copy profile entry %s: %w", rel, err)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func copyBrowserProfileFile(src, dst string, mode os.FileMode) error {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
		}
		in, err := os.Open(src)
		if err != nil {
			lastErr = err
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			_ = in.Close()
			lastErr = err
			continue
		}
		_, copyErr := io.Copy(out, in)
		inErr := in.Close()
		outErr := out.Close()
		if copyErr == nil && inErr == nil && outErr == nil {
			return nil
		}
		_ = os.Remove(dst)
		switch {
		case copyErr != nil:
			lastErr = copyErr
		case inErr != nil:
			lastErr = inErr
		default:
			lastErr = outErr
		}
	}
	return lastErr
}

func cloneBrowserProfileTreeAtomic(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	staging := fmt.Sprintf("%s.staging-%d-%d", dst, os.Getpid(), time.Now().UnixNano())
	_ = os.RemoveAll(staging)
	defer os.RemoveAll(staging)
	if err := cloneBrowserProfileTree(src, staging); err != nil {
		return err
	}
	marker, err := json.Marshal(browserProfileReadyMarker{
		Source:          filepath.Clean(src),
		SourceFreshness: browserProfileFreshness(src).UnixNano(),
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, ".profile-ready"), append(marker, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.Rename(staging, dst); err != nil {
		return err
	}
	return nil
}

func ensureTaskBrowserProfile(taskID int, worker, purpose, browserPath, sourceOverride string, force bool) (string, error) {
	browserProfileOperationMu.Lock()
	defer browserProfileOperationMu.Unlock()
	worker = sanitizePathSegment(worker)
	purpose = sanitizePathSegment(purpose)
	if purpose == "" {
		purpose = "content"
	}
	dest := taskBrowserProfileDir(worker, taskID, purpose)
	if !force {
		if _, err := os.Stat(filepath.Join(dest, ".profile-ready")); err == nil {
			preferred := strings.TrimSpace(sourceOverride)
			if preferred == "" && purpose == "content" {
				preferred = browserProfileSeedSource(worker, browserPath)
			}
			if preferred == "" || browserProfileReadyMatches(dest, preferred) {
				return dest, nil
			}
		}
	}
	source := strings.TrimSpace(sourceOverride)
	if source == "" {
		if purpose == "content" {
			source = browserProfileSeedSource(worker, browserPath)
		} else {
			source = defaultBrowserUserDataSource(browserPath)
		}
	}
	if source == "" {
		if err := cloneBrowserProfileTreeAtomic("", dest); err != nil {
			return "", err
		}
		return dest, nil
	}
	if err := cloneBrowserProfileTreeAtomic(source, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func updateVerifiedBrowserProfile(worker, source string) (string, error) {
	browserProfileOperationMu.Lock()
	defer browserProfileOperationMu.Unlock()
	dest := verifiedBrowserProfileDir(worker)
	if err := cloneBrowserProfileTreeAtomic(source, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// updateMachineBrowserProfile publishes verification state captured in a
// per-task browser back to the real Chromium user-data directory. Unlike task
// profiles, the machine profile must not contain our .profile-ready marker.
func updateMachineBrowserProfile(browserPath, source string) (string, error) {
	browserProfileOperationMu.Lock()
	defer browserProfileOperationMu.Unlock()
	dest := strings.TrimSpace(defaultBrowserUserDataSource(browserPath))
	if dest == "" {
		return "", fmt.Errorf("machine browser profile path unavailable")
	}
	staging := fmt.Sprintf("%s.staging-%d-%d", dest, os.Getpid(), time.Now().UnixNano())
	_ = os.RemoveAll(staging)
	defer os.RemoveAll(staging)
	if err := cloneBrowserProfileTree(source, staging); err != nil {
		return "", err
	}
	_ = os.Remove(filepath.Join(staging, ".profile-ready"))
	backup := fmt.Sprintf("%s.backup-%d-%d", dest, os.Getpid(), time.Now().UnixNano())
	_ = os.RemoveAll(backup)
	hadDest := false
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, backup); err != nil {
			return "", err
		}
		hadDest = true
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(staging, dest); err != nil {
		if hadDest {
			_ = os.Rename(backup, dest)
		}
		return "", err
	}
	if hadDest {
		_ = os.RemoveAll(backup)
	}
	return dest, nil
}

// quiesceBrowserProfile terminates only Chromium processes whose command line
// references this exact per-task profile. The Python worker may exit before its
// browser child, so killing only the worker process tree is not sufficient.
func quiesceBrowserProfile(profilePath string) error {
	profilePath = strings.TrimSpace(profilePath)
	if profilePath == "" || runtime.GOOS != "windows" {
		return nil
	}
	absPath, err := filepath.Abs(profilePath)
	if err != nil {
		return err
	}
	script := `$profile=[IO.Path]::GetFullPath($env:COMIC_PROFILE_PATH).TrimEnd('\'); $quoted='--user-data-dir="'+$profile+'"'; $plainPattern=[regex]::Escape('--user-data-dir='+$profile)+'(?=$|\s)'; $spacedQuoted='--user-data-dir "'+$profile+'"'; $spacedPattern=[regex]::Escape('--user-data-dir '+$profile)+'(?=$|\s)'; $deadline=(Get-Date).AddSeconds(8); do { $matches=@(Get-CimInstance Win32_Process -Filter "Name='chrome.exe' OR Name='chromium.exe' OR Name='msedge.exe' OR Name='brave.exe'" -ErrorAction Stop | Where-Object { $_.CommandLine -and ($_.CommandLine.IndexOf($quoted,[StringComparison]::OrdinalIgnoreCase) -ge 0 -or $_.CommandLine.IndexOf($spacedQuoted,[StringComparison]::OrdinalIgnoreCase) -ge 0 -or [regex]::IsMatch($_.CommandLine,$plainPattern,[Text.RegularExpressions.RegexOptions]::IgnoreCase) -or [regex]::IsMatch($_.CommandLine,$spacedPattern,[Text.RegularExpressions.RegexOptions]::IgnoreCase)) }); foreach($process in $matches) { Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue }; if($matches.Count -eq 0) { exit 0 }; Start-Sleep -Milliseconds 200 } while((Get-Date) -lt $deadline); exit 23`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "COMIC_PROFILE_PATH="+absPath)
	cmd.SysProcAttr = hiddenSysProcAttr()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("release browser profile %s: %w: %s", absPath, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func cleanupTaskBrowserProfile(taskID int, worker, purpose string) error {
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return nil
	}
	path := taskBrowserProfileDir(worker, taskID, purpose)
	if path == "" {
		return nil
	}
	if err := quiesceBrowserProfile(path); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := os.RemoveAll(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return lastErr
}

func cleanupTaskBrowserProfiles(taskID int, worker string) error {
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return nil
	}
	root := taskBrowserProfileRoot(worker, taskID)
	if root == "" {
		return nil
	}
	if err := quiesceBrowserProfile(root); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := os.RemoveAll(root); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return lastErr
}

func pythonBinCandidates() []string {
	bundled := bundledPythonExecutable()
	candidates := []string{bundled}
	if !runningFromPortableBundle() {
		candidates = append(candidates,
			strings.TrimSpace(os.Getenv("COMIC_DOWNLOADER_PYTHON")),
			strings.TrimSpace(os.Getenv("PYTHON")),
		)
		candidates = append(candidates, condaPythonCandidates()...)
		candidates = append(candidates, "python", "py")
	}
	var bins []string
	seen := make(map[string]bool)
	for _, bin := range candidates {
		if bin == "" {
			continue
		}
		key := strings.ToLower(bin)
		if seen[key] {
			continue
		}
		seen[key] = true
		if pythonBinSupportsDrissionPage(bin) {
			bins = append(bins, bin)
		}
	}
	return bins
}

func runningFromPortableBundle() bool {
	if exe, err := os.Executable(); err == nil {
		return looksLikePortableBundle(filepath.Dir(exe))
	}
	return false
}

func pythonBinSupportsDrissionPage(bin string) bool {
	if bin == "" {
		return false
	}
	if strings.HasSuffix(strings.ToLower(bin), ".exe") || filepath.IsAbs(bin) {
		if _, err := os.Stat(bin); err != nil {
			return false
		}
	}
	cmd := exec.Command(bin, "-c", "import DrissionPage")
	cmd.SysProcAttr = hiddenSysProcAttr()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func pythonBinSupportsAdblockRules(bin string) bool {
	if bin == "" {
		return false
	}
	if strings.HasSuffix(strings.ToLower(bin), ".exe") || filepath.IsAbs(bin) {
		if _, err := os.Stat(bin); err != nil {
			return false
		}
	}
	adblockScript := resourcePath("adblock_rules.py")
	code := fmt.Sprintf(`import os, sys
sys.path.insert(0, r%q)
import adblock_rules`, filepath.Dir(adblockScript))
	cmd := exec.Command(bin, "-c", code)
	cmd.SysProcAttr = hiddenSysProcAttr()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func updateAdblockRulesNow() error {
	bins := pythonBinCandidates()
	adblockScript := resourcePath("adblock_rules.py")
	projectDir := filepath.Dir(adblockScript)
	var lastErr error
	for _, bin := range bins {
		if !pythonBinSupportsAdblockRules(bin) {
			continue
		}
		code := fmt.Sprintf(`import os, sys
sys.path.insert(0, r%q)
from adblock_rules import load_adblock_patterns, get_adblock_status
patterns = load_adblock_patterns(True)
status = get_adblock_status()
print(len(patterns))
print(status.get("updated_at", ""))
print(status.get("cache_file", ""))`, projectDir)
		cmd := exec.Command(bin, "-c", code)
		cmd.SysProcAttr = hiddenSysProcAttr()
		cmd.Dir = projectDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			lastErr = fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(string(out)))
			continue
		}
		log.Printf("adblock manual refresh done: python=%s output=%s", bin, strings.TrimSpace(string(out)))
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no python interpreter available for adblock refresh")
	}
	return lastErr
}

func hiddenSysProcAttr() *syscall.SysProcAttr {
	if runtime.GOOS != "windows" {
		return nil
	}
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000,
	}
}

func removeTaskID(ids []int, target int) []int {
	if len(ids) == 0 {
		return ids
	}
	next := ids[:0]
	for _, id := range ids {
		if id != target {
			next = append(next, id)
		}
	}
	return next
}

func condaPythonCandidates() []string {
	var candidates []string
	roots := []string{}
	for _, env := range []string{"CONDA_PREFIX", "MAMBA_ROOT_PREFIX", "CONDA_EXE"} {
		if value := strings.TrimSpace(os.Getenv(env)); value != "" {
			roots = append(roots, value)
		}
	}
	roots = append(roots, `C:\ProgramData\miniforge3`, `C:\Program Files\Miniforge3`)
	for _, root := range roots {
		base := root
		if strings.HasSuffix(strings.ToLower(base), `\python.exe`) {
			base = filepath.Dir(filepath.Dir(base))
		}
		if strings.HasSuffix(strings.ToLower(base), `\scripts\conda.exe`) || strings.HasSuffix(strings.ToLower(base), `\scripts\mamba.exe`) {
			base = filepath.Dir(filepath.Dir(base))
		}
		for _, rel := range []string{
			filepath.Join(base, "envs", "comic_downloader", "python.exe"),
			filepath.Join(base, "envs", "comic_downloader", "python3.exe"),
			filepath.Join(base, "python.exe"),
			filepath.Join(base, "python3.exe"),
		} {
			if _, err := os.Stat(rel); err == nil {
				candidates = append(candidates, rel)
			}
		}
	}
	return candidates
}

func bundledPythonExecutable() string {
	candidates := []string{
		resourcePath("runtime", "python", "python.exe"),
		resourcePath("runtime", "python", "python3.exe"),
		resourcePath("runtime", "python", "bin", "python.exe"),
		resourcePath("runtime", "python", "bin", "python3.exe"),
		resourcePath("runtime", "Python", "python.exe"),
		resourcePath("runtime", "Python", "python3.exe"),
	}
	for _, candidate := range candidates {
		if candidate != "" {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return ""
}

func bundledChromiumPath() string {
	candidates := []string{
		resourcePath("runtime", "chromium", "chrome.exe"),
		resourcePath("runtime", "chromium", "Chrome.exe"),
		resourcePath("runtime", "chromium", "chrome"),
	}
	for _, candidate := range candidates {
		if candidate != "" {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return ""
}
