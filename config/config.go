package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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

	// PreviewIntervalMs is the base period, in milliseconds, at which the
	// accumulated audio is re-recognized to produce a live preview. A value > 0
	// enables the pseudo-streaming preview; <= 0 disables it (recognition then
	// runs only once when recording stops). It is the floor of the adaptive
	// schedule: short recordings re-run at this cadence.
	PreviewIntervalMs int `yaml:"preview_interval_ms"`

	// PreviewMaxIntervalMs caps the adaptive preview cadence. Because SenseVoice
	// is non-streaming, each preview re-recognizes the whole accumulated buffer,
	// so a single re-run grows with recording length. To stop long recordings
	// from monopolizing the CPU, the next re-run is delayed in proportion to how
	// long the last one took (see PreviewBackoffFactor), but never beyond this
	// ceiling. <= 0 disables the ceiling.
	PreviewMaxIntervalMs int `yaml:"preview_max_interval_ms"`

	// PreviewBackoffFactor sets how much idle time to leave between preview
	// re-runs, as a multiple of the last re-run's duration: the wait before the
	// next re-run is lastRun * factor (floored at PreviewIntervalMs, capped at
	// PreviewMaxIntervalMs). A factor of 1.0 keeps the CPU duty cycle around 50%
	// for long recordings. <= 0 falls back to 1.0.
	PreviewBackoffFactor float64 `yaml:"preview_backoff_factor"`
}

// CorrectionsConfig configures the engine-agnostic final-text correction
// service: one optional neural BERT strategy followed by a user dictionary.
type CorrectionsConfig struct {
	// Enabled turns the whole service on. When false neither model nor
	// dictionary is called and the recognized text is pasted unchanged.
	Enabled bool `yaml:"enabled"`
	// Strategy selects the optional neural correction model. Supported values
	// are macbert-mdcspell-v2 and macbert4csc. An explicit empty value keeps the
	// service in dictionary-only mode.
	Strategy string `yaml:"strategy"`
	// ModelDir contains one subdirectory per strategy. When empty it defaults to
	// <DefaultModelDir>/correction.
	ModelDir string `yaml:"model_dir"`
	// RuntimePath points at libonnxruntime. When empty it is resolved next to the
	// executable, sharing the runtime already bundled for sherpa-onnx.
	RuntimePath string `yaml:"runtime_path"`
	// ConfidenceThreshold and MinMargin limit model-driven rewrites. Both must
	// pass so the service favors precision and low over-correction.
	ConfidenceThreshold float64 `yaml:"confidence_threshold"`
	MinMargin           float64 `yaml:"min_margin"`
	MaxLength           int     `yaml:"max_length"`
	TimeoutMs           int     `yaml:"timeout_ms"`
	NumThreads          int     `yaml:"num_threads"`
	// DictPath is the correction dictionary file. It is hot-reloaded on change,
	// so edits take effect without restarting the daemon. When empty it
	// defaults to <AppDir>/corrections.txt.
	DictPath string `yaml:"dict_path"`
}

const (
	CorrectionStrategyMDCSpell = models.CorrectionStrategyMDCSpell
	CorrectionStrategyMacBERT  = models.CorrectionStrategyMacBERT
)

// Correction modes select the final-text correction pipeline. They form a
// single-select group (like the engine selector) rather than independent
// toggles, so the active pipeline is always unambiguous.
const (
	// CorrectionModeOff disables all correction; recognized text is pasted
	// unchanged (except for the unconditional trailing-symbol trim).
	CorrectionModeOff = "off"
	// CorrectionModeLocal runs the local BERT + dictionary pass only.
	CorrectionModeLocal = "local"
	// CorrectionModeLLM runs the LLM pass only.
	CorrectionModeLLM = "llm"
	// CorrectionModeCombined runs the local pass first, then the LLM pass.
	CorrectionModeCombined = "combined"
)

