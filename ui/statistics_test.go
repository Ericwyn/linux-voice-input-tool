package ui

import (
	"strings"
	"testing"

	"linux-voice-input/config"
	"linux-voice-input/session"
	"linux-voice-input/stats"
)

func TestToggleStatisticsPersistsAndUpdatesController(t *testing.T) {
	cfg := config.DefaultConfig()
	controller := session.New(cfg)
	defer controller.Close()

	var saved *config.Config
	app := &App{
		cfg:        cfg,
		controller: controller,
		saveConfig: func(value *config.Config) error {
			copy := *value
			saved = &copy
			return nil
		},
	}

	app.toggleStatistics()
	if !app.StatisticsEnabled() || !controller.StatisticsEnabled() {
		t.Fatal("statistics not enabled in app/controller")
	}
	if saved == nil || !saved.Statistics.Enabled {
		t.Fatalf("saved config = %#v", saved)
	}

	app.toggleStatistics()
	if app.StatisticsEnabled() || controller.StatisticsEnabled() {
		t.Fatal("statistics not disabled in app/controller")
	}
}

func TestFormatStatisticsView(t *testing.T) {
	today := stats.Aggregate{Totals: stats.Counters{SuccessfulInputs: 2, Characters: 20, HanCharacters: 18}}
	recent := stats.Aggregate{Totals: stats.Counters{SuccessfulInputs: 8, Characters: 90, HanCharacters: 70}}
	view := formatStatisticsView(true, today, recent)
	for _, want := range []string{"状态: 已启用", "今日: 2 次", "最近30天: 8 次"} {
		if !strings.Contains(view, want) {
			t.Fatalf("formatStatisticsView() = %q, missing %q", view, want)
		}
	}
}
