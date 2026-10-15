package ui

import (
	"testing"

	"linux-voice-input/config"
	"linux-voice-input/session"
)

// TestSetCorrectionModeTogglesLocalService verifies the controller flips the
// local correction service on/off as the mode changes. The LLM service stays
// off here because the default config has no LLM endpoint configured.
func TestSetCorrectionModeTogglesLocalService(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CorrectionMode = config.CorrectionModeOff
	controller := session.New(cfg)
	defer controller.Close()

	controller.SetCorrectionMode(config.CorrectionModeLocal)
	if !controller.LocalCorrectionEnabled() {
		t.Fatal("local correction should be enabled in local mode")
	}
	if controller.LLMCorrectionEnabled() {
		t.Fatal("LLM correction should stay off without an LLM config")
	}

	controller.SetCorrectionMode(config.CorrectionModeOff)
	if controller.LocalCorrectionEnabled() {
		t.Fatal("local correction should be disabled in off mode")
	}
}

// TestSetCorrectionModeCombinedEnablesLLMWhenConfigured verifies that combined
// mode enables both services once the LLM config is complete.
func TestSetCorrectionModeCombinedEnablesLLMWhenConfigured(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CorrectionMode = config.CorrectionModeOff
	cfg.LLMCorrection.BaseURL = "https://example.com/v1"
	cfg.LLMCorrection.APIKey = "sk-test"
	cfg.LLMCorrection.Model = "gpt-4o-mini"
	controller := session.New(cfg)
	defer controller.Close()

	controller.SetCorrectionMode(config.CorrectionModeCombined)
	if !controller.LocalCorrectionEnabled() {
		t.Fatal("local correction should be enabled in combined mode")
	}
	if !controller.LLMCorrectionEnabled() {
		t.Fatal("LLM correction should be enabled in combined mode with complete config")
	}
}