// LLMCorrectionPlaceholder is the token in the prompt template replaced by the
// recognized text before the request is sent.
const LLMCorrectionPlaceholder = "{{text}}"

// defaultLLMPromptTemplate is a ready-to-use correction prompt. It instructs the
// model to output only the corrected text so the response can be pasted
// verbatim, and injects the recognized text at the placeholder.
const defaultLLMPromptTemplate = "你是一个中文语音转写纠错助手。" +
	"请将下面这段语音识别结果中的错别字、同音词、多字漏字和明显的口语顺序问题纠正过来，使其通顺准确。" +
	"只输出纠正后的文本，不要解释，不要加引号。\n\n原文：{{text}}"

// LLMCorrectionConfig configures the optional LLM-based final-text correction.
// It calls an OpenAI-compatible /v1/chat/completions endpoint after the local
// pass. Any failure or timeout falls open to the input text, so a flaky network
// never costs the user the recognized result.
type LLMCorrectionConfig struct {
	// BaseURL is the OpenAI-compatible API base, e.g. https://api.openai.com/v1.
	// The client appends "/chat/completions".
	BaseURL string `yaml:"base_url"`
	// APIKey is sent as "Authorization: Bearer <key>".
	APIKey string `yaml:"api_key"`
	// Model is the chat model name, e.g. gpt-4o-mini.
	Model string `yaml:"model"`
	// PromptTemplate must contain LLMCorrectionPlaceholder, replaced by the
	// recognized text before the request is sent.
	PromptTemplate string `yaml:"prompt_template"`
	// TimeoutMs is a hard timeout for the whole request; on timeout the input
	// text is returned unchanged. Defaults to 2000 when non-positive.
	TimeoutMs int `yaml:"timeout_ms"`
	// ExtraParams is merged into the request JSON, letting the user set or
	// override arbitrary fields (max_tokens, top_p, even temperature/model) for
	// broad server compatibility. The messages field is always authoritative
	// and cannot be overridden.
	ExtraParams map[string]any `yaml:"extra_params"`
}

// CorrectionModelFiles are the two files required by a BERT MLM strategy.
type CorrectionModelFiles struct {
	Model string
	Vocab string
}

type AudioConfig struct {
	SampleRate int `yaml:"sample_rate"`
}

