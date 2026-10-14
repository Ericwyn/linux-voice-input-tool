package session

import "testing"

func TestStripTrailingSymbols(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"chinese period", "今天天气不错。", "今天天气不错"},
		{"chinese comma", "你好，", "你好"},
		{"english period and space", "hello world. ", "hello world"},
		{"multiple trailing marks", "好的！！！  ", "好的"},
		{"trailing whitespace only", "继续说 ", "继续说"},
		{"ascii period", "test.", "test"},
		{"no trailing symbol", "没有符号", "没有符号"},
		{"empty", "", ""},
		{"only symbols", "。。！", ""},
		{"interior punctuation kept", "是的，我知道。", "是的，我知道"},
		{"trailing digit kept", "编号是 12", "编号是 12"},
		{"english trailing word kept", "code review", "code review"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripTrailingSymbols(tc.in); got != tc.want {
				t.Fatalf("stripTrailingSymbols(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
