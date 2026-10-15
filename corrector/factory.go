package corrector

import (
	"fmt"

	"linux-voice-input/config"
	"linux-voice-input/corrector/llm"
	"linux-voice-input/corrector/onnxbert"
)

// NewServiceFromConfig builds the ordered production pipeline: at most one
// neural BERT strategy followed by the user dictionary. Model files are loaded
// lazily by onnxbert.Strategy on the first enabled correction call.
func NewServiceFromConfig(cfg *config.Config) (*Service, error) {
	if cfg == nil {
		return nil, fmt.Errorf("correction config is nil")
	}

	strategies := make([]Strategy, 0, 2)
	if cfg.Corrections.Strategy != "" {
		paths, err := cfg.CorrectionModelPaths(cfg.Corrections.Strategy)
		if err != nil {
			return nil, err
		}
		model, err := onnxbert.New(onnxbert.Config{
			Name:                cfg.Corrections.Strategy,
			ModelPath:           paths.Model,
			VocabPath:           paths.Vocab,
			RuntimePath:         cfg.CorrectionsRuntimePath(),
			ConfidenceThreshold: cfg.Corrections.ConfidenceThreshold,
			MinMargin:           cfg.Corrections.MinMargin,
			MaxLength:           cfg.Corrections.MaxLength,
			NumThreads:          cfg.Corrections.NumThreads,
		})
		if err != nil {
			return nil, fmt.Errorf("configure correction model: %w", err)
		}
		strategies = append(strategies, model)
	}

	// Dictionary is deliberately last: explicit user terminology must win over
	// a general model prediction. A missing file is a safe no-op.
	strategies = append(strategies, New(cfg.CorrectionsDictPath()))
	// Enabled follows the single-select correction mode: the local pass runs in
	// local and combined modes.
	return NewService(cfg.UseLocalCorrection(), strategies...), nil
}

// NewLLMServiceFromConfig builds a correction service containing only the LLM
// strategy. When the LLM config is incomplete it returns a disabled, empty
// service, so the caller can always hold a non-nil service that simply passes
// text through until the user fills in the configuration. When complete, the
// service's enabled state follows the correction mode (llm or combined).
func NewLLMServiceFromConfig(cfg *config.Config) *Service {
	if cfg == nil {
		return NewService(false)
	}
	if ok, _ := cfg.LLMCorrectionComplete(); !ok {
		return NewService(false)
	}
	client := llm.NewClient(
		cfg.LLMCorrection.BaseURL,
		cfg.LLMCorrection.APIKey,
		cfg.LLMCorrection.Model,
		cfg.LLMCorrection.ExtraParams,
	)
	strategy := llm.NewStrategy(client, cfg.LLMCorrection.PromptTemplate, config.LLMCorrectionPlaceholder)
	return NewService(cfg.UseLLMCorrection(), strategy)
}
