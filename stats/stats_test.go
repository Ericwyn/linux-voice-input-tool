package stats

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCountCharactersUsesUnicodeRunesAndSkipsWhitespace(t *testing.T) {
	characters, han := CountCharacters("你好, Go 👋\n")
	if characters != 6 || han != 2 {
		t.Fatalf("CountCharacters() = %d/%d, want 6/2", characters, han)
	}
}

func TestRuneEditDistance(t *testing.T) {
	tests := []struct {
		name          string
		first, second string
		want          int
	}{
		{name: "same", first: "线程", second: "线程", want: 0},
		{name: "replace runes", first: "开一个现成", second: "开一个线程", want: 2},
		{name: "insert", first: "Redis", second: "Redis!", want: 1},
		{name: "delete unicode", first: "你好呀", second: "你好", want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RuneEditDistance(test.first, test.second); got != test.want {
				t.Fatalf("RuneEditDistance(%q, %q) = %d, want %d", test.first, test.second, got, test.want)
			}
		})
	}
}

func TestTrackerWritesDailyFilesAndRangeAggregates(t *testing.T) {
	root := t.TempDir()
	location := time.FixedZone("test", 8*60*60)
	firstDay := time.Date(2026, time.August, 25, 10, 0, 0, 0, location)
	secondDay := firstDay.AddDate(0, 0, 1)
	tracker := NewTracker(root, true)

	tracker.Record(NewEvent(firstDay, "sensevoice", "开一个现成", "开一个线程",
		3*time.Second, 4*time.Second, []string{"model", "model"}))
	tracker.Record(NewEvent(secondDay, "whisper", "hello", "hello",
		2*time.Second, 2500*time.Millisecond, nil))
	if err := tracker.Flush(); err != nil {
		t.Fatal(err)
	}

	firstDocument, err := ReadDay(root, firstDay)
	if err != nil {
		t.Fatal(err)
	}
	if firstDocument.Totals.SuccessfulInputs != 1 || firstDocument.Totals.Characters != 5 ||
		firstDocument.Totals.HanCharacters != 5 || firstDocument.Totals.CorrectedInputs != 1 ||
		firstDocument.Totals.CorrectionEdits != 2 || firstDocument.Totals.CorrectionStrategyInputs["model"] != 1 {
		t.Fatalf("first day totals = %#v", firstDocument.Totals)
	}
	info, err := os.Stat(DailyPath(root, firstDay))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("daily file permissions = %o, want 600", info.Mode().Perm())
	}

	aggregate, err := LoadRecent(root, secondDay, 2)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Days != 2 || aggregate.Totals.SuccessfulInputs != 2 || aggregate.Totals.Characters != 10 {
		t.Fatalf("aggregate = %#v", aggregate)
	}
	if aggregate.Engines["sensevoice"].SuccessfulInputs != 1 || aggregate.Engines["whisper"].SuccessfulInputs != 1 {
		t.Fatalf("engine totals = %#v", aggregate.Engines)
	}
	month, err := LoadMonth(root, 2026, time.August, location)
	if err != nil {
		t.Fatal(err)
	}
	if month.Totals.SuccessfulInputs != 2 {
		t.Fatalf("month totals = %#v", month.Totals)
	}
	all, err := LoadAll(root)
	if err != nil {
		t.Fatal(err)
	}
	if all.Totals.SuccessfulInputs != 2 {
		t.Fatalf("all totals = %#v", all.Totals)
	}

	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTrackerDisabledPreservesHistory(t *testing.T) {
	root := t.TempDir()
	date := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.Local)
	tracker := NewTracker(root, false)
	event := NewEvent(date, "sensevoice", "一", "一", time.Second, 2*time.Second, nil)

	tracker.Record(event)
	if err := tracker.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(DailyPath(root, date)); !os.IsNotExist(err) {
		t.Fatalf("disabled tracker created a file: %v", err)
	}

	tracker.SetEnabled(true)
	tracker.Record(event)
	if err := tracker.Flush(); err != nil {
		t.Fatal(err)
	}
	tracker.SetEnabled(false)
	tracker.Record(event)
	if err := tracker.Flush(); err != nil {
		t.Fatal(err)
	}
	document, err := ReadDay(root, date)
	if err != nil {
		t.Fatal(err)
	}
	if document.Totals.SuccessfulInputs != 1 {
		t.Fatalf("successful inputs = %d, want 1", document.Totals.SuccessfulInputs)
	}
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTrackerCloseFlushesLastEvent(t *testing.T) {
	root := t.TempDir()
	date := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.Local)
	tracker := NewTracker(root, true)
	tracker.Record(NewEvent(date, "sensevoice", "测试", "测试", time.Second, time.Second, nil))
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
	document, err := ReadDay(root, date)
	if err != nil {
		t.Fatal(err)
	}
	if document.Totals.SuccessfulInputs != 1 {
		t.Fatalf("successful inputs = %d, want 1", document.Totals.SuccessfulInputs)
	}
}

func TestCorruptDailyFileIsNotOverwritten(t *testing.T) {
	root := t.TempDir()
	date := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.Local)
	path := DailyPath(root, date)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("not-json\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}

	tracker := NewTracker(root, true)
	tracker.Record(NewEvent(date, "sensevoice", "测试", "测试", time.Second, time.Second, nil))
	if err := tracker.Flush(); err == nil {
		t.Fatal("Flush() should report the corrupt daily file")
	}
	_ = tracker.Close()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("corrupt file was overwritten: %q", got)
	}
}

func TestLoadAllIgnoresFilesOutsideDailyLayout(t *testing.T) {
	root := t.TempDir()
	date := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.Local)
	tracker := NewTracker(root, true)
	tracker.Record(NewEvent(date, "sensevoice", "测试", "测试", time.Second, time.Second, nil))
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.json"), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}

	aggregate, err := LoadAll(root)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Days != 1 || aggregate.Totals.SuccessfulInputs != 1 {
		t.Fatalf("aggregate = %#v", aggregate)
	}
}
