//go:build windows && !legacyui

package main

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

// goTitleMark is a UI-only resource displayed after the title text. It is
// intentionally separate from embeddedAppIcon, the executable/window icon.
//
//go:embed assets/go-title-mark.svg
var goTitleMarkBytes []byte

func goTitleMarkResource() fyne.Resource {
	return fyne.NewStaticResource("go-title-mark.svg", goTitleMarkBytes)
}
