package hotkey

import (
	"errors"
	"log"
	"sync"
	"time"

	"linux-voice-input/portal"
)

const shortcutDescription = "开始/停止语音输入"

// portalHandler binds the hotkey through the GlobalShortcuts portal. GNOME
// shows a one-time dialog in which the user confirms (or changes) the key; the
// choice is remembered for this app id and later changes happen in GNOME
// Settings → Apps → <this app> → Global Shortcuts.
type portalHandler struct {
	mu      sync.Mutex
	onPress func()
	session *portal.GlobalShortcuts
	// generation invalidates in-flight asynchronous binds after Unbind/Bind.
	generation uint64
	// held is non-nil while the shortcut is pressed; it is closed on release.
	held     chan struct{}
	trigger  string
	presses  chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
}

func newPortal() (*portalHandler, error) {
	if !portal.HasInterface("org.freedesktop.portal.GlobalShortcuts") {
		return nil, errors.New("desktop portal lacks GlobalShortcuts; use a GNOME custom shortcut running --record instead")
	}
	h := &portalHandler{presses: make(chan struct{}, 4), stop: make(chan struct{})}
	// Presses run on one worker goroutine: the callback may block for the
	// whole transcription, and the portal's signal loop must stay free to
	// deliver the matching release.
	go h.pressWorker()
	return h, nil
}

func (h *portalHandler) pressWorker() {
	for {
		select {
		case <-h.presses:
			h.mu.Lock()
			fn := h.onPress
			h.mu.Unlock()
			if fn != nil {
				fn()
			}
		case <-h.stop:
			return
		}
	}
}

func (h *portalHandler) SetOnPress(fn func()) {
	h.mu.Lock()
	h.onPress = fn
	h.mu.Unlock()
}

func (h *portalHandler) Backend() string { return "GlobalShortcuts portal" }

// Trigger returns the key combination GNOME actually assigned, if known.
func (h *portalHandler) Trigger() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.trigger
}

// WaitReleased blocks until the hotkey is no longer held, or timeout elapses.
func (h *portalHandler) WaitReleased(timeout time.Duration) {
	h.mu.Lock()
	held := h.held
	h.mu.Unlock()
	if held == nil {
		return
	}
	select {
	case <-held:
	case <-time.After(timeout):
		log.Printf("Hotkey release not reported within %s; pasting anyway", timeout)
	}
}

func (h *portalHandler) onEvent(id string, active bool) {
	if id != portal.ToggleShortcutID {
		return
	}
	h.mu.Lock()
	if active {
		if h.held == nil {
			h.held = make(chan struct{})
		}
	} else if h.held != nil {
		close(h.held)
		h.held = nil
	}
	h.mu.Unlock()
	if active {
		select {
		case h.presses <- struct{}{}:
		default: // drop presses while the worker is saturated
		}
	}
}

// Bind starts the portal binding in the background: the confirmation dialog
// can stay open for a long time and callers hold locks while binding. Errors
// are logged rather than returned.
func (h *portalHandler) Bind(combo string) error {
	h.mu.Lock()
	h.closeSessionLocked()
	h.generation++
	gen := h.generation
	h.mu.Unlock()

	go h.bind(gen, combo)
	return nil
}

func (h *portalHandler) bind(gen uint64, combo string) {
	session, err := portal.OpenGlobalShortcuts(h.onEvent)
	if err != nil {
		log.Printf("Global hotkey via portal unavailable: %v (the portal needs %s.desktop installed; run make install)", err, portal.AppID)
		return
	}
	trigger, err := session.Bind(portal.ToggleShortcutID, shortcutDescription, combo)
	if err != nil {
		session.Close()
		log.Printf("Bind hotkey %q via portal failed: %v", combo, err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if gen != h.generation {
		// Unbind or a newer Bind happened while the dialog was open.
		session.Close()
		return
	}
	h.session = session
	h.trigger = trigger
	log.Printf("Hotkey bound via GlobalShortcuts portal: GNOME assigned %q (configured hotkey %q is only used on first registration; change it in GNOME Settings → Apps → Linux Voice Input → Global Shortcuts)", trigger, combo)
}

func (h *portalHandler) closeSessionLocked() {
	if h.session != nil {
		h.session.Close()
		h.session = nil
	}
	if h.held != nil {
		close(h.held)
		h.held = nil
	}
}

func (h *portalHandler) Unbind() {
	h.mu.Lock()
	h.generation++
	h.closeSessionLocked()
	h.mu.Unlock()
}

// Listen blocks until Stop; activation signals arrive on the portal client's
// own goroutine.
func (h *portalHandler) Listen() { <-h.stop }

func (h *portalHandler) Stop() { h.stopOnce.Do(func() { close(h.stop) }) }

func (h *portalHandler) Close() { h.Unbind() }
