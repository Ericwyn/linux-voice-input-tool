package corrector

import (
	"testing"

	"linux-voice-input/config"
)

func completeLLMConfig(mode string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.CorrectionMode = mode
	cfg.LLMCorrection.BaseURL = "https://api.example.com/v1"
	cfg.LLMCorrection.APIKey = "sk-xxx"
	cfg.LLMCorrection.Model = "gpt-4o-mini"
	return cfg
}

func TestNewLLMServiceFromConfigComplete(t *testing.T) {
	svc := NewLLMServiceFromConfig(completeLLMConfig(config.CorrectionModeLLM))
	if svc == nil {
		t.Fatal("service should never be nil")
	}
	if !svc.Enabled() {
		t.Fatal("llm mode with complete config should be enabled")
	}
}

func TestNewLLMServiceFromConfigCombinedEnabled(t *testing.T) {
	svc := NewLLMServiceFromConfig(completeLLMConfig(config.CorrectionModeCombined))
	if !svc.Enabled() {
		t.Fatal("combined mode with complete config should enable the llm service")
	}
}

func TestNewLLMServiceFromConfigIncompleteIsInert(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CorrectionMode = config.CorrectionModeLLM
	// Missing base_url/api_key/model: incomplete.
	svc := NewLLMServiceFromConfig(cfg)
	if svc == nil {
		t.Fatal("service should never be nil")
	}
	if svc.Enabled() {
		t.Fatal("incomplete llm config must yield a disabled service")
	}
}

func TestNewLLMServiceFromConfigLocalModeDisabled(t *testing.T) {
	// Complete config but mode is local: the LLM service exists but is disabled.
	svc := NewLLMServiceFromConfig(completeLLMConfig(config.CorrectionModeLocal))
	if svc.Enabled() {
		t.Fatal("local mode should not enable the llm service")
	}
}

func TestNewLLMServiceFromConfigNil(t *testing.T) {
	if svc := NewLLMServiceFromConfig(nil); svc == nil || svc.Enabled() {
		t.Fatal("nil config should yield a non-nil disabled service")
	}
}
