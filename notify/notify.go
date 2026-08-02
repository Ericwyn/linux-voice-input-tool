// Package notify shows desktop notifications through the freedesktop
// org.freedesktop.Notifications D-Bus interface, spoken directly over the
// session bus via godbus. It needs no external gdbus / notify-send process and
// works with any spec-compliant notification server (GNOME, KDE, XFCE, dunst,
// …), rather than depending on a particular desktop's helper binaries.
//
// A single notification id is reused throughout one recording session: the
// live transcription preview keeps updating the same on-screen bubble, and the
// final result updates that very same bubble in place rather than popping a
// second notification. The preview is shown as a resident (non-expiring)
// notification so it stays visible while recording; the final result replaces
// it in place with a transient, self-expiring notification, so the one bubble
// simply evolves from "录音中: …" to "已输入：…" and then fades on its own,
// even when the one-shot process exits right after pasting.
package notify

import (
	"log"
	"os/exec"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	dbusService   = "org.freedesktop.Notifications"
	dbusPath      = "/org/freedesktop/Notifications"
	dbusInterface = "org.freedesktop.Notifications"

	appName = "Linux Voice Input"

	// resultTimeoutMs is the expiry hint, in milliseconds, for transient result
	// notifications. The server may apply its own display policy, but transient
	// notifications are not kept in the notification history regardless.
	resultTimeoutMs int32 = 3000
)

var (
	// mu serializes all notification state and D-Bus calls. Every exported
	// function acquires it, so the unexported *Locked helpers assume it is held.
	mu sync.Mutex

	// sessionBus is the shared session-bus connection, opened lazily on first
	// use and reused thereafter. It is intentionally never closed: it is a
	// process-wide shared connection (the same one fyne uses in daemon mode).
	sessionBus *dbus.Conn

	// currentID is the id of the notification for the current session, so both
	// preview updates and the final result can replace it in place. 0 means
	// none is active.
	currentID uint32
	// previewActive is true only while the resident (never-expiring) live
	// preview bubble is the current notification. Any Show/clear resets it, so
	// it precisely marks the window in which an un-torn-down preview could
	// otherwise linger on screen. EndPreview keys off it as a safety net.
	previewActive bool
)

// bus returns the shared session-bus connection, opening it on first use.
// Callers must hold mu.
func bus() (*dbus.Conn, error) {
	if sessionBus != nil {
		return sessionBus, nil
	}
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, err
	}
	sessionBus = conn
	return sessionBus, nil
}

