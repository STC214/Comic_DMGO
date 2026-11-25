//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const persistedAppStateVersion = 5

type persistedPoint struct {
	X int32 `json:"x"`
	Y int32 `json:"y"`
}

type persistedRect struct {
	Left   int32 `json:"left"`
	Top    int32 `json:"top"`
	Right  int32 `json:"right"`
	Bottom int32 `json:"bottom"`
}

type persistedWindowPlacement struct {
	Valid          bool           `json:"valid"`
	Flags          uint32         `json:"flags"`
	ShowCmd        uint32         `json:"showCmd"`
	MinPosition    persistedPoint `json:"minPosition"`
	MaxPosition    persistedPoint `json:"maxPosition"`
	NormalPosition persistedRect  `json:"normalPosition"`
}

type persistedUIState struct {
	ActiveTab         string                   `json:"activeTab"`
	URLDraft          string                   `json:"urlDraft"`
	DownloadRootDraft string                   `json:"downloadRootDraft"`
	ChromiumPath      string                   `json:"chromiumPath"`
	SelectedTaskIDs   []int                    `json:"selectedTaskIds,omitempty"`
	AutoRetryTaskIDs  []int                    `json:"autoRetryTaskIds,omitempty"`
	Window            persistedWindowPlacement `json:"window"`
}

type persistedAppState struct {
	Version     int              `json:"version"`
	SavedAt     time.Time        `json:"savedAt"`
	NextTaskID  int              `json:"nextTaskId"`
	Concurrency int              `json:"concurrency"`
	Tasks       []*Task          `json:"tasks"`
	UI          persistedUIState `json:"ui"`
}

var persistedStateMu sync.Mutex

func persistedStatePath() string {
	return filepath.Join(runtimeRootDir(), "comic_downloader_state.json")
}

func loadPersistedAppState() (*persistedAppState, error) {
	primary := persistedStatePath()
	state, err := loadPersistedAppStateFromPath(primary)
	if err != nil || state != nil {
		return state, err
	}
	// Older builds launched from bin treated the executable directory as the
	// project root. Import that state once after root discovery was corrected.
	if exe, exeErr := os.Executable(); exeErr == nil {
		legacy := filepath.Join(filepath.Dir(exe), "runtime", "comic_downloader_state.json")
		if !strings.EqualFold(filepath.Clean(legacy), filepath.Clean(primary)) {
			return loadPersistedAppStateFromPath(legacy)
		}
	}
	return nil, nil
}

func loadPersistedAppStateFromPath(path string) (*persistedAppState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parsePersistedAppState(data)
}

func parsePersistedAppState(data []byte) (*persistedAppState, error) {
	var state persistedAppState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	state.normalize()
	return &state, nil
}

func savePersistedAppState(state *persistedAppState) error {
	if state == nil {
		return nil
	}
	state.normalize()

	path := persistedStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "comic_downloader_state_*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	enc, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(enc); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

func (s *persistedAppState) normalize() {
	if s == nil {
		return
	}
	if s.Version <= 0 {
		s.Version = persistedAppStateVersion
	}
	if s.UI.ActiveTab == "" {
		s.UI.ActiveTab = "base"
	}
	for _, task := range s.Tasks {
		if task == nil {
			continue
		}
		task.URL = strings.TrimSpace(task.URL)
		task.Title = strings.TrimSpace(task.Title)
		task.DownloadRoot = strings.TrimSpace(task.DownloadRoot)
		task.Detail = strings.TrimSpace(task.Detail)
		task.SpeedText = strings.TrimSpace(task.SpeedText)
		task.ThumbnailPath = strings.TrimSpace(task.ThumbnailPath)
		if task.CreatedAt.IsZero() {
			task.CreatedAt = task.UpdatedAt
		}
		if task.CreatedAt.IsZero() {
			task.CreatedAt = time.Now()
		}
		if task.UpdatedAt.IsZero() {
			task.UpdatedAt = task.CreatedAt
		}
	}
}
