// Package llm implements an LLM-based final-text correction strategy. It calls
// an OpenAI-compatible /v1/chat/completions endpoint with the recognized text
// rendered into a user-configured prompt, and returns the model's corrected
// text. It is engine-agnostic and plugs into the corrector.Service pipeline
// through the Strategy type, inheriting the fail-open contract: any error is
// returned to the service, which keeps the previous text.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client is a minimal OpenAI-compatible chat-completions client. The hard
// timeout is supplied per call via the request context rather than a
// Client.Timeout, so the caller controls cancellation precisely.
type Client struct {
	baseURL     string
	apiKey      string
	model       string
	extraParams map[string]any
	http        *http.Client
}

// NewClient builds a client for the given endpoint. extraParams, when set, is
// merged into every request body (see Complete). baseURL may include or omit a
// trailing slash; "/chat/completions" is appended.
func NewClient(baseURL, apiKey, model string, extraParams map[string]any) *Client {
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		apiKey:      apiKey,
		model:       model,
		extraParams: extraParams,
		http:        &http.Client{},
	}
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Complete sends a single user-message chat completion and returns the trimmed
// assistant content. The ctx carries the hard timeout; a cancelled ctx aborts
// the in-flight request so the caller never blocks past its budget.
//
// The request body is assembled as a map so user-supplied extraParams can set
// or override arbitrary fields (max_tokens, top_p, even temperature or model)
// for broad server compatibility. The messages field is written last and is
// therefore authoritative: extraParams can never displace the prompt.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	payload := map[string]any{
		"model":       c.model,
		"temperature": 0,
		"stream":      false,
	}
	for k, v := range c.extraParams {
		payload[k] = v
	}
	payload["messages"] = []map[string]string{{"role": "user", "content": prompt}}

	buf, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("LLM API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("LLM response has no choices")
	}
	text := strings.TrimSpace(result.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("LLM returned empty content")
	}
	return text, nil
}
