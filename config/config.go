package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"linux-voice-input/models"
)

// WhisperConfig holds the settings for the online whisper engine (SiliconFlow).
// It is one engine block among the peers (sherpa / sensevoice), symmetric with
// them; the global engine selector lives at the config root, not here.
type WhisperConfig struct {
	Key     string `yaml:"key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

// legacyAPIBlock captures the pre-migration top-level `api:` block, which mixed
// the global engine selector with whisper's own fields. Load reads it only to
// migrate old configs into the new engine/whisper layout, then clears it so it
// is never written back.
type legacyAPIBlock struct {
	Engine  string `yaml:"engine"`
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

// SenseVoiceConfig configures the local SenseVoice engine: one non-streaming
// model that produces ASR text with punctuation (via inverse text
// normalization) in a single pass.
type SenseVoiceConfig struct {
	// ModelDir is the base directory holding the SenseVoice bundle. When empty
	// it falls back to SherpaConfig.ModelDir (both engines share one models
	// directory by default). Model and Tokens are derived from it.
	ModelDir string `yaml:"model_dir"`
	Model    string `yaml:"model"`
	Tokens   string `yaml:"tokens"`

	// Language is the recognition language: auto/zh/en/yue/ja/ko. "auto" lets
	// the model detect among Chinese/Cantonese/English/Japanese/Korean.
	Language string `yaml:"language"`
	// UseITN enables inverse text normalization, which also yields punctuation.
	UseITN     bool `yaml:"use_itn"`
	NumThreads int  `yaml:"num_threads"`

	// PreviewIntervalMs is the period, in milliseconds, at which the accumulated
	// audio is re-recognized to produce a live preview. A value > 0 enables the
	// pseudo-streaming preview; <= 0 disables it (recognition then runs only
	// once when recording stops).
	PreviewIntervalMs int `yaml:"preview_interval_ms"`
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
	// Engine is the global recognition engine selector: "sensevoice", "sherpa",
	// or "whisper". It lives at the root because it chooses among the engine
	// blocks below rather than belonging to any one of them.
	Engine string `yaml:"engine"`

	Sherpa     SherpaConfig     `yaml:"sherpa"`
	SenseVoice SenseVoiceConfig `yaml:"sensevoice"`
	Whisper    WhisperConfig    `yaml:"whisper"`

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
	sensePaths := models.SenseVoicePathsFor(baseDir)
	return &Config{
		// Default to the local SenseVoice engine: a single non-streaming model
		// covering ASR plus punctuation, the lightest local option.
		Engine: "sensevoice",
		Whisper: WhisperConfig{
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
		SenseVoice: SenseVoiceConfig{
			ModelDir:          baseDir,
			Model:             sensePaths.Model,
			Tokens:            sensePaths.Tokens,
			Language:          "auto",
			UseITN:            true,
			NumThreads:        4,
			PreviewIntervalMs: 1000,
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

// RuntimeLibDir returns the directory in which the sherpa-onnx native shared
// libraries should live for the binary to find them via its baked-in
// $ORIGIN/lib rpath: a `lib` subdirectory next to the executable. It falls back
// to a relative `lib` when the executable path cannot be determined. Both the
// --download-models CLI path and the settings window use it so the library
// lands where the loader looks.
func RuntimeLibDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "lib"
	}
	return filepath.Join(filepath.Dir(exePath), "lib")
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

// ResolveModelPaths derives the concrete model file paths from the configured
// model directories, making the single directory the source of truth. Explicit
// per-file paths in the config are overridden. Sherpa paths derive from
// Sherpa.ModelDir; SenseVoice paths derive from SenseVoice.ModelDir, falling
// back to Sherpa.ModelDir when the former is empty (both engines share one
// models directory by default). If a base directory is empty (e.g. a legacy
// config), the corresponding explicit paths are kept as-is.
func (c *Config) ResolveModelPaths() {
	if c.Sherpa.ModelDir != "" {
		paths := models.Paths(c.Sherpa.ModelDir)
		c.Sherpa.Encoder = paths.Encoder
		c.Sherpa.Decoder = paths.Decoder
		c.Sherpa.Tokens = paths.Tokens
		c.Sherpa.OfflineModel = paths.OfflineModel
		c.Sherpa.OfflineTokens = paths.OfflineTokens
		c.Sherpa.PunctModel = paths.PunctModel
	}

	senseDir := c.SenseVoice.ModelDir
	if senseDir == "" {
		senseDir = c.Sherpa.ModelDir
	}
	if senseDir != "" {
		sensePaths := models.SenseVoicePathsFor(senseDir)
		c.SenseVoice.ModelDir = senseDir
		c.SenseVoice.Model = sensePaths.Model
		c.SenseVoice.Tokens = sensePaths.Tokens
	}
}

// ModelBaseDir returns the base directory holding the configured engine's local
// models. SenseVoice uses its own ModelDir when set, otherwise both local
// engines share the sherpa models directory; a final fallback is the default.
func (c *Config) ModelBaseDir() string {
	if c.Engine == "sensevoice" && c.SenseVoice.ModelDir != "" {
		return c.SenseVoice.ModelDir
	}
	if c.Sherpa.ModelDir != "" {
		return c.Sherpa.ModelDir
	}
	return DefaultModelDir()
}

// migrateLegacyAPI detects the pre-migration top-level `api:` block in raw and,
// if present, folds its values into the new engine/whisper layout on cfg: the
// engine selector moves to the root, and the whisper-specific fields move into
// the whisper block. It only overrides fields the old block actually set, so
// defaults survive. It returns true when a legacy block was found, signaling the
// caller to rewrite the file in the new format (which no longer has `api:`).
func migrateLegacyAPI(raw []byte, cfg *Config) bool {
	var probe struct {
		API *legacyAPIBlock `yaml:"api"`
	}
	if err := yaml.Unmarshal(raw, &probe); err != nil || probe.API == nil {
		return false
	}
	if probe.API.Engine != "" {
		cfg.Engine = probe.API.Engine
	}
	if probe.API.Key != "" {
		cfg.Whisper.Key = probe.API.Key
	}
	if probe.API.BaseURL != "" {
		cfg.Whisper.BaseURL = probe.API.BaseURL
	}
	if probe.API.Model != "" {
		cfg.Whisper.Model = probe.API.Model
	}
	return true
}

func Load() (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(ConfigPath())
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
		migrated := migrateLegacyAPI(data, cfg)
		cfg.ResolveModelPaths()
		// Rewrite the file when we upgraded an old `api:` block, so the new
		// engine/whisper layout is persisted and the migration runs only once.
		if migrated {
			if err := Save(cfg); err != nil {
				return nil, err
			}
		}
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
			migrateLegacyAPI(data, cfg)
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
