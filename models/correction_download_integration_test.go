package models

import (
	"context"
	"os"
	"testing"
	"time"
)

// This test is opt-in because it downloads about 200 MiB from GitHub Releases.
// It validates the exact production URLs and archive layout used by both the
// CLI and tray downloader.
func TestCorrectionReleaseDownloads(t *testing.T) {
	if os.Getenv("MODEL_DOWNLOAD_INTEGRATION") != "1" {
		t.Skip("set MODEL_DOWNLOAD_INTEGRATION=1 to download and verify correction release assets")
	}

	for _, strategy := range []string{CorrectionStrategyMDCSpell, CorrectionStrategyMacBERT} {
		t.Run(strategy, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			set := ForCorrectionStrategy(strategy)
			baseDir := t.TempDir()
			if err := DownloadMissing(ctx, baseDir, set, func(message string) { t.Log(message) }, nil); err != nil {
				t.Fatalf("DownloadMissing(%s): %v", strategy, err)
			}
			if missing := Check(baseDir, set); len(missing) != 0 {
				t.Fatalf("downloaded %s still missing: %#v", strategy, missing)
			}
		})
	}
}
