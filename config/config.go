package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type APIConfig struct {
	Key     string `yaml:"key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type AudioConfig struct {
	SampleRate  int `yaml:"sample_rate"`
	MaxDuration int `yaml:"max_duration"`
}

type Config struct {
	API              APIConfig   `yaml:"api"`
	Hotkey           string      `yaml:"hotkey"`
	Audio            AudioConfig `yaml:"audio"`
	RestoreClipboard bool        `yaml:"restore_clipboard"`
}

func DefaultConfig() *Config {
	return &Config{
		API: APIConfig{
			Key:     "",
			BaseURL: "https://api.siliconflow.cn/v1",
			Model:   "FunAudioLLM/SenseVoiceSmall",
		},
		Hotkey: "alt+shift+g",
		Audio: AudioConfig{
			SampleRate:  16000,
			MaxDuration: 60,
		},
		RestoreClipboard: true,
	}
}

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "linux-voice-input")
}

func ConfigPath() string {
	return filepath.Join(configDir(), "config.yaml")
}

func Load() (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			if err := Save(cfg); err != nil {
				return nil, err
			}
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Save(cfg *Config) error {
	dir := configDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), data, 0644)
}
