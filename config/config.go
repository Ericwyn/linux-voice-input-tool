package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type APIConfig struct {
	Engine  string `yaml:"engine"` // "whisper" (online) or "sherpa" (local streaming)
	Key     string `yaml:"key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type SherpaConfig struct {
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
}

func defaultModelDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".linux-voice-input", "models",
		"sherpa-onnx-streaming-paraformer-bilingual-zh-en")
}

func DefaultConfig() *Config {
	modelDir := defaultModelDir()
	home, _ := os.UserHomeDir()
	offlineModelDir := filepath.Join(home, ".linux-voice-input", "models",
		"sherpa-onnx-paraformer-zh-2023-09-14")
	punctModelDir := filepath.Join(home, ".linux-voice-input", "models",
		"sherpa-onnx-punct-ct-transformer-zh-en-vocab272727-2024-04-12")
	return &Config{
		API: APIConfig{
			Engine:  "sherpa",
			Key:     "",
			BaseURL: "https://api.siliconflow.cn/v1",
			Model:   "FunAudioLLM/SenseVoiceSmall",
		},
		Sherpa: SherpaConfig{
			Encoder:                 filepath.Join(modelDir, "encoder.int8.onnx"),
			Decoder:                 filepath.Join(modelDir, "decoder.int8.onnx"),
			Tokens:                  filepath.Join(modelDir, "tokens.txt"),
			NumThreads:              4,
			Rule1MinTrailingSilence: 2.4,
			Rule2MinTrailingSilence: 1.2,
			OfflineModel:            filepath.Join(offlineModelDir, "model.int8.onnx"),
			OfflineTokens:           filepath.Join(offlineModelDir, "tokens.txt"),
			PunctModel:              filepath.Join(punctModelDir, "model.onnx"),
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

func Load() (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(ConfigPath())
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
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
