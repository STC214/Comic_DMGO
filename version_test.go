package main

import (
	"strings"
	"testing"
	"time"
)

func TestVersionRuleYearMonthDayHourMinuteSecond(t *testing.T) {
	original := appVersion
	defer func() { appVersion = original }()
	appVersion = "20260812153045"
	if got := normalizedAppVersion(); got != appVersion {
		t.Fatalf("normalized version=%q", got)
	}
	if got := appVersionLabel(); !strings.Contains(got, appVersion) {
		t.Fatalf("version label=%q", got)
	}
	appVersion = "2026-08-12"
	if got := normalizedAppVersion(); got != "00000000000000" {
		t.Fatalf("invalid version normalized=%q", got)
	}
}

func TestDownloadSpeedTracker(t *testing.T) {
	start := time.Date(2026, 8, 12, 10, 0, 0, 0, time.Local)
	tracker := newDownloadSpeedTracker(start)
	if got := tracker.Update(start.Add(500*time.Millisecond), 1024); got != "实时 2.0 KB/s" {
		t.Fatalf("realtime speed=%q", got)
	}
	if got := tracker.Update(start.Add(time.Second), 2048); got != "实时 2.0 KB/s" {
		t.Fatalf("second realtime speed=%q", got)
	}
	if got := tracker.Update(start.Add(1500*time.Millisecond), 2048); got != "实时 0 B/s" {
		t.Fatalf("stalled realtime speed=%q", got)
	}
	if got := tracker.Average(start.Add(2 * time.Second)); got != "平均 1.0 KB/s" {
		t.Fatalf("average speed=%q", got)
	}
}

func TestTaskCardDisplaysRealtimeSpeed(t *testing.T) {
	task := &Task{State: TaskRunning, Percent: 0.5, SpeedText: "实时 2.0 MB/s", Detail: "downloading images 10/20"}
	text := taskCardMetaText(task)
	if !strings.Contains(text, "实时 2.0 MB/s") {
		t.Fatalf("task card metadata=%q", text)
	}
}
