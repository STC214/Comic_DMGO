//go:build windows

package main

import (
	"embed"
	"os"
	"path/filepath"
	"sync"
)

var _ embed.FS

//go:embed assets/app.ico
var embeddedAppIcon []byte

var (
	appIconOnce sync.Once
	appIconPath string
	appIconErr  error
)

func ensureAppIconFile() (string, error) {
	appIconOnce.Do(func() {
		cacheDir := filepath.Join(projectRootDir(), ".tmp", "assets")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			appIconErr = err
			return
		}
		path := filepath.Join(cacheDir, "app.ico")
		if err := os.WriteFile(path, embeddedAppIcon, 0o644); err != nil {
			appIconErr = err
			return
		}
		appIconPath = path
	})
	return appIconPath, appIconErr
}
