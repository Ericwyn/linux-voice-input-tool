package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDefaultCorrectionMode(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.CorrectionMode != CorrectionModeOff {
		t.Fatalf("default correction_mode = %q, want %q", cfg.CorrectionMode, CorrectionModeOff)
	}
	if cfg.LLMCorrection.TimeoutMs != 2000 {
		t.Fatalf("default llm timeout = %d, want 2000", cfg.LLMCorrection.TimeoutMs)
	}
	if cfg.LLMCorrection.PromptTemplate == "" {
		t.Fatal("default llm prompt template should be non-empty")
	}
}

func TestUseLocalAndLLMCorrectionTruthTable(t *testing.T) {
	cases := []struct {
		mode      string
		wantLocal bool
		wantLLM   bool
	}{
		{CorrectionModeOff, false, false},
		{CorrectionModeLocal, true, false},
		{CorrectionModeLLM, false, true},
		{CorrectionModeCombined, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			cfg := &Config{CorrectionMode: tc.mode}
			if got := cfg.UseLocalCorrection(); got != tc.wantLocal {
				t.Fatalf("UseLocalCorrection() = %v, want %v", got, tc.wantLocal)
			}
			if got := cfg.UseLLMCorrection(); got != tc.wantLLM {
				t.Fatalf("UseLLMCorrection() = %v, want %v", got, tc.wantLLM)
			}
		})
	}
}

