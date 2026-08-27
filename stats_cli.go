package main

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"linux-voice-input/config"
	"linux-voice-input/stats"
)

type statsQuery struct {
	days      int
	monthYear int
	month     time.Month
}

func runStatsMode(cfg *config.Config, args []string, writer io.Writer) error {
	query, err := parseStatsQuery(args)
	if err != nil {
		return err
	}
	return renderStats(config.StatsDir(), cfg.Statistics.Enabled, query, time.Now(), writer)
}

func parseStatsQuery(args []string) (statsQuery, error) {
	var query statsQuery
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--stats":
			continue
		case argument == "--days":
			if index+1 >= len(args) {
				return statsQuery{}, fmt.Errorf("--days requires a positive integer")
			}
			index++
			if err := setStatsDays(&query, args[index]); err != nil {
				return statsQuery{}, err
			}
		case strings.HasPrefix(argument, "--days="):
			if err := setStatsDays(&query, strings.TrimPrefix(argument, "--days=")); err != nil {
				return statsQuery{}, err
			}
		case argument == "--month":
			if index+1 >= len(args) {
				return statsQuery{}, fmt.Errorf("--month requires YYYY-MM")
			}
			index++
			if err := setStatsMonth(&query, args[index]); err != nil {
				return statsQuery{}, err
			}
		case strings.HasPrefix(argument, "--month="):
			if err := setStatsMonth(&query, strings.TrimPrefix(argument, "--month=")); err != nil {
				return statsQuery{}, err
			}
		default:
			return statsQuery{}, fmt.Errorf("unknown --stats option %q", argument)
		}
	}
	if query.days > 0 && query.monthYear > 0 {
		return statsQuery{}, fmt.Errorf("--days and --month cannot be used together")
	}
	return query, nil
}

func setStatsDays(query *statsQuery, value string) error {
	days, err := strconv.Atoi(value)
	if err != nil || days <= 0 || days > 36500 {
		return fmt.Errorf("--days must be between 1 and 36500")
	}
	if query.days != 0 {
		return fmt.Errorf("--days may only be specified once")
	}
	query.days = days
	return nil
}

func setStatsMonth(query *statsQuery, value string) error {
	month, err := time.Parse("2006-01", value)
	if err != nil {
		return fmt.Errorf("--month must use YYYY-MM: %w", err)
	}
	if query.monthYear != 0 {
		return fmt.Errorf("--month may only be specified once")
	}
	query.monthYear = month.Year()
	query.month = month.Month()
	return nil
}

func renderStats(root string, enabled bool, query statsQuery, now time.Time, writer io.Writer) error {
	state := "已暂停"
	if enabled {
		state = "已启用"
	}
	if _, err := fmt.Fprintf(writer, "语音输入统计（%s）\n数据目录: %s\n", state, root); err != nil {
		return err
	}

	if query.days > 0 {
		aggregate, err := stats.LoadRecent(root, now, query.days)
		if err != nil {
			return err
		}
		if err := writeStatsAggregate(writer, fmt.Sprintf("最近%d天", query.days), aggregate); err != nil {
			return err
		}
		return writeEngineStats(writer, aggregate.Engines)
	}

	if query.monthYear > 0 {
		aggregate, err := stats.LoadMonth(root, query.monthYear, query.month, now.Location())
		if err != nil {
			return err
		}
		if err := writeStatsAggregate(writer,
			fmt.Sprintf("%04d-%02d", query.monthYear, query.month), aggregate); err != nil {
			return err
		}
		return writeEngineStats(writer, aggregate.Engines)
	}

	today, err := stats.LoadRecent(root, now, 1)
	if err != nil {
		return err
	}
	recent7, err := stats.LoadRecent(root, now, 7)
	if err != nil {
		return err
	}
	recent30, err := stats.LoadRecent(root, now, 30)
	if err != nil {
		return err
	}
	all, err := stats.LoadAll(root)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		label     string
		aggregate stats.Aggregate
	}{
		{label: "今日", aggregate: today},
		{label: "最近7天", aggregate: recent7},
		{label: "最近30天", aggregate: recent30},
		{label: "全部累计", aggregate: all},
	} {
		if err := writeStatsAggregate(writer, item.label, item.aggregate); err != nil {
			return err
		}
	}
	return writeEngineStats(writer, all.Engines)
}

func writeStatsAggregate(writer io.Writer, label string, aggregate stats.Aggregate) error {
	_, err := fmt.Fprintf(writer, "%s: %s\n", label, stats.FormatCounters(aggregate.Totals))
	return err
}

func writeEngineStats(writer io.Writer, engines map[string]stats.Counters) error {
	if len(engines) == 0 {
		return nil
	}
	names := make([]string, 0, len(engines))
	for name := range engines {
		names = append(names, name)
	}
	sort.Strings(names)
	if _, err := fmt.Fprintln(writer, "按引擎:"); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := fmt.Fprintf(writer, "  %s: %s\n", name, stats.FormatCounters(engines[name])); err != nil {
			return err
		}
	}
	return nil
}