// notifyLocked sends a Notify call and returns the id the server assigned.
// Passing replacesID != 0 updates that existing notification in place.
func notifyLocked(replacesID uint32, summary, body string, hints map[string]dbus.Variant, timeoutMs int32) (uint32, error) {
	conn, err := bus()
	if err != nil {
		return 0, err
	}
	obj := conn.Object(dbusService, dbus.ObjectPath(dbusPath))
	call := obj.Call(dbusInterface+".Notify", 0,
		appName,     // app_name
		replacesID,  // replaces_id
		"",          // app_icon
		summary,     // summary
		body,        // body
		[]string{},  // actions
		hints,       // hints
		timeoutMs,   // expire_timeout
	)
	if call.Err != nil {
		return 0, call.Err
	}
	var id uint32
	if err := call.Store(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// closeLocked removes the notification with the given id. A zero id is a no-op.
func closeLocked(id uint32) {
	if id == 0 {
		return
	}
	conn, err := bus()
	if err != nil {
		return
	}
	obj := conn.Object(dbusService, dbus.ObjectPath(dbusPath))
	if call := obj.Call(dbusInterface+".CloseNotification", 0, id); call.Err != nil {
		log.Printf("close notification failed: %v", call.Err)
	}
}

// clearCurrentLocked tears down whatever notification is currently active and
// resets the tracked state.
func clearCurrentLocked() {
	closeLocked(currentID)
	currentID = 0
	previewActive = false
}

// Persistent shows (or updates in place) the resident live-preview
// notification used for the "recording…" status. It never auto-expires, so it
// stays visible for the whole recording; each call replaces the previous text.
func Persistent(summary, body string) {
	mu.Lock()
	defer mu.Unlock()

	id, err := notifyLocked(currentID, summary, body, map[string]dbus.Variant{
		"urgency":  dbus.MakeVariant(byte(2)),
		"resident": dbus.MakeVariant(true),
	}, 0)
	if err != nil {
		log.Printf("dbus persistent notify failed: %v", err)
		fallbackNotifySend(summary, body)
		return
	}
	currentID = id
	previewActive = true
}

// Show displays (or updates in place) the session notification with a final
// result. It replaces the current bubble — including a resident live-preview
// one — in place, so the same on-screen notification simply evolves from
// "录音中: …" into the result rather than popping a second one. The new hints
// drop resident and set transient with a timeout, so the server expires the
// bubble on its own and keeps it out of history. The final flag is retained
// for call-site compatibility; every Show is transient and self-expiring.
func Show(summary, body string, final bool) {
	mu.Lock()
	defer mu.Unlock()

	id, err := notifyLocked(currentID, summary, body, map[string]dbus.Variant{
		"transient": dbus.MakeVariant(true),
	}, resultTimeoutMs)
	if err != nil {
		log.Printf("dbus notify failed: %v", err)
		fallbackNotifySend(summary, body)
		return
	}
	currentID = id
	// A transient result is self-expiring, so the never-expiring preview is no
	// longer on screen: clear the flag so EndPreview leaves this result alone.
	previewActive = false
}

// EndPreview closes the resident live-preview bubble if one is still active,
// and does nothing otherwise. It is the teardown counterpart to Persistent:
// callers defer it around a recording so the never-expiring preview can never
// linger on screen, whichever path ends the recording — success, error, or
// even a panic. Because it only acts while previewActive is set, it never
// touches a transient result/error bubble that a Show already put up. It is
// idempotent and safe to call multiple times.
func EndPreview() {
	mu.Lock()
	defer mu.Unlock()
	if previewActive {
		clearCurrentLocked()
	}
}

// If shows a notification only when enabled is true.
func If(enabled bool, summary, body string, final bool) {
	if enabled {
		Show(summary, body, final)
	}
}

// FinalIf shows a final notification when enabled. When disabled it instead
// tears down any active notification so the resident "recording…" preview does
// not stay pinned on screen after the recording ends.
func FinalIf(enabled bool, summary, body string) {
	if enabled {
		Show(summary, body, true)
		return
	}
	mu.Lock()
	clearCurrentLocked()
	mu.Unlock()
}

// FinalSuccess reports a successful result. When a live preview bubble is still
// on screen it already showed the recognized text — which has now been pasted
// into the input box — so the preview is torn down silently instead of popping
// a redundant "已输入" bubble. When no preview was shown (e.g. the whisper
// engine, or the preview notification disabled), it falls back to FinalIf so an
// enabled success notification still confirms the result. Either way no
// resident bubble is left behind.
func FinalSuccess(enabled bool, summary, body string) {
	mu.Lock()
	hadPreview := previewActive
	mu.Unlock()

	if hadPreview {
		EndPreview()
		return
	}
	FinalIf(enabled, summary, body)
}

// fallbackNotifySend is a best-effort fallback used only when the session bus
// is unavailable. It cannot replace or close notifications, so it simply fires
// a transient one; if notify-send is not installed the failure is ignored.
func fallbackNotifySend(summary, body string) {
	cmd := exec.Command("notify-send", "--app-name="+appName,
		"--expire-time=3000", "--hint=boolean:transient:true", summary, body)
	if err := cmd.Run(); err != nil {
		log.Printf("notify-send fallback failed: %v", err)
	}
}
