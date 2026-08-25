// correction-eval runs one or more local Chinese correction models against
// identical text cases. It is intentionally independent of audio so model
// quality, over-correction and latency can be evaluated quickly.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"linux-voice-input/config"
	"linux-voice-input/corrector/onnxbert"
)

type evalCase struct {
	Input    string `json:"input"`
	Expected string `json:"expected,omitempty"`
	Category string `json:"category,omitempty"`
}

type modelStats struct {
	model         string
	total         int
	withExpected  int
	matched       int
	overCorrected int
	errors        int
	durations     []time.Duration
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "correction-eval:", err)
		os.Exit(1)
	}
}

func run() error {
	defaults := config.DefaultConfig()
	modelsFlag := flag.String("models", config.CorrectionStrategyMDCSpell+","+config.CorrectionStrategyMacBERT, "comma-separated correction strategies")
	textFlag := flag.String("text", "", "one sentence to evaluate; positional arguments add more sentences")
	casesPath := flag.String("cases", "", "JSONL file with input/expected cases")
	modelDir := flag.String("model-dir", defaults.CorrectionsModelDir(), "correction model root")
	runtimePath := flag.String("runtime", "", "path to libonnxruntime; auto-detected when empty")
	threshold := flag.Float64("threshold", defaults.Corrections.ConfidenceThreshold, "minimum predicted-token probability")
	margin := flag.Float64("margin", defaults.Corrections.MinMargin, "minimum probability gain over original token")
	maxLength := flag.Int("max-length", defaults.Corrections.MaxLength, "maximum BERT tokens per sentence")
	threads := flag.Int("threads", defaults.Corrections.NumThreads, "ONNX Runtime intra-op threads")
	timeout := flag.Duration("timeout", time.Duration(defaults.Corrections.TimeoutMs)*time.Millisecond, "soft timeout per sentence")
	flag.Parse()

	cases, err := collectCases(*textFlag, flag.Args(), *casesPath, os.Stdin)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return errors.New("no input; pass --text, positional sentences, --cases, or stdin lines")
	}

	runtime := resolveRuntimePath(*runtimePath, defaults.CorrectionsRuntimePath())
	strategies := splitModels(*modelsFlag)
	if len(strategies) == 0 {
		return errors.New("--models is empty")
	}

	fmt.Printf("cases=%d model_dir=%s runtime=%s threshold=%.2f margin=%.2f max_length=%d threads=%d\n",
		len(cases), *modelDir, runtime, *threshold, *margin, *maxLength, *threads)

	anySuccess := false
	for _, strategyName := range strategies {
		cfg := config.DefaultConfig()
		cfg.Corrections.ModelDir = *modelDir
		paths, err := cfg.CorrectionModelPaths(strategyName)
		if err != nil {
			fmt.Printf("\n[%s] CONFIG ERROR: %v\n", strategyName, err)
			continue
		}
		strategy, err := onnxbert.New(onnxbert.Config{
			Name:                strategyName,
			ModelPath:           paths.Model,
			VocabPath:           paths.Vocab,
			RuntimePath:         runtime,
			ConfidenceThreshold: *threshold,
			MinMargin:           *margin,
			MaxLength:           *maxLength,
			NumThreads:          *threads,
		})
		if err != nil {
			fmt.Printf("\n[%s] CONFIG ERROR: %v\n", strategyName, err)
			continue
		}

		stats := evaluateModel(strategy, cases, *timeout)
		_ = strategy.Close()
		printSummary(stats)
		if stats.errors < stats.total {
			anySuccess = true
		}
	}
	if !anySuccess {
		return errors.New("every model evaluation failed; check model and runtime paths")
	}
	return nil
}

