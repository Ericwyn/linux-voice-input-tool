package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Aggregate is a merged view over one or more daily documents.
type Aggregate struct {
	Days    int
	Totals  Counters
	Engines map[string]Counters
}

// DailyPath returns the YYYY/MM/DD.json path for a local calendar date.
func DailyPath(root string, date time.Time) string {
	return filepath.Join(root, date.Format("2006"), date.Format("01"), date.Format("02")+".json")
}

// ReadDay reads one daily file. A missing file is a valid empty day.
func ReadDay(root string, date time.Time) (DailyDocument, error) {
	document, _, err := readDay(root, date)
	return document, err
}

func readDay(root string, date time.Time) (DailyDocument, bool, error) {
	path := DailyPath(root, date)
	document, err := readDailyPath(path, date.Format(time.DateOnly))
	if errors.Is(err, fs.ErrNotExist) {
		return newDailyDocument(date), false, nil
	}
	if err != nil {
		return DailyDocument{}, false, err
	}
	return document, true, nil
}

func readDailyPath(path, expectedDate string) (DailyDocument, error) {
	file, err := os.Open(path)
	if err != nil {
		return DailyDocument{}, err
	}
	defer file.Close()

	var document DailyDocument
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&document); err != nil {
		return DailyDocument{}, fmt.Errorf("decode statistics file %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return DailyDocument{}, fmt.Errorf("decode statistics file %s: %w", path, err)
	}
	if document.Version != SchemaVersion {
		return DailyDocument{}, fmt.Errorf(
			"statistics file %s has unsupported version %d (want %d)",
			path, document.Version, SchemaVersion,
		)
	}
	if document.Date != expectedDate {
		return DailyDocument{}, fmt.Errorf(
			"statistics file %s declares date %q (want %q)",
			path, document.Date, expectedDate,
		)
	}
	if document.Engines == nil {
		document.Engines = make(map[string]Counters)
	}
	return document, nil
}

func writeDay(root string, date time.Time, document DailyDocument) error {
	path := DailyPath(root, date)
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create statistics directory: %w", err)
	}

	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode statistics: %w", err)
	}
	data = append(data, '\n')

	temporary, err := os.CreateTemp(directory, ".stats-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary statistics file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set statistics permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write statistics: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync statistics: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close statistics: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace statistics file: %w", err)
	}
	removeTemporary = false
	return nil
}

// LoadRange merges every existing daily file in the inclusive local-date
// range. Missing dates contribute zero and are not counted in Aggregate.Days.
func LoadRange(root string, start, end time.Time) (Aggregate, error) {
	start = dayStart(start)
	end = dayStart(end.In(start.Location()))
	if end.Before(start) {
		return Aggregate{}, fmt.Errorf("statistics end date %s is before start date %s",
			end.Format(time.DateOnly), start.Format(time.DateOnly))
	}

	aggregate := newAggregate()
	for date := start; !date.After(end); date = date.AddDate(0, 0, 1) {
		document, exists, err := readDay(root, date)
		if err != nil {
			return Aggregate{}, err
		}
		if exists {
			aggregate.addDocument(document)
		}
	}
	return aggregate, nil
}

// LoadRecent merges today and the preceding days-1 local calendar days.
func LoadRecent(root string, now time.Time, days int) (Aggregate, error) {
	if days <= 0 {
		return Aggregate{}, fmt.Errorf("statistics days must be positive")
	}
	end := dayStart(now)
	start := end.AddDate(0, 0, -(days - 1))
	return LoadRange(root, start, end)
}

// LoadMonth merges one local calendar month.
func LoadMonth(root string, year int, month time.Month, location *time.Location) (Aggregate, error) {
	if month < time.January || month > time.December {
		return Aggregate{}, fmt.Errorf("invalid statistics month %d", month)
	}
	if location == nil {
		location = time.Local
	}
	start := time.Date(year, month, 1, 0, 0, 0, 0, location)
	end := start.AddDate(0, 1, -1)
	return LoadRange(root, start, end)
}

// LoadAll walks all valid YYYY/MM/DD.json paths and merges them. Files outside
// that layout are ignored, so temporary or user-created files cannot pollute a
// query; malformed daily files within the layout return a precise error.
func LoadAll(root string) (Aggregate, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if _, ok := dateFromPath(root, path); ok {
			paths = append(paths, path)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return newAggregate(), nil
	}
	if err != nil {
		return Aggregate{}, fmt.Errorf("walk statistics directory: %w", err)
	}
	sort.Strings(paths)

	aggregate := newAggregate()
	for _, path := range paths {
		date, _ := dateFromPath(root, path)
		document, err := readDailyPath(path, date.Format(time.DateOnly))
		if err != nil {
			return Aggregate{}, err
		}
		aggregate.addDocument(document)
	}
	return aggregate, nil
}

func newAggregate() Aggregate {
	return Aggregate{Engines: make(map[string]Counters)}
}

func (aggregate *Aggregate) addDocument(document DailyDocument) {
	aggregate.Days++
	aggregate.Totals.Add(document.Totals)
	if aggregate.Engines == nil {
		aggregate.Engines = make(map[string]Counters)
	}
	for engine, counters := range document.Engines {
		engineTotal := aggregate.Engines[engine]
		engineTotal.Add(counters)
		aggregate.Engines[engine] = engineTotal
	}
}

func dayStart(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

func dateFromPath(root, path string) (time.Time, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return time.Time{}, false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 3 || filepath.Ext(parts[2]) != ".json" {
		return time.Time{}, false
	}
	dateText := parts[0] + "-" + parts[1] + "-" + strings.TrimSuffix(parts[2], ".json")
	date, err := time.ParseInLocation(time.DateOnly, dateText, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return date, true
}
