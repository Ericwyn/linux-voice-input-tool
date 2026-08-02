package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSharedLibrary(t *testing.T) {
	dirWithLib := t.TempDir()
	emptyDir := t.TempDir()

	const libName = "libsherpa-onnx-c-api.so"
	if err := os.WriteFile(filepath.Join(dirWithLib, libName), []byte("stub"), 0644); err != nil {
		t.Fatalf("write stub lib: %v", err)
	}

	// A directory entry with the library's name must not be mistaken for the
	// library file itself; only a regular file counts as resolved.
	if err := os.Mkdir(filepath.Join(emptyDir, libName), 0755); err != nil {
		t.Fatalf("make decoy dir: %v", err)
	}

	t.Run("found in one of the search dirs returns that dir", func(t *testing.T) {
		got := resolveSharedLibrary(libName, []string{emptyDir, dirWithLib})
		if got != dirWithLib {
			t.Fatalf("resolveSharedLibrary = %q, want %q", got, dirWithLib)
		}
	})

	t.Run("earlier dir wins", func(t *testing.T) {
		otherDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(otherDir, libName), []byte("stub"), 0644); err != nil {
			t.Fatalf("write stub lib: %v", err)
		}
		got := resolveSharedLibrary(libName, []string{otherDir, dirWithLib})
		if got != otherDir {
			t.Fatalf("resolveSharedLibrary = %q, want first match %q", got, otherDir)
		}
	})

	t.Run("missing everywhere returns empty", func(t *testing.T) {
		got := resolveSharedLibrary(libName, []string{emptyDir})
		if got != "" {
			t.Fatalf("resolveSharedLibrary = %q, want empty for a same-named directory", got)
		}
		got = resolveSharedLibrary("libnope.so", []string{dirWithLib, emptyDir})
		if got != "" {
			t.Fatalf("resolveSharedLibrary = %q, want empty when nothing matches", got)
		}
	})
}

func TestAppendUnique(t *testing.T) {
	var got []string
	got = appendUnique(got, "a")
	got = appendUnique(got, "b")
	got = appendUnique(got, "a")
	got = appendUnique(got, "b")

	want := []string{"a", "b"}
	if len(got) != len(want) {
		t.Fatalf("appendUnique produced %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("appendUnique produced %v, want %v", got, want)
		}
	}
}

// indexOfDir returns the position of target in dirs, or -1 if absent. It lets
// tests assert both presence and relative ordering of search directories.
func indexOfDir(dirs []string, target string) int {
	for i, dir := range dirs {
		if dir == target {
			return i
		}
	}
	return -1
}

func TestComposeSearchDirsExpandsOriginAndHonorsEnv(t *testing.T) {
	const exeDir = "/opt/app"
	runpaths := []string{"${ORIGIN}/vendor-libs:$ORIGIN/more-libs"}
	ldLibraryPath := "/custom/ld/dir"

	dirs := composeSearchDirs(runpaths, nil, exeDir, ldLibraryPath)

	for _, want := range []string{
		filepath.Join(exeDir, "vendor-libs"),
		filepath.Join(exeDir, "more-libs"),
		ldLibraryPath,
		"/usr/lib",
	} {
		if indexOfDir(dirs, want) < 0 {
			t.Errorf("search dirs %v missing expected entry %q", dirs, want)
		}
	}

	// LD_LIBRARY_PATH must be searched before DT_RUNPATH, matching the loader.
	ldIdx := indexOfDir(dirs, ldLibraryPath)
	runpathIdx := indexOfDir(dirs, filepath.Join(exeDir, "vendor-libs"))
	if ldIdx >= runpathIdx {
		t.Errorf("LD_LIBRARY_PATH (index %d) should come before DT_RUNPATH (index %d): %v",
			ldIdx, runpathIdx, dirs)
	}
}

func TestComposeSearchDirsRpathIgnoredWhenRunpathPresent(t *testing.T) {
	const exeDir = "/opt/app"
	rpathOnlyDir := "/rpath/only"

	// With a DT_RUNPATH present, DT_RPATH must be ignored (loader semantics).
	withRunpath := composeSearchDirs([]string{"/runpath/dir"}, []string{rpathOnlyDir}, exeDir, "")
	if indexOfDir(withRunpath, rpathOnlyDir) >= 0 {
		t.Errorf("DT_RPATH %q should be ignored when DT_RUNPATH is present: %v", rpathOnlyDir, withRunpath)
	}
	if indexOfDir(withRunpath, "/runpath/dir") < 0 {
		t.Errorf("DT_RUNPATH entry missing: %v", withRunpath)
	}

	// Without any DT_RUNPATH, DT_RPATH is honored.
	withoutRunpath := composeSearchDirs(nil, []string{rpathOnlyDir}, exeDir, "")
	if indexOfDir(withoutRunpath, rpathOnlyDir) < 0 {
		t.Errorf("DT_RPATH %q should be honored when no DT_RUNPATH is present: %v", rpathOnlyDir, withoutRunpath)
	}
}
