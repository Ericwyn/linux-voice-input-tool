package config

import (
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultCorrectionsConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Corrections.Enabled {
		t.Fatal("corrections should be opt-in")
	}
	if cfg.Corrections.Strategy != CorrectionStrategyMDCSpell {
		t.Fatalf("strategy = %q, want %q", cfg.Corrections.Strategy, CorrectionStrategyMDCSpell)
	}
	if cfg.Corrections.ConfidenceThreshold != 0.90 || cfg.Corrections.MinMargin != 0.20 {
		t.Fatalf("thresholds = %v/%v", cfg.Corrections.ConfidenceThreshold, cfg.Corrections.MinMargin)
	}
	if cfg.Corrections.MaxLength != 128 || cfg.Corrections.TimeoutMs != 1500 || cfg.Corrections.NumThreads != 4 {
		t.Fatalf("limits = max:%d timeout:%d threads:%d", cfg.Corrections.MaxLength, cfg.Corrections.TimeoutMs, cfg.Corrections.NumThreads)
	}
}

func TestCorrectionModelPathsForTopTwo(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Corrections.ModelDir = "/tmp/correction-models"

	tests := []struct {
		strategy string
		dir      string
	}{
		{CorrectionStrategyMDCSpell, "macbert-mdcspell-v2"},
		{CorrectionStrategyMacBERT, "macbert4csc"},
	}
	for _, test := range tests {
		paths, err := cfg.CorrectionModelPaths(test.strategy)
		if err != nil {
			t.Fatalf("CorrectionModelPaths(%q): %v", test.strategy, err)
		}
		wantDir := filepath.Join(cfg.Corrections.ModelDir, test.dir)
		if paths.Model != filepath.Join(wantDir, "model.int8.onnx") || paths.Vocab != filepath.Join(wantDir, "vocab.txt") {
			t.Fatalf("paths for %s = %#v", test.strategy, paths)
		}
	}

	if _, err := cfg.CorrectionModelPaths("unknown"); err == nil {
		t.Fatal("unknown strategy should return an error")
	}
}

func TestOldCorrectionsYAMLKeepsNewDefaults(t *testing.T) {
	cfg := DefaultConfig()
	data := []byte("corrections:\n  enabled: true\n  dict_path: /tmp/corrections.txt\n")
	if err := yaml.Unmarshal(data, cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Corrections.Enabled || cfg.Corrections.DictPath != "/tmp/corrections.txt" {
		t.Fatalf("legacy fields not loaded: %#v", cfg.Corrections)
	}
	if cfg.Corrections.Strategy != CorrectionStrategyMDCSpell || cfg.Corrections.MaxLength != 128 {
		t.Fatalf("new defaults lost after old YAML: %#v", cfg.Corrections)
	}
}

func TestCorrectionsRuntimePath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Corrections.RuntimePath = "/custom/libonnxruntime.so"
	if got := cfg.CorrectionsRuntimePath(); got != cfg.Corrections.RuntimePath {
		t.Fatalf("explicit runtime path = %q", got)
	}

	cfg.Corrections.RuntimePath = ""
	base := filepath.Base(cfg.CorrectionsRuntimePath())
	want := "libonnxruntime.so"
	if runtime.GOOS == "darwin" {
		want = "libonnxruntime.dylib"
	} else if runtime.GOOS == "windows" {
		want = "onnxruntime.dll"
	}
	if base != want {
		t.Fatalf("runtime library = %q, want base %q", base, want)
	}
}
