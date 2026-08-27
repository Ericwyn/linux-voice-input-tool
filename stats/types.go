// Package stats records privacy-preserving, aggregate voice-input usage data.
// It deliberately stores no recognized or corrected text: callers turn one
// successful paste into a numeric Event before handing it to Tracker.
package stats

import (
	"fmt"
	"time"
	"unicode"
)

const SchemaVersion = 1

// Counters is the common aggregate payload used for one day and each engine
// within that day. Durations are integer milliseconds so the on-disk JSON is
// stable and convenient for tools written in any language.
type Counters struct {
	SuccessfulInputs         uint64            `json:"successful_inputs"`
	Characters               uint64            `json:"characters"`
	HanCharacters            uint64            `json:"han_characters"`
	RecordingDurationMS      uint64            `json:"recording_duration_ms"`
	InputDurationMS          uint64            `json:"input_duration_ms"`
	CorrectedInputs          uint64            `json:"corrected_inputs"`
	CorrectionEdits          uint64            `json:"correction_edits"`
	CorrectionStrategyInputs map[string]uint64 `json:"correction_strategy_inputs,omitempty"`
}

// Add merges another aggregate into c.
func (c *Counters) Add(other Counters) {
	c.SuccessfulInputs += other.SuccessfulInputs
	c.Characters += other.Characters
	c.HanCharacters += other.HanCharacters
	c.RecordingDurationMS += other.RecordingDurationMS
	c.InputDurationMS += other.InputDurationMS
	c.CorrectedInputs += other.CorrectedInputs
	c.CorrectionEdits += other.CorrectionEdits
	if len(other.CorrectionStrategyInputs) > 0 {
		if c.CorrectionStrategyInputs == nil {
			c.CorrectionStrategyInputs = make(map[string]uint64)
		}
		for name, count := range other.CorrectionStrategyInputs {
			c.CorrectionStrategyInputs[name] += count
		}
	}
}

// DailyDocument is one YYYY/MM/DD.json file. Totals is the source of truth for
// the whole day; Engines provides the same counters split by recognition
// engine, without duplicating individual events or text.
type DailyDocument struct {
	Version   int                 `json:"version"`
	Date      string              `json:"date"`
	UpdatedAt time.Time           `json:"updated_at"`
	Totals    Counters            `json:"totals"`
	Engines   map[string]Counters `json:"engines,omitempty"`
}

// Event contains only the numeric facts needed to update one daily file.
// AppliedStrategies lists strategies that changed text during their stage; it
// never contains the text itself.
type Event struct {
	OccurredAt        time.Time
	Engine            string
	Characters        uint64
	HanCharacters     uint64
	RecordingTime     time.Duration
	InputTime         time.Duration
	Corrected         bool
	CorrectionEdits   uint64
	AppliedStrategies []string
}

// NewEvent measures final output and correction impact, then discards both text
// arguments. The returned Event is safe to queue and persist without exposing
// recognized content.
func NewEvent(
	occurredAt time.Time,
	engine string,
	rawText string,
	finalText string,
	recordingTime time.Duration,
	inputTime time.Duration,
	appliedStrategies []string,
) Event {
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	characters, hanCharacters := CountCharacters(finalText)
	corrected := rawText != finalText
	var correctionEdits uint64
	if corrected {
		correctionEdits = uint64(RuneEditDistance(rawText, finalText))
	}

	return Event{
		OccurredAt:        occurredAt,
		Engine:            engine,
		Characters:        characters,
		HanCharacters:     hanCharacters,
		RecordingTime:     maxDuration(recordingTime),
		InputTime:         maxDuration(inputTime),
		Corrected:         corrected,
		CorrectionEdits:   correctionEdits,
		AppliedStrategies: uniqueNonEmpty(appliedStrategies),
	}
}

// CountCharacters returns the number of non-whitespace Unicode code points and
// the subset belonging to the Unicode Han script.
func CountCharacters(text string) (characters, hanCharacters uint64) {
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		characters++
		if unicode.Is(unicode.Han, r) {
			hanCharacters++
		}
	}
	return characters, hanCharacters
}

