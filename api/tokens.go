package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureTokensTxt converts a ModelScope tokens.json (JSON array of strings)
// into the tokens.txt format required by sherpa-onnx (one "token index" per line).
// If tokens.txt already exists next to the source file, it returns its path directly.
func EnsureTokensTxt(tokensPath string) (string, error) {
	if strings.HasSuffix(tokensPath, ".txt") {
		if _, err := os.Stat(tokensPath); err == nil {
			return tokensPath, nil
		}
		return "", fmt.Errorf("tokens file not found: %s", tokensPath)
	}

	tokensDir := filepath.Dir(tokensPath)
	outputPath := filepath.Join(tokensDir, "tokens.txt")

	if info, err := os.Stat(outputPath); err == nil && info.Size() > 0 {
		return outputPath, nil
	}

	data, err := os.ReadFile(tokensPath)
	if err != nil {
		return "", fmt.Errorf("read tokens file: %w", err)
	}

	var tokens []string
	if err := json.Unmarshal(data, &tokens); err != nil {
		return "", fmt.Errorf("parse tokens.json: %w", err)
	}

	var builder strings.Builder
	for index, token := range tokens {
		builder.WriteString(token)
		builder.WriteByte(' ')
		builder.WriteString(fmt.Sprintf("%d", index))
		builder.WriteByte('\n')
	}

	if err := os.WriteFile(outputPath, []byte(builder.String()), 0644); err != nil {
		return "", fmt.Errorf("write tokens.txt: %w", err)
	}

	return outputPath, nil
}
