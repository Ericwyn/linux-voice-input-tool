package corrector

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type recordingStrategy struct {
	name   string
	apply  func(string) (string, error)
	calls  *[]string
	closed bool
}

func (s *recordingStrategy) Name() string { return s.name }

func (s *recordingStrategy) Apply(_ context.Context, text string) (string, error) {
	if s.calls != nil {
		*s.calls = append(*s.calls, s.name+":"+text)
	}
	return s.apply(text)
}

func (s *recordingStrategy) Close() error {
	s.closed = true
	return nil
}

func TestServiceDisabledDoesNotInvokeStrategies(t *testing.T) {
	var calls []string
	strategy := &recordingStrategy{
		name:  "model",
		calls: &calls,
		apply: func(text string) (string, error) { return "changed", nil },
	}
	service := NewService(false, strategy)

	result := service.Correct(context.Background(), "original")

	if result.Text != "original" {
		t.Fatalf("Correct() text = %q, want original", result.Text)
	}
	if len(calls) != 0 {
		t.Fatalf("disabled service invoked strategies: %v", calls)
	}
}

func TestServiceAppliesStrategiesInOrder(t *testing.T) {
	var calls []string
	model := &recordingStrategy{
		name:  "model",
		calls: &calls,
		apply: func(text string) (string, error) {
			return text + "-model", nil
		},
	}
	dictionary := &recordingStrategy{
		name:  "dictionary",
		calls: &calls,
		apply: func(text string) (string, error) {
			return text + "-dictionary", nil
		},
	}
	service := NewService(true, model, dictionary)

	result := service.Correct(context.Background(), "input")

	if result.Text != "input-model-dictionary" {
		t.Fatalf("Correct() text = %q", result.Text)
	}
	wantCalls := []string{"model:input", "dictionary:input-model"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("strategy calls = %v, want %v", calls, wantCalls)
	}
	if !reflect.DeepEqual(result.Applied, []string{"model", "dictionary"}) {
		t.Fatalf("applied = %v", result.Applied)
	}
}

func TestServiceFailsOpenAndContinues(t *testing.T) {
	modelErr := errors.New("model unavailable")
	model := &recordingStrategy{
		name: "model",
		apply: func(string) (string, error) {
			return "must-not-leak", modelErr
		},
	}
	dictionary := &recordingStrategy{
		name: "dictionary",
		apply: func(text string) (string, error) {
			return text + "-dictionary", nil
		},
	}
	service := NewService(true, model, dictionary)

	result := service.Correct(context.Background(), "input")

	if result.Text != "input-dictionary" {
		t.Fatalf("Correct() text = %q, want fail-open dictionary result", result.Text)
	}
	if len(result.Failures) != 1 || result.Failures[0].Strategy != "model" || !errors.Is(result.Failures[0].Err, modelErr) {
		t.Fatalf("failures = %#v", result.Failures)
	}
}

func TestServiceCanBeToggledAtRuntime(t *testing.T) {
	strategy := &recordingStrategy{
		name: "model",
		apply: func(text string) (string, error) {
			return text + "-corrected", nil
		},
	}
	service := NewService(false, strategy)

	service.SetEnabled(true)
	if !service.Enabled() {
		t.Fatal("service should be enabled")
	}
	if got := service.Correct(context.Background(), "input").Text; got != "input-corrected" {
		t.Fatalf("enabled result = %q", got)
	}

	service.SetEnabled(false)
	if service.Enabled() {
		t.Fatal("service should be disabled")
	}
	if got := service.Correct(context.Background(), "input").Text; got != "input" {
		t.Fatalf("disabled result = %q", got)
	}
}

func TestServiceCloseClosesStrategies(t *testing.T) {
	first := &recordingStrategy{name: "first", apply: func(text string) (string, error) { return text, nil }}
	second := &recordingStrategy{name: "second", apply: func(text string) (string, error) { return text, nil }}
	service := NewService(true, first, second)

	if err := service.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if !first.closed || !second.closed {
		t.Fatalf("strategies were not closed: first=%v second=%v", first.closed, second.closed)
	}
}

func TestServiceReportsDuration(t *testing.T) {
	strategy := &recordingStrategy{
		name: "model",
		apply: func(text string) (string, error) {
			time.Sleep(time.Millisecond)
			return text, nil
		},
	}
	result := NewService(true, strategy).Correct(context.Background(), "input")
	if result.Duration < time.Millisecond {
		t.Fatalf("Duration = %v, want at least 1ms", result.Duration)
	}
}

func TestServiceCanceledModelStillRunsDictionary(t *testing.T) {
	model := &recordingStrategy{
		name:  "model",
		apply: func(string) (string, error) { return "partial", context.Canceled },
	}
	dictionary := &recordingStrategy{
		name:  "dictionary",
		apply: func(text string) (string, error) { return text + "-dictionary", nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewService(true, model, dictionary).Correct(ctx, "input")
	if result.Text != "input-dictionary" {
		t.Fatalf("canceled result = %q", result.Text)
	}
	if len(result.Failures) != 1 || !errors.Is(result.Failures[0].Err, context.Canceled) {
		t.Fatalf("failures = %#v", result.Failures)
	}
}