// RuneEditDistance computes Levenshtein distance over Unicode code points, not
// UTF-8 bytes. It uses O(min(m,n)) memory because transcriptions can be long.
func RuneEditDistance(first, second string) int {
	a := []rune(first)
	b := []rune(second)

	// Correction output normally differs at only a few positions. Removing the
	// common edges keeps that common case near-linear instead of running the
	// dynamic program across an entire long transcription.
	commonPrefix := 0
	for commonPrefix < len(a) && commonPrefix < len(b) && a[commonPrefix] == b[commonPrefix] {
		commonPrefix++
	}
	a = a[commonPrefix:]
	b = b[commonPrefix:]
	for len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] {
		a = a[:len(a)-1]
		b = b[:len(b)-1]
	}

	if len(a) > len(b) {
		a, b = b, a
	}

	previous := make([]int, len(a)+1)
	current := make([]int, len(a)+1)
	for i := range previous {
		previous[i] = i
	}

	for row, rightRune := range b {
		current[0] = row + 1
		for column, leftRune := range a {
			cost := 0
			if leftRune != rightRune {
				cost = 1
			}
			current[column+1] = min(
				current[column]+1,
				previous[column+1]+1,
				previous[column]+cost,
			)
		}
		previous, current = current, previous
	}
	return previous[len(a)]
}

func (event Event) counters() Counters {
	counters := Counters{
		SuccessfulInputs:    1,
		Characters:          event.Characters,
		HanCharacters:       event.HanCharacters,
		RecordingDurationMS: durationMilliseconds(event.RecordingTime),
		InputDurationMS:     durationMilliseconds(event.InputTime),
		CorrectionEdits:     event.CorrectionEdits,
	}
	if event.Corrected {
		counters.CorrectedInputs = 1
	}
	if len(event.AppliedStrategies) > 0 {
		counters.CorrectionStrategyInputs = make(map[string]uint64, len(event.AppliedStrategies))
		for _, strategy := range event.AppliedStrategies {
			counters.CorrectionStrategyInputs[strategy] = 1
		}
	}
	return counters
}

func (document *DailyDocument) apply(event Event) {
	counters := event.counters()
	document.Totals.Add(counters)
	if document.Engines == nil {
		document.Engines = make(map[string]Counters)
	}
	engine := event.Engine
	if engine == "" {
		engine = "unknown"
	}
	engineCounters := document.Engines[engine]
	engineCounters.Add(counters)
	document.Engines[engine] = engineCounters
	document.UpdatedAt = event.OccurredAt
}

func newDailyDocument(date time.Time) DailyDocument {
	return DailyDocument{
		Version: SchemaVersion,
		Date:    date.Format(time.DateOnly),
		Engines: make(map[string]Counters),
	}
}

func durationMilliseconds(duration time.Duration) uint64 {
	if duration <= 0 {
		return 0
	}
	return uint64(duration / time.Millisecond)
}

func maxDuration(duration time.Duration) time.Duration {
	if duration < 0 {
		return 0
	}
	return duration
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// FormatDuration renders stored milliseconds compactly for CLI and tray views.
func FormatDuration(milliseconds uint64) string {
	if milliseconds == 0 {
		return "0秒"
	}
	totalSeconds := (milliseconds + 500) / 1000
	if totalSeconds == 0 {
		return "<1秒"
	}
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	switch {
	case hours > 0:
		return fmt.Sprintf("%d小时%d分%d秒", hours, minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%d分%d秒", minutes, seconds)
	default:
		return fmt.Sprintf("%d秒", seconds)
	}
}

// FormatCounters produces one compact human-readable summary.
func FormatCounters(counters Counters) string {
	return fmt.Sprintf(
		"%d 次，%d 字符（%d 汉字），录音 %s，端到端 %s，纠错 %d 次/%d 处",
		counters.SuccessfulInputs,
		counters.Characters,
		counters.HanCharacters,
		FormatDuration(counters.RecordingDurationMS),
		FormatDuration(counters.InputDurationMS),
		counters.CorrectedInputs,
		counters.CorrectionEdits,
	)
}
