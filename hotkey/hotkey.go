// Package hotkey provides the daemon's global record-toggle hotkey. On X11 it
// grabs the key on the root window; on Wayland it uses the XDG
// GlobalShortcuts portal, because Wayland compositors do not let clients
// observe keys typed into other windows.
package hotkey

import (
	"time"

	"linux-voice-input/portal"
)

// Handler is a global hotkey backend.
type Handler interface {
	// SetOnPress sets the callback run on each hotkey activation.
	SetOnPress(fn func())
	// Bind activates the given combo (e.g. "alt+shift+g"), replacing any
	// previous binding.
	Bind(combo string) error
	// Unbind stops delivering the hotkey; Bind can be called again later.
	Unbind()
	// Listen runs the event loop until Stop; it may return immediately for
	// backends that deliver events on their own goroutine.
	Listen()
	Stop()
	Close()
	// Backend names the implementation for logs and diagnostics.
	Backend() string
}

// New picks the backend for the current session.
func New() (Handler, error) {
	// Return explicit nils so a failed constructor never yields a non-nil
	// interface wrapping a nil pointer.
	if portal.IsWayland() {
		h, err := newPortal()
		if err != nil {
			return nil, err
		}
		return h, nil
	}
	h, err := newX11()
	if err != nil {
		return nil, err
	}
	return h, nil
}

// ReleaseWaiter is implemented by backends that know when the hotkey keys are
// released. Pasting while the user still holds the hotkey's modifiers would
// turn Ctrl+V into e.g. Ctrl+Alt+Shift+V, which applications ignore.
type ReleaseWaiter interface {
	WaitReleased(timeout time.Duration)
}