func TestNormalizeCorrectionMode(t *testing.T) {
	cases := map[string]string{
		CorrectionModeOff:      CorrectionModeOff,
		CorrectionModeLocal:    CorrectionModeLocal,
		CorrectionModeLLM:      CorrectionModeLLM,
		CorrectionModeCombined: CorrectionModeCombined,
		"":                     CorrectionModeOff,
		"bogus":                CorrectionModeOff,
		"LOCAL":                CorrectionModeOff, // case-sensitive, unknown -> off
	}
	for in, want := range cases {
		if got := NormalizeCorrectionMode(in); got != want {
			t.Fatalf("NormalizeCorrectionMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLLMCorrectionComplete(t *testing.T) {
	complete := func() *Config {
		cfg := DefaultConfig()
		cfg.LLMCorrection.BaseURL = "https://api.example.com/v1"
		cfg.LLMCorrection.APIKey = "sk-xxx"
		cfg.LLMCorrection.Model = "gpt-4o-mini"
		return cfg
	}

	t.Run("all present", func(t *testing.T) {
		if ok, missing := complete().LLMCorrectionComplete(); !ok {
			t.Fatalf("expected complete, missing=%v", missing)
		}
	})

	t.Run("missing base_url", func(t *testing.T) {
		cfg := complete()
		cfg.LLMCorrection.BaseURL = "  "
		ok, missing := cfg.LLMCorrectionComplete()
		if ok || !contains(missing, "base_url") {
			t.Fatalf("ok=%v missing=%v", ok, missing)
		}
	})

	t.Run("missing api_key", func(t *testing.T) {
		cfg := complete()
		cfg.LLMCorrection.APIKey = ""
		ok, missing := cfg.LLMCorrectionComplete()
		if ok || !contains(missing, "api_key") {
			t.Fatalf("ok=%v missing=%v", ok, missing)
		}
	})

	t.Run("missing model", func(t *testing.T) {
		cfg := complete()
		cfg.LLMCorrection.Model = ""
		ok, missing := cfg.LLMCorrectionComplete()
		if ok || !contains(missing, "model") {
			t.Fatalf("ok=%v missing=%v", ok, missing)
		}
	})

	t.Run("empty prompt", func(t *testing.T) {
		cfg := complete()
		cfg.LLMCorrection.PromptTemplate = ""
		ok, missing := cfg.LLMCorrectionComplete()
		if ok || !contains(missing, "prompt_template") {
			t.Fatalf("ok=%v missing=%v", ok, missing)
		}
	})

	t.Run("prompt missing placeholder", func(t *testing.T) {
		cfg := complete()
		cfg.LLMCorrection.PromptTemplate = "纠正这段话"
		ok, missing := cfg.LLMCorrectionComplete()
		if ok {
			t.Fatal("prompt without placeholder should be incomplete")
		}
		// label includes the placeholder hint
		found := false
		for _, m := range missing {
			if len(m) >= len("prompt_template") && m[:len("prompt_template")] == "prompt_template" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing=%v, want a prompt_template entry", missing)
		}
	})
}

func TestLLMCorrectionTimeout(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.LLMCorrectionTimeout(); got != 2000*time.Millisecond {
		t.Fatalf("default timeout = %v", got)
	}
	cfg.LLMCorrection.TimeoutMs = 0
	if got := cfg.LLMCorrectionTimeout(); got != 2000*time.Millisecond {
		t.Fatalf("zero timeout should fall back to 2s, got %v", got)
	}
	cfg.LLMCorrection.TimeoutMs = -5
	if got := cfg.LLMCorrectionTimeout(); got != 2000*time.Millisecond {
		t.Fatalf("negative timeout should fall back to 2s, got %v", got)
	}
	cfg.LLMCorrection.TimeoutMs = 500
	if got := cfg.LLMCorrectionTimeout(); got != 500*time.Millisecond {
		t.Fatalf("timeout = %v, want 500ms", got)
	}
}

// TestMigrateCorrectionMode covers the single-select migration from the legacy
// corrections.enabled flag when correction_mode is absent, present, or invalid.
func TestMigrateCorrectionMode(t *testing.T) {
	t.Run("absent key with enabled true -> local", func(t *testing.T) {
		cfg := DefaultConfig()
		raw := []byte("corrections:\n  enabled: true\n")
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			t.Fatal(err)
		}
		changed := migrateCorrectionMode(raw, cfg)
		if !changed || cfg.CorrectionMode != CorrectionModeLocal {
			t.Fatalf("changed=%v mode=%q, want true/local", changed, cfg.CorrectionMode)
		}
	})

	t.Run("absent key with enabled false -> off", func(t *testing.T) {
		cfg := DefaultConfig()
		raw := []byte("engine: sensevoice\n")
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			t.Fatal(err)
		}
		changed := migrateCorrectionMode(raw, cfg)
		if !changed || cfg.CorrectionMode != CorrectionModeOff {
			t.Fatalf("changed=%v mode=%q, want true/off", changed, cfg.CorrectionMode)
		}
	})

	t.Run("present valid mode is kept", func(t *testing.T) {
		cfg := DefaultConfig()
		raw := []byte("correction_mode: combined\n")
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			t.Fatal(err)
		}
		changed := migrateCorrectionMode(raw, cfg)
		if changed || cfg.CorrectionMode != CorrectionModeCombined {
			t.Fatalf("changed=%v mode=%q, want false/combined", changed, cfg.CorrectionMode)
		}
	})

	t.Run("present invalid mode normalized to off", func(t *testing.T) {
		cfg := DefaultConfig()
		raw := []byte("correction_mode: bogus\n")
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			t.Fatal(err)
		}
		changed := migrateCorrectionMode(raw, cfg)
		if !changed || cfg.CorrectionMode != CorrectionModeOff {
			t.Fatalf("changed=%v mode=%q, want true/off", changed, cfg.CorrectionMode)
		}
	})
}

func TestOldYAMLKeepsLLMDefaults(t *testing.T) {
	// A legacy config without an llm_correction block must inherit defaults.
	cfg := DefaultConfig()
	data := []byte("engine: sensevoice\n")
	if err := yaml.Unmarshal(data, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.LLMCorrection.TimeoutMs != 2000 {
		t.Fatalf("legacy llm timeout = %d, want 2000", cfg.LLMCorrection.TimeoutMs)
	}
	if cfg.LLMCorrection.PromptTemplate == "" {
		t.Fatal("legacy config lost the default prompt template")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
