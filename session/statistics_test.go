package session

import (
	"errors"
	"testing"
	"time"

	"linux-voice-input/config"
	"linux-voice-input/input"
	"linux-voice-input/stats"
)

type recordingStatsTracker struct {
	enabled bool
	events  []stats.Event
}

func (tracker *recordingStatsTracker) Enabled() bool         { return tracker.enabled }
func (tracker *recordingStatsTracker) SetEnabled(value bool) { tracker.enabled = value }
func (tracker *recordingStatsTracker) Flush() error          { return nil }
func (tracker *recordingStatsTracker) Close() error          { return nil }
func (tracker *recordingStatsTracker) Record(event stats.Event) {
	if tracker.enabled {
		tracker.events = append(tracker.events, event)
	}
}

func TestControllerStatisticsToggleDoesNotReplaceEngine(t *testing.T) {
	cfg := config.DefaultConfig()
	tracker := &recordingStatsTracker{}
	controller := &Controller{cfg: cfg, statistics: tracker}
	originalEngine := controller.engine

	controller.SetStatisticsEnabled(true)

	if !controller.StatisticsEnabled() || !cfg.Statistics.Enabled || !tracker.enabled {
		t.Fatal("statistics were not enabled consistently")
	}
	if controller.engine != originalEngine {
		t.Fatal("toggling statistics replaced the ASR engine")
	}

	controller.SetStatisticsEnabled(false)
	if controller.StatisticsEnabled() || cfg.Statistics.Enabled {
		t.Fatal("statistics were not disabled consistently")
	}
}

func TestPasteSuccessRecordsMeasuredEvent(t *testing.T) {
	insertedAt := time.Date(2026, time.August, 26, 12, 0, 5, 0, time.Local)
	startTime := insertedAt.Add(-5 * time.Second)
	tracker := &recordingStatsTracker{enabled: true}
	_, err := pasteAndRecordStatistics(
		func(text string, restoreClipboard bool, targetWindow string) (input.PasteResult, error) {
			if text != "开一个线程" || !restoreClipboard || targetWindow != "42" {
				t.Fatalf("paste args = %q/%v/%q", text, restoreClipboard, targetWindow)
			}
			return input.PasteResult{InsertedAt: insertedAt}, nil
		},
		transcriptionResult{
			RawText:           "开一个现成",
			Text:              "开一个线程",
			AppliedStrategies: []string{"dictionary"},
		},
		true,
		"42",
		tracker,
		"sensevoice",
		startTime,
		3*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracker.events) != 1 {
		t.Fatalf("recorded events = %d, want 1", len(tracker.events))
	}
	event := tracker.events[0]

	if event.OccurredAt != insertedAt || event.Engine != "sensevoice" {
		t.Fatalf("event identity = %#v", event)
	}
	if event.Characters != 5 || event.HanCharacters != 5 || !event.Corrected || event.CorrectionEdits != 2 {
		t.Fatalf("event metrics = %#v", event)
	}
	if event.RecordingTime != 3*time.Second || event.InputTime != 5*time.Second {
		t.Fatalf("event durations = %s/%s", event.RecordingTime, event.InputTime)
	}
}

func TestPasteFailureDoesNotRecordStatistics(t *testing.T) {
	tracker := &recordingStatsTracker{enabled: true}
	_, err := pasteAndRecordStatistics(
		func(string, bool, string) (input.PasteResult, error) {
			return input.PasteResult{}, errors.New("paste failed")
		},
		transcriptionResult{RawText: "测试", Text: "测试"},
		false,
		"",
		tracker,
		"sensevoice",
		time.Now().Add(-time.Second),
		time.Second,
	)
	if err == nil {
		t.Fatal("paste failure should be returned")
	}
	if len(tracker.events) != 0 {
		t.Fatalf("paste failure recorded %d events", len(tracker.events))
	}
}
