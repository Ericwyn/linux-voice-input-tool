package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"linux-voice-input/stats"
)

func TestParseStatsQuery(t *testing.T) {
	if got := parseMode([]string{"--stats", "--days", "30"}); got != modeStats {
		t.Fatalf("parseMode(--stats) = %v, want modeStats", got)
	}

	query, err := parseStatsQuery([]string{"--stats", "--days", "30"})
	if err != nil || query.days != 30 {
		t.Fatalf("days query = %#v, %v", query, err)
	}

	query, err = parseStatsQuery([]string{"--stats", "--month=2026-08"})
	if err != nil || query.monthYear != 2026 || query.month != time.August {
		t.Fatalf("month query = %#v, %v", query, err)
	}

	if _, err := parseStatsQuery([]string{"--stats", "--days", "30", "--month", "2026-08"}); err == nil {
		t.Fatal("combined --days/--month should fail")
	}
	if _, err := parseStatsQuery([]string{"--stats", "--days", "0"}); err == nil {
		t.Fatal("zero --days should fail")
	}
}

func TestRenderStatsForRecentDays(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.Local)
	tracker := stats.NewTracker(root, true)
	tracker.Record(stats.NewEvent(now, "sensevoice", "测试", "测试", time.Second, 2*time.Second, nil))
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := renderStats(root, true, statsQuery{days: 30}, now, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"语音输入统计（已启用）", "最近30天: 1 次", "按引擎:", "sensevoice: 1 次"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("renderStats() = %q, missing %q", output.String(), want)
		}
	}
}

func TestRenderStatsWithNoFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.Local)
	var output bytes.Buffer
	if err := renderStats(root, false, statsQuery{}, now, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"语音输入统计（已暂停）", "今日: 0 次", "全部累计: 0 次"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("renderStats() = %q, missing %q", output.String(), want)
		}
	}
}
