package settings

import (
	"encoding/json"
	"testing"

	"linux-voice-input/config"
)

// TestConfigToJSONRoundTrip verifies the full cfg -> JSON -> cfg loop preserves
// values across every field type the config uses, with special attention to
// integer fields (JSON numbers decode as float64 and must re-marshal as ints).
func TestConfigToJSONRoundTrip(t *testing.T) {
	original := config.DefaultConfig()
	original.CorrectionMode = config.CorrectionModeCombined
	original.LLMCorrection.BaseURL = "https://api.example.com/v1"
	original.LLMCorrection.APIKey = "sk-secret"
	original.LLMCorrection.Model = "gpt-4o-mini"
	original.LLMCorrection.TimeoutMs = 2000
	original.LLMCorrection.ExtraParams = map[string]any{
		"thinking": map[string]any{"type": "disabled"},
	}
	original.Hotkey = "alt+shift+d"
	original.Corrections.ConfidenceThreshold = 0.9
	original.SenseVoice.NumThreads = 4

	jsonBytes, err := ConfigToJSON(original)
	if err != nil {
		t.Fatalf("ConfigToJSON() error: %v", err)
	}

	restored, err := ConfigFromJSON(jsonBytes)
	if err != nil {
		t.Fatalf("ConfigFromJSON() error: %v", err)
	}

	if restored.CorrectionMode != config.CorrectionModeCombined {
		t.Fatalf("correction_mode = %q", restored.CorrectionMode)
	}
	if restored.LLMCorrection.BaseURL != original.LLMCorrection.BaseURL {
		t.Fatalf("base_url = %q", restored.LLMCorrection.BaseURL)
	}
	if restored.LLMCorrection.APIKey != "sk-secret" {
		t.Fatalf("api_key = %q", restored.LLMCorrection.APIKey)
	}
	// Integer field must survive the float64 detour as an int.
	if restored.LLMCorrection.TimeoutMs != 2000 {
		t.Fatalf("timeout_ms = %d, want 2000", restored.LLMCorrection.TimeoutMs)
	}
	if restored.SenseVoice.NumThreads != 4 {
		t.Fatalf("num_threads = %d, want 4", restored.SenseVoice.NumThreads)
	}
	// Float field preserved.
	if restored.Corrections.ConfidenceThreshold != 0.9 {
		t.Fatalf("confidence_threshold = %v, want 0.9", restored.Corrections.ConfidenceThreshold)
	}
	// Nested extra_params map preserved.
	thinking, ok := restored.LLMCorrection.ExtraParams["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("extra_params.thinking = %#v", restored.LLMCorrection.ExtraParams["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Fatalf("extra_params.thinking.type = %v", thinking["type"])
	}
	// Boolean preserved.
	if restored.HotkeyEnabled != original.HotkeyEnabled {
		t.Fatalf("hotkey_enabled = %v", restored.HotkeyEnabled)
	}
}

// TestConfigToJSONIsObjectWithYAMLKeys confirms the emitted JSON uses the YAML
// field names the frontend auto-renders against.
func TestConfigToJSONIsObjectWithYAMLKeys(t *testing.T) {
	jsonBytes, err := ConfigToJSON(config.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal(jsonBytes, &tree); err != nil {
		t.Fatalf("emitted JSON is not an object: %v", err)
	}
	for _, key := range []string{"engine", "correction_mode", "llm_correction", "hotkey", "remove_trailing_symbols"} {
		if _, ok := tree[key]; !ok {
			t.Fatalf("emitted JSON missing key %q", key)
		}
	}
	llm, ok := tree["llm_correction"].(map[string]any)
	if !ok {
		t.Fatalf("llm_correction not an object: %#v", tree["llm_correction"])
	}
	if _, ok := llm["timeout_ms"]; !ok {
		t.Fatal("llm_correction.timeout_ms missing")
	}
}

// TestConfigFromJSONOmittedKeysKeepDefaults verifies that a partial submission
// (frontend omits a key) inherits the default rather than a zero value.
func TestConfigFromJSONOmittedKeysKeepDefaults(t *testing.T) {
	// Only send the engine; everything else must come from defaults.
	restored, err := ConfigFromJSON([]byte(`{"engine":"whisper"}`))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Engine != "whisper" {
		t.Fatalf("engine = %q", restored.Engine)
	}
	// Default timeout must be present, not zero.
	if restored.LLMCorrection.TimeoutMs != 2000 {
		t.Fatalf("omitted timeout_ms = %d, want default 2000", restored.LLMCorrection.TimeoutMs)
	}
	if restored.LLMCorrection.PromptTemplate == "" {
		t.Fatal("omitted prompt_template lost its default")
	}
}

// TestConfigFromJSONRejectsInvalidJSON ensures malformed input is an error, not
// a silent empty config that could overwrite a good file.
func TestConfigFromJSONRejectsInvalidJSON(t *testing.T) {
	if _, err := ConfigFromJSON([]byte(`{not json`)); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}
