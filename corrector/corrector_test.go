package corrector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// parseFrom is a small test helper that parses dictionary text directly from a
// string, bypassing the file layer so parser behavior can be asserted in
// isolation.
func parseFrom(t *testing.T, content string) []rule {
	t.Helper()
	rules, err := parseRules(strings.NewReader(content))
	if err != nil {
		t.Fatalf("parseRules returned error: %v", err)
	}
	return rules
}

func TestParseRulesBasic(t *testing.T) {
	rules := parseFrom(t, "线程 = 现成, 先成\n")

	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d: %+v", len(rules), rules)
	}
	for _, r := range rules {
		if r.correct != "线程" {
			t.Errorf("expected correct=线程, got %q", r.correct)
		}
	}
}

func TestParseRulesSkipsCommentsAndBlanks(t *testing.T) {
	content := `
# this is a comment
   # indented comment

线程 = 现成
`
	rules := parseFrom(t, content)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d: %+v", len(rules), rules)
	}
	if rules[0].variant != "现成" || rules[0].correct != "线程" {
		t.Errorf("unexpected rule: %+v", rules[0])
	}
}

func TestParseRulesFullWidthComma(t *testing.T) {
	rules := parseFrom(t, "缓存 = 换成，还成")
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules from full-width comma, got %d: %+v", len(rules), rules)
	}
}

func TestParseRulesSkipsNoOpAndEmptyVariants(t *testing.T) {
	// A variant identical to the correct term is a no-op and must be dropped;
	// trailing/empty variants from a dangling comma must be ignored.
	rules := parseFrom(t, "线程 = 线程, 现成, ,")
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d: %+v", len(rules), rules)
	}
	if rules[0].variant != "现成" {
		t.Errorf("expected only variant 现成, got %q", rules[0].variant)
	}
}

func TestParseRulesSkipsLinesWithoutSeparatorOrCorrect(t *testing.T) {
	content := `线程 现成
= 现成
线程 = 现成`
	rules := parseFrom(t, content)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d: %+v", len(rules), rules)
	}
}

func TestParseRulesSortsByVariantLengthDescending(t *testing.T) {
	// Longer variants must sort first so applyRules prefers the longest match.
	rules := parseFrom(t, "分布式锁 = 分布锁\n锁 = 索")
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if len([]rune(rules[0].variant)) < len([]rune(rules[1].variant)) {
		t.Errorf("rules not sorted by length descending: %+v", rules)
	}
}

func TestApplyRulesReplacesHomophone(t *testing.T) {
	rules := parseFrom(t, "线程 = 现成")
	got := applyRules(rules, "这里开一个现成来处理")
	want := "这里开一个线程来处理"
	if got != want {
		t.Errorf("applyRules = %q, want %q", got, want)
	}
}

func TestApplyRulesReplacesTransliteratedEnglish(t *testing.T) {
	rules := parseFrom(t, "Redis = 瑞迪斯\nDocker = 道克")
	got := applyRules(rules, "把数据写进瑞迪斯再用道克部署")
	want := "把数据写进Redis再用Docker部署"
	if got != want {
		t.Errorf("applyRules = %q, want %q", got, want)
	}
}

func TestApplyRulesPrefersLongestMatch(t *testing.T) {
	// With both a short and a long variant sharing a prefix, the longest wins,
	// so "分布锁" becomes "分布式锁" rather than leaving a stray "锁"->"索" hit.
	rules := parseFrom(t, "分布式锁 = 分布锁\n锁 = 索")
	got := applyRules(rules, "实现一个分布锁")
	want := "实现一个分布式锁"
	if got != want {
		t.Errorf("applyRules = %q, want %q", got, want)
	}
}

func TestApplyRulesNoCascade(t *testing.T) {
	// A correction must not be re-corrected by a later rule in the same pass:
	// "甲"->"乙" then "乙"->"丙" must yield "乙", not "丙".
	rules := []rule{{variant: "甲", correct: "乙"}, {variant: "乙", correct: "丙"}}
	got := applyRules(rules, "甲")
	if got != "乙" {
		t.Errorf("applyRules cascaded: got %q, want 乙", got)
	}
}

func TestApplyRulesEmptyRulesReturnsInput(t *testing.T) {
	if got := applyRules(nil, "原样返回"); got != "原样返回" {
		t.Errorf("expected unchanged text, got %q", got)
	}
}

func TestCorrectEmptyPathIsNoOp(t *testing.T) {
	c := New("")
	if got := c.Correct("现成"); got != "现成" {
		t.Errorf("empty-path corrector changed text: %q", got)
	}
}

func TestCorrectMissingFileIsNoOp(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "does-not-exist.txt"))
	if got := c.Correct("现成"); got != "现成" {
		t.Errorf("missing-file corrector changed text: %q", got)
	}
}

func TestCorrectFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dict.txt")
	if err := os.WriteFile(path, []byte("线程 = 现成\n"), 0644); err != nil {
		t.Fatalf("write dict: %v", err)
	}

	c := New(path)
	if got := c.Correct("开一个现成"); got != "开一个线程" {
		t.Errorf("Correct = %q, want 开一个线程", got)
	}
}

func TestCorrectHotReloadsOnFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dict.txt")
	if err := os.WriteFile(path, []byte("线程 = 现成\n"), 0644); err != nil {
		t.Fatalf("write dict: %v", err)
	}

	c := New(path)
	if got := c.Correct("现成"); got != "线程" {
		t.Fatalf("initial Correct = %q, want 线程", got)
	}

	// Rewrite with a different rule and bump mtime so the corrector reloads.
	// The explicit Chtimes guards against coarse mtime resolution making the
	// two writes look simultaneous on fast filesystems.
	if err := os.WriteFile(path, []byte("缓存 = 换成\n"), 0644); err != nil {
		t.Fatalf("rewrite dict: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if got := c.Correct("换成"); got != "缓存" {
		t.Errorf("after reload Correct(换成) = %q, want 缓存", got)
	}
	// The old rule must be gone after the reload.
	if got := c.Correct("现成"); got != "现成" {
		t.Errorf("stale rule still applied: Correct(现成) = %q, want 现成", got)
	}
}

func TestCorrectStopsAfterFileDeleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dict.txt")
	if err := os.WriteFile(path, []byte("线程 = 现成\n"), 0644); err != nil {
		t.Fatalf("write dict: %v", err)
	}

	c := New(path)
	if got := c.Correct("现成"); got != "线程" {
		t.Fatalf("initial Correct = %q, want 线程", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove dict: %v", err)
	}
	if got := c.Correct("现成"); got != "现成" {
		t.Errorf("corrections continued after file deleted: %q", got)
	}
}
