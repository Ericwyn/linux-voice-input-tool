package session

import (
	"errors"
	"testing"

	"linux-voice-input/config"
	"linux-voice-input/corrector"
)

// TestApplyLLMCorrectionDisabled verifies a disabled LLM service passes text
// through untouched.
func TestApplyLLMCorrectionDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	service := corrector.NewService(false, &sessionTestStrategy{
		name:  "llm",
		apply: func(string) (string, error) { return "should-not-run", nil },
	})
	controller := &Controller{cfg: cfg, llmService: service}

	if got := controller.applyLLMCorrection("原始"); got != "原始" {
		t.Fatalf("applyLLMCorrection() = %q, want untouched 原始", got)
	}
}

// TestApplyLLMCorrectionEmptyText verifies empty input is returned as-is
// without invoking the strategy.
func TestApplyLLMCorrectionEmptyText(t *testing.T) {
	cfg := config.DefaultConfig()
	called := false
	service := corrector.NewService(true, &sessionTestStrategy{
		name: "llm",
		apply: func(string) (string, error) {
			called = true
			return "x", nil
		},
	})
	controller := &Controller{cfg: cfg, llmService: service}

	if got := controller.applyLLMCorrection(""); got != "" {
		t.Fatalf("applyLLMCorrection(\"\") = %q, want empty", got)
	}
	if called {
		t.Fatal("strategy should not run on empty text")
	}
}

// TestApplyLLMCorrectionSuccess verifies an enabled service applies the
// correction.
func TestApplyLLMCorrectionSuccess(t *testing.T) {
	cfg := config.DefaultConfig()
	service := corrector.NewService(true, &sessionTestStrategy{
		name:  "llm",
		apply: func(text string) (string, error) { return text + "（已纠错）", nil },
	})
	controller := &Controller{cfg: cfg, llmService: service}

	if got := controller.applyLLMCorrection("语音文本"); got != "语音文本（已纠错）" {
		t.Fatalf("applyLLMCorrection() = %q", got)
	}
}

// TestApplyLLMCorrectionFailOpen verifies a strategy error keeps the input
// text (fail-open), matching the "timeout/failure uses original" requirement.
func TestApplyLLMCorrectionFailOpen(t *testing.T) {
	cfg := config.DefaultConfig()
	service := corrector.NewService(true, &sessionTestStrategy{
		name:  "llm",
		apply: func(string) (string, error) { return "partial", errors.New("network down") },
	})
	controller := &Controller{cfg: cfg, llmService: service}

	if got := controller.applyLLMCorrection("保留原文"); got != "保留原文" {
		t.Fatalf("applyLLMCorrection() = %q, want fail-open 保留原文", got)
	}
}

// TestApplyLLMCorrectionNilService verifies a nil service is safe.
func TestApplyLLMCorrectionNilService(t *testing.T) {
	controller := &Controller{cfg: config.DefaultConfig()}
	if got := controller.applyLLMCorrection("x"); got != "x" {
		t.Fatalf("applyLLMCorrection() = %q, want x", got)
	}
}

// TestSetCorrectionModeDrivesBothServices verifies the mode flips both services'
// enabled state as documented.
func TestSetCorrectionModeDrivesBothServices(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CorrectionMode = config.CorrectionModeOff
	local := corrector.NewService(false, &sessionTestStrategy{
		name:  "local",
		apply: func(s string) (string, error) { return s, nil },
	})
	llm := corrector.NewService(false, &sessionTestStrategy{
		name:  "llm",
		apply: func(s string) (string, error) { return s, nil },
	})
	controller := &Controller{cfg: cfg, correctionService: local, llmService: llm}

	cases := []struct {
		mode      string
		wantLocal bool
		wantLLM   bool
	}{
		{config.CorrectionModeOff, false, false},
		{config.CorrectionModeLocal, true, false},
		{config.CorrectionModeLLM, false, true},
		{config.CorrectionModeCombined, true, true},
	}
	for _, tc := range cases {
		controller.SetCorrectionMode(tc.mode)
		if local.Enabled() != tc.wantLocal {
			t.Fatalf("mode %q: local enabled = %v, want %v", tc.mode, local.Enabled(), tc.wantLocal)
		}
		if llm.Enabled() != tc.wantLLM {
			t.Fatalf("mode %q: llm enabled = %v, want %v", tc.mode, llm.Enabled(), tc.wantLLM)
		}
	}
}
