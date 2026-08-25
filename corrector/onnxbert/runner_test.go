package onnxbert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimePathFromMaps(t *testing.T) {
	maps := strings.NewReader("7f000-7f100 r-xp 00000000 00:00 0 /opt/app/lib/libonnxruntime.so\n" +
		"7f200-7f300 r-xp 00000000 00:00 0 /opt/app/lib/libsherpa-onnx-c-api.so\n")
	if got := runtimePathFromMaps(maps); got != "/opt/app/lib/libonnxruntime.so" {
		t.Fatalf("runtimePathFromMaps() = %q", got)
	}
}

func TestResolveRuntimeLibraryPrefersExistingConfiguredPath(t *testing.T) {
	directory := t.TempDir()
	configured := filepath.Join(directory, "libonnxruntime.so")
	if err := os.WriteFile(configured, []byte("stub"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONNXRUNTIME_SHARED_LIBRARY_PATH", "/different/runtime.so")
	if got := resolveRuntimeLibrary(configured); got != configured {
		t.Fatalf("resolveRuntimeLibrary() = %q", got)
	}
}
