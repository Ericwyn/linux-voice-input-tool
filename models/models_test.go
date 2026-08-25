package models

import (
	"reflect"
	"testing"
)

func TestDownloadProgressString(t *testing.T) {
	const mb = 1024 * 1024

	tests := []struct {
		name string
		p    DownloadProgress
		want string
	}{
		{
			name: "known total shows percent",
			p:    DownloadProgress{Downloaded: 5 * mb, Total: 10 * mb, BytesPerSec: 2 * mb},
			want: "下载中: 5.0/10.0 MB (50.0%) 2.0 MB/s",
		},
		{
			name: "unknown total omits percent",
			p:    DownloadProgress{Downloaded: 3 * mb, Total: -1, BytesPerSec: 1 * mb},
			want: "下载中: 3.0 MB 1.0 MB/s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestForCorrectionStrategy(t *testing.T) {
	tests := []struct {
		strategy string
		dir      string
		url      string
	}{
		{
			strategy: CorrectionStrategyMDCSpell,
			dir:      "macbert-mdcspell-v2",
			url:      "https://github.com/Ericwyn/linux-voice-input-tool/releases/download/correction-models-int8-v1/macbert-mdcspell-v2-int8-onnx.tar.bz2",
		},
		{
			strategy: CorrectionStrategyMacBERT,
			dir:      "macbert4csc",
			url:      "https://github.com/Ericwyn/linux-voice-input-tool/releases/download/correction-models-int8-v1/macbert4csc-int8-onnx.tar.bz2",
		},
	}

	for _, test := range tests {
		set := ForCorrectionStrategy(test.strategy)
		if len(set) != 1 {
			t.Fatalf("ForCorrectionStrategy(%q) returned %d models", test.strategy, len(set))
		}
		model := set[0]
		if model.Dir != test.dir || model.URL != test.url {
			t.Fatalf("model for %s = %#v", test.strategy, model)
		}
		if !reflect.DeepEqual(model.Files, []string{"model.int8.onnx", "vocab.txt"}) {
			t.Fatalf("required files for %s = %v", test.strategy, model.Files)
		}
	}
	if got := ForCorrectionStrategy(""); got != nil {
		t.Fatalf("empty strategy = %#v, want nil", got)
	}
	if got := ForCorrectionStrategy("unknown"); got != nil {
		t.Fatalf("unknown strategy = %#v, want nil", got)
	}
}

func TestRequiredDownloads(t *testing.T) {
	request := DownloadRequest{
		EngineName:         "sensevoice",
		EngineBaseDir:      "/models/asr",
		CorrectionsEnabled: true,
		CorrectionStrategy: CorrectionStrategyMDCSpell,
		CorrectionBaseDir:  "/models/correction",
	}
	groups, err := RequiredDownloads(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("group count = %d, want ASR + correction", len(groups))
	}
	if groups[0].BaseDir != request.EngineBaseDir || len(groups[0].Models) != 1 {
		t.Fatalf("engine group = %#v", groups[0])
	}
	if groups[1].BaseDir != request.CorrectionBaseDir || len(groups[1].Models) != 1 ||
		groups[1].Models[0].Dir != "macbert-mdcspell-v2" {
		t.Fatalf("correction group = %#v", groups[1])
	}

	groups, err = RequiredDownloads(DownloadRequest{EngineName: "whisper"})
	if err != nil || len(groups) != 0 {
		t.Fatalf("remote-only groups = %#v, err=%v", groups, err)
	}

	groups, err = RequiredDownloads(DownloadRequest{
		EngineName:         "whisper",
		CorrectionsEnabled: true,
		CorrectionStrategy: CorrectionStrategyMacBERT,
		CorrectionBaseDir:  "/models/correction",
	})
	if err != nil || len(groups) != 1 || groups[0].Models[0].Dir != "macbert4csc" {
		t.Fatalf("whisper + correction groups = %#v, err=%v", groups, err)
	}

	if _, err := RequiredDownloads(DownloadRequest{
		CorrectionsEnabled: true,
		CorrectionStrategy: "unknown",
	}); err == nil {
		t.Fatal("unknown correction strategy should fail")
	}
}
