package session

import (
	"context"
	"errors"
	"testing"

	"linux-voice-input/config"
	"linux-voice-input/corrector"
)

type sessionTestStrategy struct {
	name  string
	apply func(string) (string, error)
}

func (s *sessionTestStrategy) Name() string { return s.name }
func (s *sessionTestStrategy) Apply(_ context.Context, text string) (string, error) {
	return s.apply(text)
}
func (s *sessionTestStrategy) Close() error { return nil }

func TestControllerApplyCorrections(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Corrections.Enabled = true
	service := corrector.NewService(true, &sessionTestStrategy{
		name: "model",
		apply: func(text string) (string, error) {
			return text + "-corrected", nil
		},
	})
	controller := &Controller{cfg: cfg, correctionService: service}

	if got := controller.applyCorrections("input"); got.Text != "input-corrected" {
		t.Fatalf("applyCorrections() = %#v", got)
	}
}

func TestControllerCorrectionFailureKeepsText(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Corrections.Enabled = true
	service := corrector.NewService(true, &sessionTestStrategy{
		name: "model",
		apply: func(string) (string, error) {
			return "partial", errors.New("model failed")
		},
	})
	controller := &Controller{cfg: cfg, correctionService: service}

	if got := controller.applyCorrections("input"); got.Text != "input" {
		t.Fatalf("applyCorrections() = %#v, want fail-open original", got)
	}
}

func TestControllerSetCorrectionModeDoesNotReplaceEngine(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CorrectionMode = config.CorrectionModeOff
	service := corrector.NewService(false, &sessionTestStrategy{
		name:  "model",
		apply: func(text string) (string, error) { return text, nil },
	})
	controller := &Controller{cfg: cfg, correctionService: service}
	originalEngine := controller.engine

	controller.SetCorrectionMode(config.CorrectionModeLocal)

	if !controller.LocalCorrectionEnabled() || !cfg.UseLocalCorrection() || !service.Enabled() {
		t.Fatal("local correction was not enabled consistently")
	}
	if controller.engine != originalEngine {
		t.Fatal("switching correction mode replaced the ASR engine")
	}
}
