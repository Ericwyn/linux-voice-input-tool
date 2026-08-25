package corrector

import (
	"fmt"

	"linux-voice-input/config"
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
	return NewService(cfg.Corrections.Enabled, strategies...), nil
}
