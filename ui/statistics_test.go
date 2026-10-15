package ui

import (
	"testing"

	"linux-voice-input/stats"
)

// TestStatsPeriod verifies the aggregate -> settings display-shape conversion,
// including counter passthrough and duration formatting. Statistics are now a
// read-only panel in the settings web UI (backed by statsProvider); the former
// tray toggle and notification view were removed.
func TestStatsPeriod(t *testing.T) {
	agg := stats.Aggregate{
		Days: 7,
		Totals: stats.Counters{
			SuccessfulInputs:    5,
			Characters:          120,
			HanCharacters:       100,
			CorrectedInputs:     3,
			RecordingDurationMS: 65000, // 1m5s
			InputDurationMS:     70000,
		},
	}
	p := statsPeriod("最近 7 天", agg)
	if p.Label != "最近 7 天" || p.Days != 7 {
		t.Fatalf("label/days = %q/%d", p.Label, p.Days)
	}
	if p.SuccessfulInputs != 5 || p.Characters != 120 || p.HanCharacters != 100 || p.CorrectedInputs != 3 {
		t.Fatalf("counters = %#v", p)
	}
	// Durations are formatted via stats.FormatDuration; just assert non-empty so
	// the test does not pin the exact format string.
	if p.RecordingDuration == "" || p.InputDuration == "" {
		t.Fatalf("durations not formatted: %#v", p)
	}
}
