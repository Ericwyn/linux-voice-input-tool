package main

import (
	"strings"
	"testing"
	"time"
)

func TestReadJSONLCases(t *testing.T) {
	input := strings.NewReader("\n{\"input\":\"因该让坐\",\"expected\":\"应该让座\"}\n{\"input\":\"今天天气很好\"}\n")
	cases, err := readJSONLCases(input)
	if err != nil {
		t.Fatalf("readJSONLCases() error: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("case count = %d", len(cases))
	}
	if cases[0].Input != "因该让坐" || cases[0].Expected != "应该让座" {
		t.Fatalf("first case = %#v", cases[0])
	}
	if cases[1].Input != "今天天气很好" || cases[1].Expected != "" {
		t.Fatalf("second case = %#v", cases[1])
	}
}

func TestReadJSONLCasesRejectsMissingInput(t *testing.T) {
	if _, err := readJSONLCases(strings.NewReader("{\"expected\":\"x\"}\n")); err == nil {
		t.Fatal("missing input should fail")
	}
}

func TestReadPlainTextCases(t *testing.T) {
	cases, err := readPlainTextCases(strings.NewReader("第一句\n\n  第二句  \n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 || cases[0].Input != "第一句" || cases[1].Input != "第二句" {
		t.Fatalf("cases = %#v", cases)
	}
}

func TestPercentile(t *testing.T) {
	values := []time.Duration{10 * time.Millisecond, 40 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}
	if got := percentile(values, 0.95); got != 40*time.Millisecond {
		t.Fatalf("p95 = %v", got)
	}
	if got := percentile(nil, 0.95); got != 0 {
		t.Fatalf("empty percentile = %v", got)
	}
}
