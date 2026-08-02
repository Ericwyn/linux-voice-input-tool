package models

import "testing"

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
