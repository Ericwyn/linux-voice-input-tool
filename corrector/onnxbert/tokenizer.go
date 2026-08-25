// Package onnxbert implements the small subset of BERT tokenization and
// masked-language-model inference needed by the local Chinese corrector.
package onnxbert

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode"
)

const defaultMaxLength = 128

// Encoding contains the three standard BERT inputs plus enough metadata to
// map a prediction back to the original Go rune. RunePositions is -1 for
// special tokens, punctuation, ASCII and WordPiece fragments, so the model can
// never rewrite those positions.
type Encoding struct {
	InputIDs      []int64
	AttentionMask []int64
	TokenTypeIDs  []int64
	Tokens        []string
	RunePositions []int
}

// Tokenizer is a BERT BasicTokenizer + greedy WordPiece tokenizer backed by a
// vocab.txt whose line number is the model token ID.
type Tokenizer struct {
	tokens    []string
	ids       map[string]int
	maxLength int
	unkID     int
	clsID     int
	sepID     int
}

// LoadTokenizer reads a Hugging Face BERT vocab.txt.
func LoadTokenizer(path string, maxLength int) (*Tokenizer, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open BERT vocabulary: %w", err)
	}
	defer file.Close()

	var vocabulary []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		vocabulary = append(vocabulary, strings.TrimSuffix(scanner.Text(), "\r"))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read BERT vocabulary: %w", err)
	}
	return NewTokenizer(vocabulary, maxLength)
}

// NewTokenizer constructs a tokenizer from an in-memory vocabulary. It is
// exported to make model-independent unit tests and custom embeddings easy.
func NewTokenizer(vocabulary []string, maxLength int) (*Tokenizer, error) {
	if maxLength <= 0 {
		maxLength = defaultMaxLength
	}
	if maxLength < 2 {
		return nil, fmt.Errorf("BERT max length must be at least 2")
	}

	ids := make(map[string]int, len(vocabulary))
	for id, token := range vocabulary {
		if _, exists := ids[token]; !exists {
			ids[token] = id
		}
	}
	required := []string{"[UNK]", "[CLS]", "[SEP]"}
	for _, token := range required {
		if _, ok := ids[token]; !ok {
			return nil, fmt.Errorf("BERT vocabulary is missing %s", token)
		}
	}

	return &Tokenizer{
		tokens:    append([]string(nil), vocabulary...),
		ids:       ids,
		maxLength: maxLength,
		unkID:     ids["[UNK]"],
		clsID:     ids["[CLS]"],
		sepID:     ids["[SEP]"],
	}, nil
}

// VocabularySize returns the model vocabulary size.
func (t *Tokenizer) VocabularySize() int { return len(t.tokens) }

// TokenID returns -1 when a token is absent.
func (t *Tokenizer) TokenID(token string) int {
	if id, ok := t.ids[token]; ok {
		return id
	}
	return -1
}

// Token returns an empty string for an out-of-range ID.
func (t *Tokenizer) Token(id int) string {
	if id < 0 || id >= len(t.tokens) {
		return ""
	}
	return t.tokens[id]
}

type basicToken struct {
	text         string
	runePosition int
	correctable  bool
}

// Encode tokenizes one text and always keeps room for the trailing [SEP].
// Text beyond maxLength is deliberately not represented; Strategy therefore
// leaves that suffix unchanged rather than risking a context-free rewrite.
func (t *Tokenizer) Encode(text string) Encoding {
	encoding := Encoding{
		InputIDs:      []int64{int64(t.clsID)},
		AttentionMask: []int64{1},
		TokenTypeIDs:  []int64{0},
		Tokens:        []string{"[CLS]"},
		RunePositions: []int{-1},
	}

	appendPiece := func(piece string, runePosition int) bool {
		if len(encoding.InputIDs) >= t.maxLength-1 {
			return false
		}
		id, ok := t.ids[piece]
		if !ok {
			id = t.unkID
			piece = "[UNK]"
			runePosition = -1
		}
		encoding.InputIDs = append(encoding.InputIDs, int64(id))
		encoding.AttentionMask = append(encoding.AttentionMask, 1)
		encoding.TokenTypeIDs = append(encoding.TokenTypeIDs, 0)
		encoding.Tokens = append(encoding.Tokens, piece)
		encoding.RunePositions = append(encoding.RunePositions, runePosition)
		return true
	}

	truncated := false
	for _, token := range basicTokenize(text) {
		pieces := t.wordPieces(token.text)
		for _, piece := range pieces {
			position := -1
			if token.correctable && len(pieces) == 1 && piece == token.text {
				position = token.runePosition
			}
			if !appendPiece(piece, position) {
				truncated = true
				break
			}
		}
		if truncated {
			break
		}
	}

	encoding.InputIDs = append(encoding.InputIDs, int64(t.sepID))
	encoding.AttentionMask = append(encoding.AttentionMask, 1)
	encoding.TokenTypeIDs = append(encoding.TokenTypeIDs, 0)
	encoding.Tokens = append(encoding.Tokens, "[SEP]")
	encoding.RunePositions = append(encoding.RunePositions, -1)
	return encoding
}

func basicTokenize(text string) []basicToken {
	runes := []rune(text)
	tokens := make([]basicToken, 0, len(runes))
	var buffered []rune

	flush := func() {
		if len(buffered) == 0 {
			return
		}
		for index, candidate := range buffered {
			buffered[index] = unicode.ToLower(candidate)
		}
		tokens = append(tokens, basicToken{text: string(buffered), runePosition: -1})
		buffered = nil
	}

	for position, candidate := range runes {
		switch {
		case unicode.IsSpace(candidate) || unicode.IsControl(candidate):
			flush()
		case isHan(candidate):
			flush()
			tokens = append(tokens, basicToken{text: string(candidate), runePosition: position, correctable: true})
		case unicode.IsPunct(candidate) || unicode.IsSymbol(candidate):
			flush()
			tokens = append(tokens, basicToken{text: string(candidate), runePosition: -1})
		default:
			buffered = append(buffered, candidate)
		}
	}
	flush()
	return tokens
}

func (t *Tokenizer) wordPieces(token string) []string {
	if _, ok := t.ids[token]; ok {
		return []string{token}
	}

	runes := []rune(token)
	if len(runes) > 100 {
		return []string{"[UNK]"}
	}
	var pieces []string
	for start := 0; start < len(runes); {
		end := len(runes)
		piece := ""
		for start < end {
			candidate := string(runes[start:end])
			if start > 0 {
				candidate = "##" + candidate
			}
			if _, ok := t.ids[candidate]; ok {
				piece = candidate
				break
			}
			end--
		}
		if piece == "" {
			return []string{"[UNK]"}
		}
		pieces = append(pieces, piece)
		start = end
	}
	return pieces
}

func isSingleHanToken(token string) bool {
	if strings.HasPrefix(token, "##") {
		return false
	}
	runes := []rune(token)
	return len(runes) == 1 && isHan(runes[0])
}

func isHan(candidate rune) bool {
	return (candidate >= 0x3400 && candidate <= 0x4DBF) ||
		(candidate >= 0x4E00 && candidate <= 0x9FFF) ||
		(candidate >= 0xF900 && candidate <= 0xFAFF) ||
		(candidate >= 0x20000 && candidate <= 0x2FA1F)
}
