package ui

import (
	"testing"

	"linux-voice-input/config"
	"linux-voice-input/session"
)

func TestToggleCorrectionsPersistsAndUpdatesController(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Corrections.Enabled = false
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

	app.toggleCorrections()

	if !app.CorrectionsEnabled() || !controller.CorrectionsEnabled() {
		t.Fatal("corrections not enabled in app/controller")
	}
	if saved == nil || !saved.Corrections.Enabled {
		t.Fatalf("saved config = %#v", saved)
	}

	app.toggleCorrections()
	if app.CorrectionsEnabled() || controller.CorrectionsEnabled() {
		t.Fatal("corrections not disabled in app/controller")
	}
}