// StatisticsConfig controls privacy-preserving local usage aggregation. The
// data directory is intentionally fixed under AppDir so configuration only
// needs an opt-in switch and historical files remain easy to find.
type StatisticsConfig struct {
	Enabled bool `yaml:"enabled"`
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

	Audio         AudioConfig         `yaml:"audio"`
	Notifications NotificationsConfig `yaml:"notifications"`
	// CorrectionMode selects the final-text correction pipeline:
	// off | local | llm | combined. It is the single source of truth for
	// whether the local and/or LLM passes run, replacing the old
	// corrections.enabled boolean (migrated on load).
	CorrectionMode   string              `yaml:"correction_mode"`
	Corrections      CorrectionsConfig   `yaml:"corrections"`
	LLMCorrection    LLMCorrectionConfig `yaml:"llm_correction"`
	Statistics       StatisticsConfig    `yaml:"statistics"`
	RestoreClipboard bool                `yaml:"restore_clipboard"`
	// RemoveTrailingSymbols strips any trailing punctuation and whitespace from
	// the final recognized text before pasting. Speech recognition (and the
	// SenseVoice ITN pass in particular) tends to append a sentence-ending
	// period even when the user has not finished a thought, so trimming it lets
	// the user add their own punctuation and spacing. On by default.
	RemoveTrailingSymbols bool `yaml:"remove_trailing_symbols"`
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
			ModelDir:             baseDir,
			Model:                sensePaths.Model,
			Tokens:               sensePaths.Tokens,
			Language:             "auto",
			UseITN:               true,
			NumThreads:           4,
			PreviewIntervalMs:    1000,
			PreviewMaxIntervalMs: 5000,
			PreviewBackoffFactor: 1.0,
		},
		Audio: AudioConfig{
			SampleRate: 16000,
		},
		// Correction off by default on a fresh install so new users see no
		// behavior change; enabling it is a deliberate choice. Legacy configs
		// are migrated from corrections.enabled by Load.
		CorrectionMode: CorrectionModeOff,
		Corrections: CorrectionsConfig{
			// Enabled is retained only for migrating pre-mode configs; the live
			// switch is CorrectionMode. DictPath empty means the default
			// <AppDir>/corrections.txt (resolved by CorrectionsDictPath).
			Enabled:             false,
			Strategy:            CorrectionStrategyMDCSpell,
			ModelDir:            DefaultCorrectionsModelDir(),
			RuntimePath:         "",
			ConfidenceThreshold: 0.90,
			MinMargin:           0.20,
			MaxLength:           128,
			TimeoutMs:           1500,
			NumThreads:          4,
			DictPath:            "",
		},
		LLMCorrection: LLMCorrectionConfig{
			// Address/key/model empty so llm/combined modes fail the
			// completeness check until the user fills them in; the prompt and
			// timeout ship with working defaults.
			BaseURL:        "",
			APIKey:         "",
			Model:          "",
			PromptTemplate: defaultLLMPromptTemplate,
			TimeoutMs:      2000,
			ExtraParams:    nil,
		},
		Statistics: StatisticsConfig{
			Enabled: false,
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
		RestoreClipboard:      true,
		RemoveTrailingSymbols: true,
		Hotkey:                "alt+shift+g",
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

// StatsDir returns the root containing YYYY/MM/DD.json aggregate files.
func StatsDir() string {
	return filepath.Join(AppDir(), "stats")
}

// DefaultCorrectionsDictPath is the correction dictionary location used when
// Corrections.DictPath is left empty: corrections.txt next to the config file.
func DefaultCorrectionsDictPath() string {
	return filepath.Join(AppDir(), "corrections.txt")
}

// DefaultCorrectionsModelDir is the root holding the two optional correction
// model bundles.
func DefaultCorrectionsModelDir() string {
	return filepath.Join(DefaultModelDir(), "correction")
}

// CorrectionsDictPath returns the effective correction dictionary path,
// falling back to the default location when the config leaves DictPath empty.
func (c *Config) CorrectionsDictPath() string {
	if c.Corrections.DictPath != "" {
		return c.Corrections.DictPath
	}
	return DefaultCorrectionsDictPath()
}

// CorrectionsModelDir returns the effective correction-model root.
func (c *Config) CorrectionsModelDir() string {
	if c.Corrections.ModelDir != "" {
		return c.Corrections.ModelDir
	}
	return DefaultCorrectionsModelDir()
}

// NormalizeCorrectionMode maps empty or unknown values to a valid mode. An
// empty value is normally resolved by migration (see migrateCorrectionMode);
// any other unrecognized value degrades to off so a typo never silently calls
// out to the network.
func NormalizeCorrectionMode(mode string) string {
	switch mode {
	case CorrectionModeOff, CorrectionModeLocal, CorrectionModeLLM, CorrectionModeCombined:
		return mode
	default:
		return CorrectionModeOff
	}
}

// UseLocalCorrection reports whether the local BERT + dictionary pass runs
// under the current mode.
func (c *Config) UseLocalCorrection() bool {
	return c.CorrectionMode == CorrectionModeLocal || c.CorrectionMode == CorrectionModeCombined
}

// UseLLMCorrection reports whether the LLM pass runs under the current mode.
func (c *Config) UseLLMCorrection() bool {
	return c.CorrectionMode == CorrectionModeLLM || c.CorrectionMode == CorrectionModeCombined
}

// LLMCorrectionComplete reports whether every field required to call the LLM is
// present, returning the missing field labels so the tray can tell the user
// exactly what to fill in. The prompt template must also contain the
// placeholder, otherwise the recognized text has nowhere to be injected.
func (c *Config) LLMCorrectionComplete() (ok bool, missing []string) {
	l := c.LLMCorrection
	if strings.TrimSpace(l.BaseURL) == "" {
		missing = append(missing, "base_url")
	}
	if strings.TrimSpace(l.APIKey) == "" {
		missing = append(missing, "api_key")
	}
	if strings.TrimSpace(l.Model) == "" {
		missing = append(missing, "model")
	}
	if strings.TrimSpace(l.PromptTemplate) == "" {
		missing = append(missing, "prompt_template")
	} else if !strings.Contains(l.PromptTemplate, LLMCorrectionPlaceholder) {
		missing = append(missing, "prompt_template(缺少占位符 "+LLMCorrectionPlaceholder+")")
	}
	return len(missing) == 0, missing
}

// LLMCorrectionTimeout returns the configured hard timeout, defaulting to 2s
// when non-positive so a zeroed or legacy field still behaves sanely.
func (c *Config) LLMCorrectionTimeout() time.Duration {
	ms := c.LLMCorrection.TimeoutMs
	if ms <= 0 {
		ms = 2000
	}
	return time.Duration(ms) * time.Millisecond
}

// CorrectionModelPaths resolves a supported strategy to its ONNX and BERT
// vocabulary files.
func (c *Config) CorrectionModelPaths(strategy string) (CorrectionModelFiles, error) {
	set := models.ForCorrectionStrategy(strategy)
	if len(set) == 0 {
		return CorrectionModelFiles{}, fmt.Errorf("unknown correction strategy %q", strategy)
	}
	base := filepath.Join(c.CorrectionsModelDir(), set[0].Dir)
	return CorrectionModelFiles{
		Model: filepath.Join(base, "model.int8.onnx"),
		Vocab: filepath.Join(base, "vocab.txt"),
	}, nil
}

// CorrectionsRuntimePath returns the configured ONNX Runtime library or the
// platform-specific library in the packaged runtime directory.
func (c *Config) CorrectionsRuntimePath() string {
	if c.Corrections.RuntimePath != "" {
		return c.Corrections.RuntimePath
	}
	name := "libonnxruntime.so"
	switch runtime.GOOS {
	case "darwin":
		name = "libonnxruntime.dylib"
	case "windows":
		name = "onnxruntime.dll"
	}
	return filepath.Join(RuntimeLibDir(), name)
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

// migrateCorrectionMode fills correction_mode for configs written before the
// single-select mode existed. It probes the raw file (not cfg, whose field is
// already seeded to the default by DefaultConfig) to tell "key absent" from
// "key present". When the key is absent, the mode is derived from the legacy
// corrections.enabled flag (true -> local, otherwise -> off); when present it
// is normalized to a valid value. It returns true when it changed cfg so Load
// rewrites the file once to persist the resolved mode.
func migrateCorrectionMode(raw []byte, cfg *Config) bool {
	var probe struct {
		CorrectionMode *string `yaml:"correction_mode"`
	}
	if err := yaml.Unmarshal(raw, &probe); err == nil && probe.CorrectionMode != nil {
		normalized := NormalizeCorrectionMode(*probe.CorrectionMode)
		if normalized != cfg.CorrectionMode {
			cfg.CorrectionMode = normalized
			return true
		}
		return false
	}
	// No correction_mode key in the file: derive from the legacy flag and
	// persist it (the key was missing, so writing it is always a change).
	if cfg.Corrections.Enabled {
		cfg.CorrectionMode = CorrectionModeLocal
	} else {
		cfg.CorrectionMode = CorrectionModeOff
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
		migrated = migrateCorrectionMode(data, cfg) || migrated
		cfg.ResolveModelPaths()
		// Rewrite the file when we upgraded an old `api:` block or filled in a
		// missing correction_mode, so the new layout is persisted and the
		// migration runs only once.
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
			migrateCorrectionMode(data, cfg)
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
