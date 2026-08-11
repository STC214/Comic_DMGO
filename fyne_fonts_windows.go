//go:build windows && !legacyui

package main

import _ "embed"

//go:embed assets/fonts/simhei.ttf
var embeddedFyneFontRegular []byte
