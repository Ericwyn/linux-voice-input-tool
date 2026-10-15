package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStrategyName(t *testing.T) {
	s := NewStrategy(nil, "x", "{{text}}")
	if s.Name() != "llm" {
		t.Fatalf("Name() = %q, want llm", s.Name())
	}
}

func TestStrategyApplyRendersPlaceholder(t *testing.T) {
	var gotPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The rendered prompt appears verbatim in the request body.
		gotPrompt = string(body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"corrected"}}]}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "k", "m", nil)
	s := NewStrategy(client, "请纠正：{{text}}（结尾再给一次 {{text}}）", "{{text}}")

	got, err := s.Apply(context.Background(), "语音文本")
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if got != "corrected" {
		t.Fatalf("Apply() = %q, want corrected", got)
	}
	// Both placeholders substituted.
	if strings.Count(gotPrompt, "语音文本") != 2 {
		t.Fatalf("expected placeholder replaced twice, body=%s", gotPrompt)
	}
	if strings.Contains(gotPrompt, "{{text}}") {
		t.Fatalf("placeholder left unrendered, body=%s", gotPrompt)
	}
}

func TestStrategyApplyPropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewClient(server.URL, "k", "m", nil)
	s := NewStrategy(client, "{{text}}", "{{text}}")
	if _, err := s.Apply(context.Background(), "x"); err == nil {
		t.Fatal("Apply() should propagate client error for the service to fail open")
	}
}

func TestStrategyCloseIsNoOp(t *testing.T) {
	if err := NewStrategy(nil, "x", "{{text}}").Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}
