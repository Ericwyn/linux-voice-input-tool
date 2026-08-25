package onnxbert

import (
	"context"
	"fmt"
	"math"
	"sync"
)

const (
	defaultConfidenceThreshold = 0.90
	defaultMinMargin           = 0.20
)

// Config describes one BERT masked-language-model checkpoint. MDCSpell-v2 and
// MacBERT4CSC differ only in these paths/name and can share this implementation.
type Config struct {
	Name                string
	ModelPath           string
	VocabPath           string
	RuntimePath         string
	ConfidenceThreshold float64
	MinMargin           float64
	MaxLength           int
	NumThreads          int
}

// Inputs are the three standard BERT tensors, flattened from shape [1, L].
type Inputs struct {
	InputIDs      []int64
	AttentionMask []int64
	TokenTypeIDs  []int64
}

// Logits is a flattened [1, sequence_length, vocabulary_size] output.
type Logits struct {
	Data           []float32
	SequenceLength int
	VocabSize      int
}

// Runner isolates the ONNX dependency from tokenization and correction policy.
type Runner interface {
	Run(ctx context.Context, inputs Inputs) (Logits, error)
	Close() error
}

type runnerFactory func(Config) (Runner, error)

// Strategy applies conservative single-Chinese-character replacements from a
// BERT MLM. The ONNX runner is initialized lazily on first use.
type Strategy struct {
	config    Config
	tokenizer *Tokenizer
	factory   runnerFactory

	initMu sync.Mutex
	runMu  sync.Mutex
	runner Runner
	closed bool
}

// New creates a production ONNX-backed strategy. Both vocabulary and model
// stay lazy so an opt-in model can be downloaded after daemon startup and then
// activated by reloading configuration.
func New(config Config) (*Strategy, error) {
	config = withDefaults(config)
	if config.Name == "" {
		return nil, fmt.Errorf("correction model name is empty")
	}
	if config.ModelPath == "" {
		return nil, fmt.Errorf("correction model path is empty")
	}
	if config.VocabPath == "" {
		return nil, fmt.Errorf("correction vocabulary path is empty")
	}
	return &Strategy{config: config, factory: newONNXRunner}, nil
}

// NewWithRunner builds a strategy around a custom runner. It is useful for
// deterministic tests and alternative local runtimes.
func NewWithRunner(config Config, tokenizer *Tokenizer, runner Runner) *Strategy {
	config = withDefaults(config)
	return &Strategy{config: config, tokenizer: tokenizer, runner: runner}
}

func withDefaults(config Config) Config {
	if config.ConfidenceThreshold <= 0 || config.ConfidenceThreshold > 1 {
		config.ConfidenceThreshold = defaultConfidenceThreshold
	}
	if config.MinMargin < 0 || config.MinMargin > 1 {
		config.MinMargin = defaultMinMargin
	}
	if config.MinMargin == 0 {
		config.MinMargin = defaultMinMargin
	}
	if config.MaxLength <= 0 {
		config.MaxLength = defaultMaxLength
	}
	if config.NumThreads <= 0 {
		config.NumThreads = 4
	}
	return config
}

// Name implements corrector.Strategy.
func (s *Strategy) Name() string { return s.config.Name }

func (s *Strategy) getReady() (*Tokenizer, Runner, error) {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	if s.closed {
		return nil, nil, fmt.Errorf("correction strategy %s is closed", s.Name())
	}
	if s.tokenizer == nil {
		tokenizer, err := LoadTokenizer(s.config.VocabPath, s.config.MaxLength)
		if err != nil {
			return nil, nil, err
		}
		s.tokenizer = tokenizer
	}
	if s.runner != nil {
		return s.tokenizer, s.runner, nil
	}
	if s.factory == nil {
		return nil, nil, fmt.Errorf("correction strategy %s has no runner", s.Name())
	}
	runner, err := s.factory(s.config)
	if err != nil {
		return nil, nil, err
	}
	s.runner = runner
	return s.tokenizer, runner, nil
}

