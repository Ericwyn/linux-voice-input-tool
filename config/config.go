package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"linux-voice-input/models"
)

type APIConfig struct {
	Engine  string `yaml:"engine"` // "whisper" (online) or "sherpa" (local streaming)
	Key     string `yaml:"key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type SherpaConfig struct {
	// ModelDir is the single base directory that holds all sherpa model
	// bundles. When set, the six concrete paths below are derived from it, so
	// users only need to configure one path.
	ModelDir                string  `yaml:"model_dir"`
	Encoder                 string  `yaml:"encoder"`
	Decoder                 string  `yaml:"decoder"`
	Tokens                  string  `yaml:"tokens"`
	NumThreads              int     `yaml:"num_threads"`
	Rule1MinTrailingSilence float32 `yaml:"rule1_min_trailing_silence"`
	Rule2MinTrailingSilence float32 `yaml:"rule2_min_trailing_silence"`
	OfflineModel            string  `yaml:"offline_model"`
	OfflineTokens           string  `yaml:"offline_tokens"`
	PunctModel              string  `yaml:"punct_model"`
}

type AudioConfig struct {
	SampleRate int `yaml:"sample_rate"`
}

type NotificationsConfig struct {
	Start             bool `yaml:"start"`
	Success           bool `yaml:"success"`
	Ignored           bool `yaml:"ignored"`
	RecordStartFailed bool `yaml:"record_start_failed"`
	RecordStopFailed  bool `yaml:"record_stop_failed"`
	SaveFailed        bool `yaml:"save_failed"`
	TranscribeFailed  bool `yaml:"transcribe_failed"`
	EmptyResult       bool `yaml:"empty_result"`
	PasteFailed       bool `yaml:"paste_failed"`
}

type Config struct {
	API              APIConfig           `yaml:"api"`
	Sherpa           SherpaConfig        `yaml:"sherpa"`
	Audio            AudioConfig         `yaml:"audio"`
	Notifications    NotificationsConfig `yaml:"notifications"`
	RestoreClipboard bool                `yaml:"restore_clipboard"`
	// Hotkey is the global hotkey combination that toggles recording,
	// e.g. "alt+shift+g". Modifiers: ctrl/alt/shift/super.
	Hotkey string `yaml:"hotkey"`
	// HotkeyEnabled controls whether the global hotkey listener is active
	// when the daemon starts. It can be toggled at runtime from the tray.
	HotkeyEnabled bool `yaml:"hotkey_enabled"`
}

// DefaultModelDir returns the base directory that holds all sherpa model
// bundles: ~/.linux-voice-input/models.
func DefaultModelDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "models"
	}
	return filepath.Join(home, ".linux-voice-input", "models")
}

func DefaultConfig() *Config {
	baseDir := DefaultModelDir()
	paths := models.Paths(baseDir)
	return &Config{
		API: APIConfig{
			Engine:  "sherpa",
			Key:     "",
			BaseURL: "https://api.siliconflow.cn/v1",
			Model:   "FunAudioLLM/SenseVoiceSmall",
		},
		Sherpa: SherpaConfig{
			ModelDir:                baseDir,
			Encoder:                 paths.Encoder,
			Decoder:                 paths.Decoder,
			Tokens:                  paths.Tokens,
			NumThreads:              4,
			Rule1MinTrailingSilence: 2.4,
			Rule2MinTrailingSilence: 1.2,
			OfflineModel:            paths.OfflineModel,
			OfflineTokens:           paths.OfflineTokens,
			PunctModel:              paths.PunctModel,
		},
		Audio: AudioConfig{
			SampleRate: 16000,
		},
		Notifications: NotificationsConfig{
			Start:             true,
			Success:           true,
			Ignored:           true,
			RecordStartFailed: true,
			RecordStopFailed:  true,
			SaveFailed:        true,
			TranscribeFailed:  true,
			EmptyResult:       true,
			PasteFailed:       true,
		},
		RestoreClipboard: true,
		Hotkey:           "alt+shift+g",
		HotkeyEnabled:    true,
	}
}

func AppDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "linux-voice-input")
	}
	return filepath.Join(home, ".linux-voice-input")
}

func ConfigPath() string {
	return filepath.Join(AppDir(), "config.yaml")
}

func legacyConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "linux-voice-input", "config.yaml")
}

// ResolveModelPaths derives the six sherpa file paths from ModelDir when it is
// set, making the single directory the source of truth. Explicit per-file
// paths in the config are overridden. If ModelDir is empty (e.g. a legacy
// config), the existing explicit paths are kept as-is.
func (c *Config) ResolveModelPaths() {
	if c.Sherpa.ModelDir == "" {
		return
	}
	paths := models.Paths(c.Sherpa.ModelDir)
	c.Sherpa.Encoder = paths.Encoder
	c.Sherpa.Decoder = paths.Decoder
	c.Sherpa.Tokens = paths.Tokens
	c.Sherpa.OfflineModel = paths.OfflineModel
	c.Sherpa.OfflineTokens = paths.OfflineTokens
	c.Sherpa.PunctModel = paths.PunctModel
}

func Load() (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(ConfigPath())
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
		cfg.ResolveModelPaths()
		return cfg, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	legacyPath := legacyConfigPath()
	if legacyPath != "" {
		data, legacyErr := os.ReadFile(legacyPath)
		if legacyErr == nil {
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, err
			}
			cfg.ResolveModelPaths()
			if err := Save(cfg); err != nil {
				return nil, err
			}
			return cfg, nil
		}
		if !os.IsNotExist(legacyErr) {
			return nil, legacyErr
		}
	}

	if err := Save(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Save(cfg *Config) error {
	dir := AppDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), data, 0644)
}
