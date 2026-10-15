// Package settings hosts the on-demand local configuration web UI: a small
// HTTP server bound to loopback that serves an embedded single-page form and a
// read/write config API. It lets users edit every config field in a browser
// instead of hand-editing YAML, then triggers the daemon's existing reload.
package settings

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"

	"linux-voice-input/config"
)

// ConfigToJSON renders the config as a JSON object keyed by the YAML field
// names. It deliberately routes config -> YAML -> generic map -> JSON so it
// reuses the existing `yaml:"..."` tags as the single source of truth: any new
// config field shows up automatically with no schema duplication here or in the
// frontend (which auto-renders whatever keys it receives).
func ConfigToJSON(cfg *config.Config) ([]byte, error) {
	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config to yaml: %w", err)
	}
	var tree map[string]any
	if err := yaml.Unmarshal(yamlBytes, &tree); err != nil {
		return nil, fmt.Errorf("yaml to map: %w", err)
	}
	jsonBytes, err := json.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("map to json: %w", err)
	}
	return jsonBytes, nil
}

// ConfigFromJSON parses a frontend JSON object back into a *config.Config. It
// seeds config.DefaultConfig() first and overlays the submitted values via the
// YAML layer, mirroring config.Load's "overlay on defaults" behavior so any key
// the frontend omits keeps its default rather than becoming a zero value.
//
// The conversion goes JSON -> generic map -> YAML -> typed struct. Numbers that
// JSON decodes as float64 are re-encoded by the YAML marshaller as plain
// integers when they are whole (e.g. 2000, 4), so integer config fields such as
// timeout_ms and num_threads unmarshal correctly; convert_test.go pins this.
func ConfigFromJSON(data []byte) (*config.Config, error) {
	var tree map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	yamlBytes, err := yaml.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("map to yaml: %w", err)
	}
	cfg := config.DefaultConfig()
	if err := yaml.Unmarshal(yamlBytes, cfg); err != nil {
		return nil, fmt.Errorf("yaml to config: %w", err)
	}
	return cfg, nil
}
