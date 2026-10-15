package llm

import (
	"context"
	"strings"
)

// Strategy adapts the LLM client to the corrector.Strategy contract (satisfied
// structurally; this package deliberately does not import corrector to avoid an
// import cycle). It renders the prompt template by substituting the placeholder
// with the recognized text, calls the endpoint, and returns the corrected text.
// Any error is returned as-is so the enclosing corrector.Service applies the
// fail-open policy and keeps the previous text.
type Strategy struct {
	client      *Client
	promptTmpl  string
	placeholder string
}

// NewStrategy builds a strategy from a client, a prompt template, and the
// placeholder token to substitute within it.
func NewStrategy(client *Client, promptTemplate, placeholder string) *Strategy {
	return &Strategy{client: client, promptTmpl: promptTemplate, placeholder: placeholder}
}

// Name identifies this strategy in Service results and logs.
func (s *Strategy) Name() string { return "llm" }

// Apply renders the prompt and requests a correction. The timeout lives on the
// ctx supplied by the caller (the session controller), keeping all timeout
// policy in one place, consistent with the local correction path.
func (s *Strategy) Apply(ctx context.Context, text string) (string, error) {
	prompt := strings.ReplaceAll(s.promptTmpl, s.placeholder, text)
	return s.client.Complete(ctx, prompt)
}

// Close releases no resources; the HTTP client needs no teardown.
func (s *Strategy) Close() error { return nil }
