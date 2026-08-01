package api

import (
	"fmt"
	"log"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"linux-voice-input/config"
)

type SherpaStreamRecognizer struct {
	recognizer *sherpa.OnlineRecognizer
}

type SherpaStream struct {
	recognizer *sherpa.OnlineRecognizer
	stream     *sherpa.OnlineStream
	mu         sync.Mutex
	segments   []string
}

func NewSherpaStreamRecognizer(cfg config.SherpaConfig) (*SherpaStreamRecognizer, error) {
	tokensTxtPath, err := EnsureTokensTxt(cfg.Tokens)
	if err != nil {
		return nil, fmt.Errorf("prepare tokens: %w", err)
	}
	log.Printf("Using tokens file: %s", tokensTxtPath)

	numThreads := cfg.NumThreads
	if numThreads <= 0 {
		numThreads = 4
	}

	recognizerConfig := &sherpa.OnlineRecognizerConfig{}
	recognizerConfig.FeatConfig = sherpa.FeatureConfig{
		SampleRate: 16000,
		FeatureDim: 80,
	}
	recognizerConfig.ModelConfig.Paraformer.Encoder = cfg.Encoder
	recognizerConfig.ModelConfig.Paraformer.Decoder = cfg.Decoder
	recognizerConfig.ModelConfig.Tokens = tokensTxtPath
	recognizerConfig.ModelConfig.NumThreads = numThreads
	recognizerConfig.ModelConfig.Provider = "cpu"
	recognizerConfig.ModelConfig.ModelType = "paraformer"
	recognizerConfig.DecodingMethod = "greedy_search"
	recognizerConfig.EnableEndpoint = 1
	recognizerConfig.Rule1MinTrailingSilence = cfg.Rule1MinTrailingSilence
	recognizerConfig.Rule2MinTrailingSilence = cfg.Rule2MinTrailingSilence
	recognizerConfig.Rule3MinUtteranceLength = 20

	recognizer := sherpa.NewOnlineRecognizer(recognizerConfig)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create sherpa online recognizer")
	}

	return &SherpaStreamRecognizer{recognizer: recognizer}, nil
}

func (r *SherpaStreamRecognizer) CreateStream() *SherpaStream {
	stream := sherpa.NewOnlineStream(r.recognizer)
	return &SherpaStream{
		recognizer: r.recognizer,
		stream:     stream,
		segments:   make([]string, 0),
	}
}

func (r *SherpaStreamRecognizer) Close() {
	if r.recognizer != nil {
		sherpa.DeleteOnlineRecognizer(r.recognizer)
		r.recognizer = nil
	}
}

// FeedAudio accepts a chunk of audio samples (float32, range [-1, 1], 16kHz)
// and performs decoding. It also handles endpoint detection and collects completed segments.
func (s *SherpaStream) FeedAudio(samples []float32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stream.AcceptWaveform(16000, samples)

	for s.recognizer.IsReady(s.stream) {
		s.recognizer.Decode(s.stream)
	}

	if s.recognizer.IsEndpoint(s.stream) {
		result := s.recognizer.GetResult(s.stream)
		segmentText := strings.TrimSpace(result.Text)
		if segmentText != "" {
			s.segments = append(s.segments, segmentText)
		}
		s.recognizer.Reset(s.stream)
	}
}

// GetPartialResult returns the current partial recognition text including
// all completed segments plus the current in-progress segment.
func (s *SherpaStream) GetPartialResult() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := s.recognizer.GetResult(s.stream)
	currentText := strings.TrimSpace(result.Text)

	allParts := make([]string, 0, len(s.segments)+1)
	allParts = append(allParts, s.segments...)
	if currentText != "" {
		allParts = append(allParts, currentText)
	}

	return strings.Join(allParts, "")
}

// Finish signals that audio input is complete, performs final decoding,
// and returns the full transcription text.
func (s *SherpaStream) Finish() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stream.InputFinished()

	for s.recognizer.IsReady(s.stream) {
		s.recognizer.Decode(s.stream)
	}

	result := s.recognizer.GetResult(s.stream)
	finalText := strings.TrimSpace(result.Text)
	if finalText != "" {
		s.segments = append(s.segments, finalText)
	}

	return strings.Join(s.segments, "")
}

func (s *SherpaStream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stream != nil {
		sherpa.DeleteOnlineStream(s.stream)
		s.stream = nil
	}
}

// SherpaOfflineRecognizer wraps the sherpa-onnx OfflineRecognizer for
// high-accuracy final transcription after recording completes.
type SherpaOfflineRecognizer struct {
	recognizer *sherpa.OfflineRecognizer
}

func NewSherpaOfflineRecognizer(cfg config.SherpaConfig) (*SherpaOfflineRecognizer, error) {
	if cfg.OfflineModel == "" {
		return nil, fmt.Errorf("offline_model path is not configured")
	}

	tokensTxtPath, err := EnsureTokensTxt(cfg.OfflineTokens)
	if err != nil {
		return nil, fmt.Errorf("prepare offline tokens: %w", err)
	}
	log.Printf("Using offline tokens file: %s", tokensTxtPath)

	numThreads := cfg.NumThreads
	if numThreads <= 0 {
		numThreads = 4
	}

	recognizerConfig := &sherpa.OfflineRecognizerConfig{}
	recognizerConfig.FeatConfig = sherpa.FeatureConfig{
		SampleRate: 16000,
		FeatureDim: 80,
	}
	recognizerConfig.ModelConfig.Paraformer.Model = cfg.OfflineModel
	recognizerConfig.ModelConfig.Tokens = tokensTxtPath
	recognizerConfig.ModelConfig.NumThreads = numThreads
	recognizerConfig.ModelConfig.Provider = "cpu"
	recognizerConfig.ModelConfig.ModelType = "paraformer"
	recognizerConfig.DecodingMethod = "greedy_search"

	recognizer := sherpa.NewOfflineRecognizer(recognizerConfig)
	if recognizer == nil {
		return nil, fmt.Errorf("failed to create sherpa offline recognizer")
	}

	return &SherpaOfflineRecognizer{recognizer: recognizer}, nil
}

// TranscribeAudioSamples performs offline recognition on the given audio samples.
func (r *SherpaOfflineRecognizer) TranscribeAudioSamples(samples []float32) (string, error) {
	stream := sherpa.NewOfflineStream(r.recognizer)
	defer sherpa.DeleteOfflineStream(stream)

	stream.AcceptWaveform(16000, samples)
	r.recognizer.Decode(stream)

	result := stream.GetResult()
	return strings.TrimSpace(result.Text), nil
}

func (r *SherpaOfflineRecognizer) Close() {
	if r.recognizer != nil {
		sherpa.DeleteOfflineRecognizer(r.recognizer)
		r.recognizer = nil
	}
}
