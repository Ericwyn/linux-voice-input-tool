package input

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"linux-voice-input/portal"
)

// waylandSession reports whether paste should go through the Wayland portal
// path. It is a variable so tests can force either backend.
var waylandSession = portal.IsWayland

// remoteDesktopTokenPath stores the RemoteDesktop restore token so the
// permission dialog only appears on first use.
func remoteDesktopTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "linux-voice-input", "remote-desktop.token")
	}
	return filepath.Join(home, ".linux-voice-input", "remote-desktop.token")
}

// waylandModifierSettle is the extra delay after the hotkey is released. The
// portal only reports the trigger key's release, and users often let go of
// the modifiers a moment later; any modifier still held turns the injected
// Ctrl+V into e.g. Ctrl+Alt+Shift+V, which applications ignore.
const waylandModifierSettle = 250 * time.Millisecond

// beforePortalPaste is set by the daemon to wait for the global hotkey to be
// released before injecting Ctrl+V.
var beforePortalPaste func()

// SetBeforePortalPaste installs a hook run right before the Wayland portal
// paste. It is not used on X11, where xdotool clears held modifiers itself.
func SetBeforePortalPaste(fn func()) { beforePortalPaste = fn }

// simulatePastePortal sends Ctrl+V through the RemoteDesktop portal. The
// session is opened per paste and closed right after so GNOME's "remote
// control" indicator only shows briefly.
func simulatePastePortal() error {
	if beforePortalPaste != nil {
		beforePortalPaste()
	}
	time.Sleep(waylandModifierSettle)

	start := time.Now()
	rd, err := portal.OpenRemoteDesktop(remoteDesktopTokenPath())
	if err != nil {
		return err
	}
	defer rd.Close()
	log.Printf("RemoteDesktop portal session ready in %s", time.Since(start).Round(time.Millisecond))
	return rd.Paste()
}
