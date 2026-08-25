// Package corrector applies a user-maintained correction dictionary to the
// final transcription text, before it is pasted. It runs after recognition and
// is engine-agnostic, so it fixes the two most common local-ASR mistakes for
// technical vocabulary regardless of which engine produced the text:
//
//  1. Chinese homophones, e.g. the model hears "现成" when the user said the
//     tech term "线程" (both read "xiàn chéng" / near-homophones).
//  2. English terms transliterated into Chinese, e.g. "瑞迪斯" for "Redis".
//
// The dictionary is a plain-text file the user edits freely; the Corrector
// watches its modification time and hot-reloads on change, so edits take effect
// without restarting the daemon.
package corrector

import (
	"bufio"
	"context"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// rule maps one wrong spelling (variant) to the desired correct spelling. A
// single correct term may have several variants, each stored as its own rule.
type rule struct {
	variant string
	correct string
}

// Corrector holds the parsed correction rules and lazily hot-reloads them from
// the dictionary file whenever the file's modification time changes. All
// methods are safe for concurrent use.
type Corrector struct {
	mu sync.Mutex

	// path is the dictionary file location. An empty path disables correction
	// entirely (Correct becomes a no-op).
	path string

	// loaded is true once we have attempted at least one load, so a missing
	// file (rules stays empty) is not retried on the mtime fast-path.
	loaded  bool
	modTime time.Time

	// rules is the immutable, length-sorted rule slice produced by the last
	// successful parse. It is replaced wholesale on reload, never mutated in
	// place, so a snapshot taken under the lock can be used without it.
	rules []rule
}

// New creates a Corrector bound to the given dictionary file path. The file is
// not read until the first Correct call, so construction never fails; a missing
// or unreadable file simply yields no corrections.
func New(path string) *Corrector {
	return &Corrector{path: path}
}

// Name implements Strategy.
func (c *Corrector) Name() string { return "dictionary" }

// Apply adapts the dictionary corrector to the service Strategy interface.
func (c *Corrector) Apply(_ context.Context, text string) (string, error) {
	return c.Correct(text), nil
}

// Close implements Strategy. The dictionary owns no persistent file handle.
func (c *Corrector) Close() error { return nil }

// Correct returns text with every configured variant replaced by its correct
// spelling. It first hot-reloads the dictionary if the file changed on disk,
// then applies the rules. With no path, no file, or an empty dictionary it
// returns the text unchanged.
func (c *Corrector) Correct(text string) string {
	if text == "" || c.path == "" {
		return text
	}

	c.mu.Lock()
	c.reloadIfChanged()
	rules := c.rules
	c.mu.Unlock()

	return applyRules(rules, text)
}

// reloadIfChanged reloads the dictionary when the file's modification time
// differs from the last load (or on the very first call). A stat error is
// treated as "file gone": the rules are cleared so a deleted dictionary stops
// correcting rather than keeping stale rules. Caller must hold c.mu.
func (c *Corrector) reloadIfChanged() {
	info, err := os.Stat(c.path)
	if err != nil {
		c.rules = nil
		c.loaded = true
		return
	}

	modTime := info.ModTime()
	if c.loaded && modTime.Equal(c.modTime) {
		return
	}

	rules, err := loadRules(c.path)
	if err != nil {
		// Keep whatever rules we already had rather than dropping corrections
		// because of a transient read error; try again on the next change.
		c.loaded = true
		c.modTime = modTime
		return
	}

	c.rules = rules
	c.modTime = modTime
	c.loaded = true
}

// loadRules reads and parses the dictionary file into a length-sorted rule
// slice ready for applyRules.
func loadRules(path string) ([]rule, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return parseRules(file)
}

// parseRules parses the dictionary format into a rule slice sorted by variant
// length descending, so applyRules prefers the longest match at each position.
//
// Format, one rule per line:
//
//	<correct> = <variant1>[, <variant2> ...]
//
// The correct term is on the left of '='; the comma-separated list on the right
// holds the wrong spellings that should be rewritten to it. Both the ASCII ','
// and the full-width Chinese '，' separate variants. Blank lines and lines whose
// first non-space character is '#' are ignored, so the file can carry comments.
// A variant equal to its own correct term is skipped (it would be a no-op), and
// a variant that is a blank string is ignored.
func parseRules(reader io.Reader) ([]rule, error) {
	scanner := bufio.NewScanner(reader)
	var rules []rule

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		separatorIndex := strings.Index(line, "=")
		if separatorIndex < 0 {
			continue
		}

		correct := strings.TrimSpace(line[:separatorIndex])
		variantsPart := line[separatorIndex+1:]
		if correct == "" {
			continue
		}

		for _, rawVariant := range strings.FieldsFunc(variantsPart, isVariantSeparator) {
			variant := strings.TrimSpace(rawVariant)
			if variant == "" || variant == correct {
				continue
			}
			rules = append(rules, rule{variant: variant, correct: correct})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(rules, func(firstIndex, secondIndex int) bool {
		return len(rules[firstIndex].variant) > len(rules[secondIndex].variant)
	})
	return rules, nil
}

// isVariantSeparator reports whether r separates two variants: either an ASCII
// comma or the full-width Chinese comma, so users can type the dictionary with
// a Chinese IME without switching punctuation.
func isVariantSeparator(candidate rune) bool {
	return candidate == ',' || candidate == '，'
}

// applyRules rewrites text by scanning left to right and, at each position,
// replacing the first (hence longest, since rules are length-sorted) matching
// variant with its correct term. Using a single forward pass means an already
// emitted correction is never re-examined, so corrections cannot cascade into
// one another (e.g. a rule producing text that another rule would then rewrite).
func applyRules(rules []rule, text string) string {
	if len(rules) == 0 {
		return text
	}

	var builder strings.Builder
	builder.Grow(len(text))

	position := 0
	for position < len(text) {
		remaining := text[position:]

		matchedRule := -1
		for index := range rules {
			if strings.HasPrefix(remaining, rules[index].variant) {
				matchedRule = index
				break
			}
		}

		if matchedRule >= 0 {
			builder.WriteString(rules[matchedRule].correct)
			position += len(rules[matchedRule].variant)
			continue
		}

		_, runeSize := utf8.DecodeRuneInString(remaining)
		builder.WriteString(remaining[:runeSize])
		position += runeSize
	}

	return builder.String()
}
