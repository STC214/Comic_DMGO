//go:build windows

package main

import "os"

func comicPreferredUIFontFace() string {
	candidates := []struct {
		path string
		face string
	}{
		{`C:\Windows\Fonts\msyh.ttc`, "Microsoft YaHei UI"},
		{`C:\Windows\Fonts\YuGothR.ttc`, "Yu Gothic UI"},
		{`C:\Windows\Fonts\msjh.ttc`, "Microsoft JhengHei UI"},
		{`C:\Windows\Fonts\segoeui.ttf`, "Segoe UI"},
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate.path); err == nil && !info.IsDir() {
			return candidate.face
		}
	}
	return "Segoe UI"
}
