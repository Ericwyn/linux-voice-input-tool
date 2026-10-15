package corrector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"linux-voice-input/config"
)

func TestNewServiceFromConfigDictionaryOnly(t *testing.T) {
	tempDir := t.TempDir()
	dictPath := filepath.Join(tempDir, "corrections.txt")
	if err := os.WriteFile(dictPath, []byte("线程 = 现成\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.CorrectionMode = config.CorrectionModeLocal
	cfg.Corrections.Strategy = ""
	cfg.Corrections.DictPath = dictPath

	service, err := NewServiceFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewServiceFromConfig() error: %v", err)
	}
	defer service.Close()
	if got := service.Correct(context.Background(), "开一个现成").Text; got != "开一个线程" {
		t.Fatalf("dictionary-only result = %q", got)
	}
}

func TestNewServiceFromConfigModelFailureStillRunsDictionary(t *testing.T) {
	tempDir := t.TempDir()
	dictPath := filepath.Join(tempDir, "corrections.txt")
	if err := os.WriteFile(dictPath, []byte("Redis = 瑞迪斯\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.CorrectionMode = config.CorrectionModeLocal
	cfg.Corrections.ModelDir = filepath.Join(tempDir, "missing-models")
	cfg.Corrections.DictPath = dictPath

	service, err := NewServiceFromConfig(cfg)
	if err != nil {
		t.Fatalf("lazy model construction should not fail: %v", err)
	}
	defer service.Close()
	result := service.Correct(context.Background(), "启动瑞迪斯")
	if result.Text != "启动Redis" {
		t.Fatalf("fail-open result = %q", result.Text)
	}
	if len(result.Failures) != 1 || result.Failures[0].Strategy != config.CorrectionStrategyMDCSpell {
		t.Fatalf("model failure not reported: %#v", result.Failures)
	}
}

func TestNewServiceFromConfigRejectsUnknownStrategy(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Corrections.Strategy = "unknown"
	if _, err := NewServiceFromConfig(cfg); err == nil {
		t.Fatal("unknown strategy should fail")
	}
}
