package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultRemoveTrailingSymbols(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.RemoveTrailingSymbols {
		t.Fatal("remove_trailing_symbols should default to on")
	}
}

func TestRemoveTrailingSymbolsCanBeDisabled(t *testing.T) {
	cfg := DefaultConfig()
	data := []byte("remove_trailing_symbols: false\n")
	if err := yaml.Unmarshal(data, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.RemoveTrailingSymbols {
		t.Fatal("remove_trailing_symbols: false was not loaded")
	}
}

func TestOldYAMLKeepsRemoveTrailingSymbolsDefault(t *testing.T) {
	// A legacy config that predates the option must inherit the default (on),
	// since DefaultConfig seeds the struct before unmarshalling.
	cfg := DefaultConfig()
	data := []byte("engine: sensevoice\n")
	if err := yaml.Unmarshal(data, cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.RemoveTrailingSymbols {
		t.Fatal("legacy config should keep remove_trailing_symbols on by default")
	}
}
