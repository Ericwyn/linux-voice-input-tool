package api

import (
	"fmt"
	"log"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"linux-voice-input/config"
)

// SenseVoiceRecognizer wraps a sherpa-onnx OfflineRecognizer configured for the
// SenseVoice model. One recognizer is reused across many transcriptions (the
// live-preview re-runs during recording plus the final pass). Its Decode is
// serialized by mu so those concurrent callers never touch the underlying C
// recognizer at the same time.
type SenseVoiceRecognizer struct {
	recognizer *sherpa.OfflineRecognizer
	mu         sync.Mutex
}

// NewSenseVoiceRecognizer builds a SenseVoice offline recognizer from config.
// Language defaults to "auto" and threads to 4 when unset. UseITN turns on
// inverse text normalization, which also produces punctuation.
func NewSenseVoiceRecognizer(cfg config.SenseVoiceConfig) (*SenseVoiceRecognizer, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("sensevoice model path is not configured")
	}

	tokensTxtPath, err := EnsureTokensTxt(cfg.Tokens)
	if err != nil {
		return nil, fmt.Errorf("prepare sensevoice tokens: %w", err)
	}
	log.Printf("Using SenseVoice tokens file: %s", tokensTxtPath)

	numThreads := cfg.NumThreads
	if numThreads <= 0 {
		numThreads = 4
	}
	language := cfg.Language
	if language == "" {
		language = "auto"
	}
	useITN := 0
	if cfg.UseITN {
		useITN = 1
	}

	recognizerConfig := &sherpa.OfflineRecognizerConfig{}
	recognizerConfig.FeatConfig = sherpa.FeatureConfig{
		SampleRate: 16000,
		FeatureDim: 80,
	}
	recognizerConfig.ModelConfig.SenseVoice.Model = cfg.Model
	recognizerConfig.ModelConfig.SenseVoice.Language = language
	recognizerConfig.ModelConfig.SenseVoice.UseInverseTextNormalization = useITN
	recognizerConfig.ModelConfig.Tokens = tokensTxtPath
	recognizerConfig.ModelConfig.NumThreads = numThreads
	recognizerConfig.ModelConfig.Provider = "cpu"
	recognizerConfig.ModelConfig.ModelType = "sense_voice"
	recognizerConfig.DecodingMethod = "greedy_search"

	recognizer := sherpa.NewOfflineRecognizer(recognizerConfig)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create sensevoice recognizer")
	}

	return &SenseVoiceRecognizer{recognizer: recognizer}, nil
}

// Transcribe runs one offline recognition over the given samples (float32,
// 16kHz, range [-1, 1]) and returns the clean transcription text. The model's
// language/emotion/event tags are logged for debugging but deliberately left
// out of the returned text. Safe for concurrent callers: Decode is serialized
// internally.
func (r *SenseVoiceRecognizer) Transcribe(samples []float32) (string, error) {
	if len(samples) == 0 {
		return "", nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.recognizer == nil {
		return "", fmt.Errorf("sensevoice recognizer is closed")
	}

	stream := sherpa.NewOfflineStream(r.recognizer)
	defer sherpa.DeleteOfflineStream(stream)

	stream.AcceptWaveform(16000, samples)
	r.recognizer.Decode(stream)

	result := stream.GetResult()
	if result.Lang != "" || result.Emotion != "" || result.Event != "" {
		log.Printf("SenseVoice tags: lang=%q emotion=%q event=%q",
			result.Lang, result.Emotion, result.Event)
	}
	return strings.TrimSpace(result.Text), nil
}

// Close releases the underlying recognizer. Safe to call multiple times.
func (r *SenseVoiceRecognizer) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recognizer != nil {
		sherpa.DeleteOfflineRecognizer(r.recognizer)
		r.recognizer = nil
	}
}
