package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompleteSuccess(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("auth = %q", auth)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"  纠正后文本  "}}]}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "sk-test", "gpt-4o-mini", nil)
	got, err := client.Complete(context.Background(), "原始文本")
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if got != "纠正后文本" {
		t.Fatalf("Complete() = %q, want trimmed 纠正后文本", got)
	}
	// Base fields present.
	if gotBody["model"] != "gpt-4o-mini" {
		t.Fatalf("request model = %v", gotBody["model"])
	}
	if gotBody["stream"] != false {
		t.Fatalf("request stream = %v, want false", gotBody["stream"])
	}
	// messages carries the prompt.
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v", gotBody["messages"])
	}
	first := msgs[0].(map[string]any)
	if first["content"] != "原始文本" {
		t.Fatalf("message content = %v", first["content"])
	}
}

func TestCompleteMergesExtraParams(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	extra := map[string]any{
		"max_tokens":  float64(500),
		"temperature": float64(0.7), // override the default 0
		// Attempt to override messages must be ignored.
		"messages": "hijacked",
	}
	client := NewClient(server.URL, "k", "m", extra)
	if _, err := client.Complete(context.Background(), "the prompt"); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if gotBody["max_tokens"] != float64(500) {
		t.Fatalf("max_tokens = %v, want 500", gotBody["max_tokens"])
	}
	if gotBody["temperature"] != float64(0.7) {
		t.Fatalf("temperature = %v, want overridden 0.7", gotBody["temperature"])
	}
	// messages must remain the authoritative prompt array, not the hijack string.
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages overridden by extra_params: %v", gotBody["messages"])
	}
	if msgs[0].(map[string]any)["content"] != "the prompt" {
		t.Fatalf("prompt lost: %v", msgs[0])
	}
}

func TestCompleteNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer server.Close()

	client := NewClient(server.URL, "k", "m", nil)
	if _, err := client.Complete(context.Background(), "x"); err == nil {
		t.Fatal("expected error on non-200")
	}
}

func TestCompleteTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"late"}}]}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "k", "m", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := client.Complete(ctx, "x")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("Complete did not honor ctx timeout, took %v", elapsed)
	}
}

func TestCompleteEmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "k", "m", nil)
	if _, err := client.Complete(context.Background(), "x"); err == nil {
		t.Fatal("expected error on empty choices")
	}
}

func TestCompleteEmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"   "}}]}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "k", "m", nil)
	if _, err := client.Complete(context.Background(), "x"); err == nil {
		t.Fatal("expected error on empty content")
	}
}

func TestClientTrimsTrailingSlash(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	client := NewClient(server.URL+"/", "k", "m", nil)
	if _, err := client.Complete(context.Background(), "x"); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/chat/completions") || strings.Contains(gotPath, "//") {
		t.Fatalf("path = %q, want single-slash /chat/completions", gotPath)
	}
}
