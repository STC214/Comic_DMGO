package main

import "regexp"

// appVersion is replaced at build time with a yyyyMMddHHmmss timestamp.
var appVersion = "00000000000000"

var appVersionPattern = regexp.MustCompile(`^\d{14}$`)

func normalizedAppVersion() string {
	if appVersionPattern.MatchString(appVersion) {
		return appVersion
	}
	return "00000000000000"
}

func appVersionLabel() string {
	return "版本 " + normalizedAppVersion()
}
