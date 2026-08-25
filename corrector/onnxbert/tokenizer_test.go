package onnxbert

import (
	"reflect"
	"testing"
)

func testVocabulary() []string {
	return []string{
		"[PAD]", "[UNK]", "[CLS]", "[SEP]", "[MASK]",
		"今", "天", "气", "很", "好", "，", "hello", "world", "##s", "应", "因", "该", "座", "坐",
	}
}

func TestTokenizerEncodesChineseAndWordPiece(t *testing.T) {
	tokenizer, err := NewTokenizer(testVocabulary(), 32)
	if err != nil {
		t.Fatalf("NewTokenizer() error: %v", err)
	}

	encoding := tokenizer.Encode("今天天气很好，Hello worlds")

	wantTokens := []string{"[CLS]", "今", "天", "天", "气", "很", "好", "，", "hello", "world", "##s", "[SEP]"}
	if !reflect.DeepEqual(encoding.Tokens, wantTokens) {
		t.Fatalf("tokens = %v, want %v", encoding.Tokens, wantTokens)
	}
	wantRunePositions := []int{-1, 0, 1, 2, 3, 4, 5, -1, -1, -1, -1, -1}
	if !reflect.DeepEqual(encoding.RunePositions, wantRunePositions) {
		t.Fatalf("rune positions = %v, want %v", encoding.RunePositions, wantRunePositions)
	}
	if len(encoding.InputIDs) != len(wantTokens) || len(encoding.AttentionMask) != len(wantTokens) || len(encoding.TokenTypeIDs) != len(wantTokens) {
		t.Fatalf("tensor lengths differ: ids=%d mask=%d types=%d", len(encoding.InputIDs), len(encoding.AttentionMask), len(encoding.TokenTypeIDs))
	}
	for _, value := range encoding.AttentionMask {
		if value != 1 {
			t.Fatalf("attention mask contains %d, want all ones", value)
		}
	}
}

func TestTokenizerTruncatesAndKeepsSeparator(t *testing.T) {
	tokenizer, err := NewTokenizer(testVocabulary(), 5)
	if err != nil {
		t.Fatalf("NewTokenizer() error: %v", err)
	}

	encoding := tokenizer.Encode("今天天气很好")
	want := []string{"[CLS]", "今", "天", "天", "[SEP]"}
	if !reflect.DeepEqual(encoding.Tokens, want) {
		t.Fatalf("tokens = %v, want %v", encoding.Tokens, want)
	}
}

func TestTokenizerRejectsVocabularyWithoutRequiredTokens(t *testing.T) {
	if _, err := NewTokenizer([]string{"[UNK]", "[CLS]"}, 8); err == nil {
		t.Fatal("NewTokenizer() expected missing special token error")
	}
}

func TestIsSingleHanToken(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"应", true},
		{"𠀀", true},
		{"应该", false},
		{"##应", false},
		{"A", false},
		{"，", false},
	}
	for _, test := range tests {
		if got := isSingleHanToken(test.token); got != test.want {
			t.Errorf("isSingleHanToken(%q) = %v, want %v", test.token, got, test.want)
		}
	}
}
