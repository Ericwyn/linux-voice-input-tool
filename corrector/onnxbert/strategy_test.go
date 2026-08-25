package onnxbert

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

type fakeRunner struct {
	output Logits
	err    error
	closed bool
}

func (r *fakeRunner) Run(_ context.Context, _ Inputs) (Logits, error) {
	return r.output, r.err
}

func (r *fakeRunner) Close() error {
	r.closed = true
	return nil
}

func logitsFor(encoding Encoding, vocabSize int, predictions map[int]int, confidence float64) Logits {
	data := make([]float32, len(encoding.InputIDs)*vocabSize)
	low := float32(-12)
	for index := range data {
		data[index] = low
	}
	for position, originalID := range encoding.InputIDs {
		data[position*vocabSize+int(originalID)] = 0
	}
	// With every other logit near -12, log(p/(1-p)) is a close enough target
	// for deterministic threshold tests.
	high := float32(math.Log(confidence / (1 - confidence)))
	for position, tokenID := range predictions {
		data[position*vocabSize+tokenID] = high
	}
	return Logits{Data: data, SequenceLength: len(encoding.InputIDs), VocabSize: vocabSize}
}

func TestStrategyCorrectsHighConfidenceSingleHanCharacter(t *testing.T) {
	tokenizer, err := NewTokenizer(testVocabulary(), 32)
	if err != nil {
		t.Fatal(err)
	}
	encoding := tokenizer.Encode("因该让坐")
	runner := &fakeRunner{output: logitsFor(encoding, len(testVocabulary()), map[int]int{
		1: tokenizer.TokenID("应"),
		4: tokenizer.TokenID("座"),
	}, 0.99)}
	strategy := NewWithRunner(Config{Name: "test", ConfidenceThreshold: 0.9, MinMargin: 0.2}, tokenizer, runner)

	got, err := strategy.Apply(context.Background(), "因该让坐")
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if got != "应该让座" {
		t.Fatalf("Apply() = %q, want %q", got, "应该让座")
	}
}

func TestStrategyKeepsLowConfidencePrediction(t *testing.T) {
	tokenizer, _ := NewTokenizer(testVocabulary(), 32)
	encoding := tokenizer.Encode("因该")
	runner := &fakeRunner{output: logitsFor(encoding, len(testVocabulary()), map[int]int{1: tokenizer.TokenID("应")}, 0.60)}
	strategy := NewWithRunner(Config{Name: "test", ConfidenceThreshold: 0.9, MinMargin: 0.2}, tokenizer, runner)

	got, err := strategy.Apply(context.Background(), "因该")
	if err != nil {
		t.Fatal(err)
	}
	if got != "因该" {
		t.Fatalf("Apply() = %q, want unchanged", got)
	}
}

func TestStrategyDoesNotReplaceChineseWithNonHanToken(t *testing.T) {
	tokenizer, _ := NewTokenizer(testVocabulary(), 32)
	encoding := tokenizer.Encode("因该")
	runner := &fakeRunner{output: logitsFor(encoding, len(testVocabulary()), map[int]int{1: tokenizer.TokenID("hello")}, 0.99)}
	strategy := NewWithRunner(Config{Name: "test", ConfidenceThreshold: 0.9, MinMargin: 0.2}, tokenizer, runner)

	got, err := strategy.Apply(context.Background(), "因该")
	if err != nil {
		t.Fatal(err)
	}
	if got != "因该" {
		t.Fatalf("Apply() = %q, want unchanged", got)
	}
}

func TestStrategyReturnsRunnerErrorWithoutPartialText(t *testing.T) {
	tokenizer, _ := NewTokenizer(testVocabulary(), 32)
	runErr := errors.New("onnx failed")
	runner := &fakeRunner{err: runErr}
	strategy := NewWithRunner(Config{Name: "test"}, tokenizer, runner)

	got, err := strategy.Apply(context.Background(), "因该")
	if got != "因该" {
		t.Fatalf("Apply() text = %q, want original", got)
	}
	if !errors.Is(err, runErr) {
		t.Fatalf("Apply() error = %v, want %v", err, runErr)
	}
}

func TestStrategyCloseClosesRunner(t *testing.T) {
	tokenizer, _ := NewTokenizer(testVocabulary(), 32)
	runner := &fakeRunner{}
	strategy := NewWithRunner(Config{Name: "test"}, tokenizer, runner)

	if err := strategy.Close(); err != nil {
		t.Fatal(err)
	}
	if !runner.closed {
		t.Fatal("runner was not closed")
	}
}

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingRunner) Run(_ context.Context, inputs Inputs) (Logits, error) {
	close(r.started)
	<-r.release
	return Logits{}, errors.New("stopped")
}

func (r *blockingRunner) Close() error { return nil }

func TestStrategyCloseWaitsForInFlightRun(t *testing.T) {
	tokenizer, _ := NewTokenizer(testVocabulary(), 32)
	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	strategy := NewWithRunner(Config{Name: "test"}, tokenizer, runner)
	applyDone := make(chan struct{})
	go func() {
		_, _ = strategy.Apply(context.Background(), "因该")
		close(applyDone)
	}()
	<-runner.started

	closeDone := make(chan struct{})
	go func() {
		_ = strategy.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("Close returned while Run was still in flight")
	case <-time.After(10 * time.Millisecond):
	}

	close(runner.release)
	select {
	case <-applyDone:
	case <-time.After(time.Second):
		t.Fatal("Apply did not return")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after Run completed")
	}
}
