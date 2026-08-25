package corrector

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Strategy is one text-correction implementation managed by Service. A
// strategy must either return a complete corrected string or an error. Service
// ignores the returned string on error, which gives every implementation the
// same fail-open contract.
type Strategy interface {
	Name() string
	Apply(ctx context.Context, text string) (string, error)
	Close() error
}

// Failure records one strategy that could not process the text. Failures are
// returned for logging and evaluation; they never make the corrected text
// unavailable to the caller.
type Failure struct {
	Strategy string
	Err      error
}

// Result is the observable outcome of one service call.
type Result struct {
	Text     string
	Applied  []string
	Failures []Failure
	Duration time.Duration
}

// Service applies a configured list of correction strategies in order. It is
// safe for concurrent use, including toggling Enabled while a correction is in
// flight. Strategies are expected to provide their own concurrency safety.
type Service struct {
	mu         sync.RWMutex
	enabled    bool
	strategies []Strategy
	closed     bool
}

// NewService creates a correction service. Nil strategies are ignored so a
// factory can conditionally add an optional model or dictionary without
// special-casing the final slice.
func NewService(enabled bool, strategies ...Strategy) *Service {
	filtered := make([]Strategy, 0, len(strategies))
	for _, strategy := range strategies {
		if strategy != nil {
			filtered = append(filtered, strategy)
		}
	}
	return &Service{enabled: enabled, strategies: filtered}
}

// Enabled reports the current runtime state.
func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled && !s.closed
}

// SetEnabled changes the runtime state without rebuilding any strategies.
// Model strategies remain lazy, so enabling the service does not load a model
// until the next non-empty Correct call.
func (s *Service) SetEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.enabled = enabled
	}
	s.mu.Unlock()
}

// Correct applies each strategy to the previous strategy's output. A failed
// strategy is skipped and the last valid text continues to later strategies.
func (s *Service) Correct(ctx context.Context, text string) (result Result) {
	start := time.Now()
	result = Result{Text: text}
	defer func() { result.Duration = time.Since(start) }()

	if s == nil || text == "" {
		return result
	}

	s.mu.RLock()
	enabled := s.enabled && !s.closed
	strategies := append([]Strategy(nil), s.strategies...)
	s.mu.RUnlock()
	if !enabled {
		return result
	}

	for _, strategy := range strategies {
		corrected, err := strategy.Apply(ctx, result.Text)
		if err != nil {
			result.Failures = append(result.Failures, Failure{Strategy: strategy.Name(), Err: err})
			continue
		}
		if corrected != result.Text {
			result.Applied = append(result.Applied, strategy.Name())
		}
		result.Text = corrected
	}

	return result
}

// Close releases every strategy once. It is safe to call repeatedly.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.enabled = false
	strategies := append([]Strategy(nil), s.strategies...)
	s.mu.Unlock()

	var closeErrors []error
	for _, strategy := range strategies {
		if err := strategy.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close correction strategy %s: %w", strategy.Name(), err))
		}
	}
	return errors.Join(closeErrors...)
}
