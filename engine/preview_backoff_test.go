package engine

import (
	"testing"
	"time"
)

func TestNextPreviewDelay(t *testing.T) {
	const (
		base = 1000 * time.Millisecond
		max  = 5000 * time.Millisecond
	)

	cases := []struct {
		name    string
		backoff float64
		maxGap  time.Duration
		lastRun time.Duration
		want    time.Duration
	}{
		{
			name:    "short run floored at base interval",
			backoff: 1.0,
			maxGap:  max,
			lastRun: 200 * time.Millisecond,
			want:    base,
		},
		{
			name:    "medium run scales with backoff 1.0",
			backoff: 1.0,
			maxGap:  max,
			lastRun: 2500 * time.Millisecond,
			want:    2500 * time.Millisecond,
		},
		{
			name:    "long run capped at max interval",
			backoff: 1.0,
			maxGap:  max,
			lastRun: 9000 * time.Millisecond,
			want:    max,
		},
		{
			name:    "backoff factor applied",
			backoff: 0.5,
			maxGap:  max,
			lastRun: 3000 * time.Millisecond,
			want:    1500 * time.Millisecond,
		},
		{
			name:    "non-positive backoff falls back to 1.0",
			backoff: 0,
			maxGap:  max,
			lastRun: 2000 * time.Millisecond,
			want:    2000 * time.Millisecond,
		},
		{
			name:    "no ceiling when max disabled",
			backoff: 1.0,
			maxGap:  0,
			lastRun: 12000 * time.Millisecond,
			want:    12000 * time.Millisecond,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &senseVoiceSession{
				previewInterval:    base,
				previewMaxInterval: tc.maxGap,
				previewBackoff:     tc.backoff,
			}
			if got := s.nextPreviewDelay(tc.lastRun); got != tc.want {
				t.Fatalf("nextPreviewDelay(%v) = %v, want %v", tc.lastRun, got, tc.want)
			}
		})
	}
}
