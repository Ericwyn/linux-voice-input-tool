package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"linux-voice-input/config"
)

func TestCheckCorrectionsDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	if got := checkCorrections(cfg); len(got) != 0 {
		t.Fatalf("disabled correction checks = %#v", got)
	}
}

func TestCheckCorrectionsReportsMissingFiles(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CorrectionMode = config.CorrectionModeLocal
	cfg.Corrections.ModelDir = t.TempDir()
	cfg.Corrections.RuntimePath = filepath.Join(t.TempDir(), "libonnxruntime.so")
	got := checkCorrections(cfg)
	if len(got) != 1 || got[0].OK {
		t.Fatalf("missing correction result = %#v", got)
	}
}

func TestCheckCorrectionsReady(t *testing.T) {
	root := t.TempDir()
	runtimePath := filepath.Join(root, "libonnxruntime.so")
	cfg := config.DefaultConfig()
	cfg.CorrectionMode = config.CorrectionModeLocal
	cfg.Corrections.ModelDir = filepath.Join(root, "models")
	cfg.Corrections.RuntimePath = runtimePath
	paths, err := cfg.CorrectionModelPaths(cfg.Corrections.Strategy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Model), 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.Model, paths.Vocab, runtimePath} {
		if err := os.WriteFile(path, []byte("stub"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got := checkCorrections(cfg)
	if len(got) != 1 || !got[0].OK {
		t.Fatalf("ready correction result = %#v", got)
	}
}
