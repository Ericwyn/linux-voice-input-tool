package main

import (
	"os"
	"path/filepath"
	"testing"

	"linux-voice-input/config"
)

func TestApplyConfiguredCorrectionsInTestMode(t *testing.T) {
	dictPath := filepath.Join(t.TempDir(), "corrections.txt")
	if err := os.WriteFile(dictPath, []byte("线程 = 现成\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Corrections.Enabled = true
	cfg.Corrections.Strategy = ""
	cfg.Corrections.DictPath = dictPath

	if got := applyConfiguredCorrections(cfg, "开一个现成"); got != "开一个线程" {
		t.Fatalf("applyConfiguredCorrections() = %q", got)
	}
}

func TestCorrectionIntegrationUsesAlreadyLoadedONNXRuntime(t *testing.T) {
	if os.Getenv("CORRECTION_INTEGRATION") != "1" {
		t.Skip("set CORRECTION_INTEGRATION=1 after preparing the default MDCSpell model")
	}
	cfg := config.DefaultConfig()
	cfg.Corrections.Enabled = true
	// A plain development binary has no sibling lib directory. The correction
	// runner should discover the libonnxruntime already loaded by sherpa-onnx.
	cfg.Corrections.RuntimePath = "/definitely/missing/libonnxruntime.so"

	if got := applyConfiguredCorrections(cfg, "这项功能已经部属完成"); got != "这项功能已经部署完成" {
		t.Fatalf("integration correction = %q", got)
	}
}
