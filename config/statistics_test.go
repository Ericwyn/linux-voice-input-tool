package config

import (
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultStatisticsConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Statistics.Enabled {
		t.Fatal("statistics should be opt-in")
	}
	if got, want := StatsDir(), filepath.Join(AppDir(), "stats"); got != want {
		t.Fatalf("StatsDir() = %q, want %q", got, want)
	}
}

func TestOldYAMLKeepsStatisticsDisabled(t *testing.T) {
	cfg := DefaultConfig()
	data := []byte("engine: sensevoice\n")
	if err := yaml.Unmarshal(data, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Statistics.Enabled {
		t.Fatal("legacy config unexpectedly enabled statistics")
	}
}

func TestStatisticsYAMLEnablesCollection(t *testing.T) {
	cfg := DefaultConfig()
	data := []byte("statistics:\n  enabled: true\n")
	if err := yaml.Unmarshal(data, cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Statistics.Enabled {
		t.Fatal("statistics.enabled was not loaded")
	}
}