// Apply runs one model pass and accepts only high-confidence single-Han
// predictions. On every error it returns the original input with the error.
func (s *Strategy) Apply(ctx context.Context, text string) (string, error) {
	if text == "" {
		return text, nil
	}
	if err := ctx.Err(); err != nil {
		return text, err
	}
	// Serialize the whole lazy-init + Run lifecycle with Close. Without this,
	// Close could destroy a session after getReady returned it but before Run
	// entered the native runtime.
	s.runMu.Lock()
	defer s.runMu.Unlock()
	tokenizer, runner, err := s.getReady()
	if err != nil {
		return text, err
	}
	encoding := tokenizer.Encode(text)

	logits, err := runner.Run(ctx, Inputs{
		InputIDs:      encoding.InputIDs,
		AttentionMask: encoding.AttentionMask,
		TokenTypeIDs:  encoding.TokenTypeIDs,
	})
	if err != nil {
		return text, err
	}
	if err := validateLogits(logits, len(encoding.InputIDs), tokenizer.VocabularySize()); err != nil {
		return text, err
	}

	correctedRunes := []rune(text)
	for position, originalTokenID64 := range encoding.InputIDs {
		runePosition := encoding.RunePositions[position]
		if runePosition < 0 || runePosition >= len(correctedRunes) || !isSingleHanToken(encoding.Tokens[position]) {
			continue
		}

		originalTokenID := int(originalTokenID64)
		predictionID, predictionProbability, originalProbability := probabilitiesAt(logits, position, originalTokenID)
		if predictionID == originalTokenID || predictionProbability < s.config.ConfidenceThreshold ||
			predictionProbability-originalProbability < s.config.MinMargin {
			continue
		}
		prediction := tokenizer.Token(predictionID)
		if !isSingleHanToken(prediction) {
			continue
		}
		correctedRunes[runePosition] = []rune(prediction)[0]
	}

	return string(correctedRunes), nil
}

func validateLogits(logits Logits, sequenceLength, vocabularySize int) error {
	if logits.SequenceLength != sequenceLength {
		return fmt.Errorf("model sequence length %d does not match input %d", logits.SequenceLength, sequenceLength)
	}
	if logits.VocabSize != vocabularySize {
		return fmt.Errorf("model vocabulary size %d does not match tokenizer %d", logits.VocabSize, vocabularySize)
	}
	want := sequenceLength * vocabularySize
	if len(logits.Data) != want {
		return fmt.Errorf("model returned %d logits, want %d", len(logits.Data), want)
	}
	return nil
}

func probabilitiesAt(logits Logits, position, originalTokenID int) (predictionID int, predictionProbability, originalProbability float64) {
	start := position * logits.VocabSize
	row := logits.Data[start : start+logits.VocabSize]
	predictionID = 0
	maxLogit := row[0]
	for tokenID := 1; tokenID < len(row); tokenID++ {
		if row[tokenID] > maxLogit {
			maxLogit = row[tokenID]
			predictionID = tokenID
		}
	}

	denominator := 0.0
	for _, logit := range row {
		denominator += math.Exp(float64(logit - maxLogit))
	}
	if denominator == 0 || math.IsInf(denominator, 0) || math.IsNaN(denominator) {
		return predictionID, 0, 0
	}
	predictionProbability = 1 / denominator
	if originalTokenID >= 0 && originalTokenID < len(row) {
		originalProbability = math.Exp(float64(row[originalTokenID]-maxLogit)) / denominator
	}
	return predictionID, predictionProbability, originalProbability
}

// Close releases the lazy runner if it was initialized.
func (s *Strategy) Close() error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.initMu.Lock()
	if s.closed {
		s.initMu.Unlock()
		return nil
	}
	s.closed = true
	runner := s.runner
	s.runner = nil
	s.initMu.Unlock()
	if runner != nil {
		return runner.Close()
	}
	return nil
}
