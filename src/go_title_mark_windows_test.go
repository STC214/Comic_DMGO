//go:build windows && !legacyui

package main

import (
	"bytes"
	"testing"
)

func TestGoTitleMarkIsSeparateFromExecutableIcon(t *testing.T) {
	resource := goTitleMarkResource()
	if resource == nil || resource.Name() != "go-title-mark.svg" || len(resource.Content()) == 0 {
		t.Fatal("Go UI title mark resource is unavailable")
	}
	if bytes.Equal(resource.Content(), embeddedAppIcon) {
		t.Fatal("Go UI title mark unexpectedly replaced the executable icon")
	}
}