func collectCases(text string, positional []string, casesPath string, stdin io.Reader) ([]evalCase, error) {
	var cases []evalCase
	if strings.TrimSpace(text) != "" {
		cases = append(cases, evalCase{Input: strings.TrimSpace(text)})
	}
	for _, value := range positional {
		if value = strings.TrimSpace(value); value != "" {
			cases = append(cases, evalCase{Input: value})
		}
	}
	if casesPath != "" {
		file, err := os.Open(casesPath)
		if err != nil {
			return nil, fmt.Errorf("open cases: %w", err)
		}
		fromFile, readErr := readJSONLCases(file)
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		cases = append(cases, fromFile...)
	}

	// A pipe is consumed only when no explicit source was supplied. This keeps
	// interactive terminals from blocking and avoids surprising case merging.
	if len(cases) == 0 && stdin != nil {
		if file, ok := stdin.(*os.File); !ok || !isTerminal(file) {
			fromStdin, err := readPlainTextCases(stdin)
			if err != nil {
				return nil, err
			}
			cases = append(cases, fromStdin...)
		}
	}
	return cases, nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func readJSONLCases(reader io.Reader) ([]evalCase, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var cases []evalCase
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item evalCase
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, fmt.Errorf("parse JSONL line %d: %w", lineNumber, err)
		}
		item.Input = strings.TrimSpace(item.Input)
		if item.Input == "" {
			return nil, fmt.Errorf("JSONL line %d has empty input", lineNumber)
		}
		cases = append(cases, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read JSONL cases: %w", err)
	}
	return cases, nil
}

func readPlainTextCases(reader io.Reader) ([]evalCase, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var cases []evalCase
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			cases = append(cases, evalCase{Input: line})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	return cases, nil
}

func splitModels(value string) []string {
	seen := make(map[string]bool)
	var models []string
	for _, raw := range strings.Split(value, ",") {
		name := strings.TrimSpace(raw)
		if name != "" && !seen[name] {
			seen[name] = true
			models = append(models, name)
		}
	}
	return models
}

func resolveRuntimePath(explicit, configured string) string {
	if explicit != "" {
		return explicit
	}
	if info, err := os.Stat(configured); err == nil && info.Size() > 0 {
		return configured
	}
	developmentPath := filepath.Join("dist", "linux-voice-input", "lib", filepath.Base(configured))
	if absolute, err := filepath.Abs(developmentPath); err == nil {
		if info, statErr := os.Stat(absolute); statErr == nil && info.Size() > 0 {
			return absolute
		}
	}
	return configured
}

func evaluateModel(strategy *onnxbert.Strategy, cases []evalCase, timeout time.Duration) modelStats {
	stats := modelStats{model: strategy.Name(), total: len(cases)}
	fmt.Printf("\n[%s]\n", strategy.Name())
	for index, item := range cases {
		ctx := context.Background()
		cancel := func() {}
		if timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, timeout)
		}
		start := time.Now()
		output, err := strategy.Apply(ctx, item.Input)
		duration := time.Since(start)
		cancel()
		stats.durations = append(stats.durations, duration)

		status := "observed"
		if item.Expected != "" {
			stats.withExpected++
			if output == item.Expected && err == nil {
				status = "PASS"
				stats.matched++
			} else {
				status = "MISS"
			}
			if item.Expected == item.Input && output != item.Input {
				stats.overCorrected++
				status = "OVERCORRECT"
			}
		}
		if err != nil {
			stats.errors++
			status = "ERROR"
		}

		fmt.Printf("  #%d %-11s %6dms", index+1, status, duration.Milliseconds())
		if item.Category != "" {
			fmt.Printf(" category=%s", item.Category)
		}
		fmt.Printf("\n    input:    %s\n    output:   %s\n", item.Input, output)
		if item.Expected != "" {
			fmt.Printf("    expected: %s\n", item.Expected)
		}
		if err != nil {
			fmt.Printf("    error:    %v\n", err)
		}
	}
	return stats
}

func printSummary(stats modelStats) {
	var total time.Duration
	for _, duration := range stats.durations {
		total += duration
	}
	average := time.Duration(0)
	if len(stats.durations) > 0 {
		average = total / time.Duration(len(stats.durations))
	}
	fmt.Printf("  summary: matched=%d/%d overcorrected=%d errors=%d avg=%dms p95=%dms",
		stats.matched, stats.withExpected, stats.overCorrected, stats.errors,
		average.Milliseconds(), percentile(stats.durations, 0.95).Milliseconds())
	if len(stats.durations) > 0 {
		fmt.Printf(" first_load_included=%dms", stats.durations[0].Milliseconds())
	}
	fmt.Println()
}

func percentile(values []time.Duration, quantile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if quantile <= 0 {
		return sorted[0]
	}
	if quantile >= 1 {
		return sorted[len(sorted)-1]
	}
	index := int(mathCeil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

// Kept local to avoid importing math solely for one integer rank operation.
func mathCeil(value float64) float64 {
	integer := int64(value)
	if float64(integer) == value {
		return value
	}
	return float64(integer + 1)
}
