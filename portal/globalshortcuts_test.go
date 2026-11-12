package portal

import "testing"

func TestToPortalTrigger(t *testing.T) {
	cases := map[string]string{
		"alt+shift+g":   "ALT+SHIFT+g",
		"ctrl+alt+v":    "CTRL+ALT+v",
		"Super+Space":   "LOGO+Space",
		" control + F9": "CTRL+F9",
		"":              "",
	}
	for in, want := range cases {
		if got := ToPortalTrigger(in); got != want {
			t.Errorf("ToPortalTrigger(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsWayland(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "wayland")
	if !IsWayland() {
		t.Fatal("wayland session not detected")
	}
	t.Setenv("XDG_SESSION_TYPE", "x11")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if IsWayland() {
		t.Fatal("explicit x11 session type must win")
	}
	t.Setenv("XDG_SESSION_TYPE", "")
	if !IsWayland() {
		t.Fatal("WAYLAND_DISPLAY fallback not used")
	}
}
