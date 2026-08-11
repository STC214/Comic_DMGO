package main

// appVersion is replaced at build time with a yyyyMMddHHmmss timestamp.
var appVersion = "00000000000000"

func appVersionLabel() string {
	return "版本 " + appVersion
}
