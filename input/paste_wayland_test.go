package input

import "testing"

func TestWaylandSkipsWindowFocus(t *testing.T) {
	orig := waylandSession
	waylandSession = func() bool { return true }
	defer func() { waylandSession = orig }()

	if got := GetActiveWindow(); got != "" {
		t.Fatalf("GetActiveWindow() = %q on Wayland, want empty", got)
	}
	if err := FocusWindow("12345"); err != nil {
		t.Fatalf("FocusWindow on Wayland should be a no-op, got %v", err)
	}
}
