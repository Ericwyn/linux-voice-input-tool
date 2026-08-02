// Package notify wraps desktop notifications via the freedesktop DBus
// Notifications interface (using gdbus), with a fallback to notify-send.
//
// It reuses a single notification id stored under XDG_RUNTIME_DIR so that
// successive updates (e.g. live transcription preview) replace the same
// notification instead of stacking new ones.
package notify

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const notifyIDFileName = "linux-voice-input.notify-id"

func runtimeDir() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return dir
}

func notifyIDPath() string {
	return filepath.Join(runtimeDir(), notifyIDFileName)
}

func readNotifyID() string {
	data, err := os.ReadFile(notifyIDPath())
	if err != nil {
		return "0"
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "0"
	}
	return id
}

func writeNotifyID(id string) {
	if id == "" || id == "0" {
		return
	}
	if err := os.WriteFile(notifyIDPath(), []byte(id), 0644); err != nil {
		log.Printf("write notification id failed: %v", err)
	}
}

// Show displays (or updates) a transient notification. When final is true the
// notification is scheduled to close after a few seconds.
func Show(summary, body string, final bool) {
	id, err := notifyDBusWithTimeout(summary, body, readNotifyID(), "3000")
	if err != nil {
		log.Printf("dbus notify failed: %v", err)
		cmd := exec.Command("notify-send", "--app-name=Linux Voice Input", "--expire-time=3000", "--hint=boolean:transient:true", summary, body)
		if err := cmd.Run(); err != nil {
			log.Printf("notify-send failed: %v", err)
		}
		return
	}

	writeNotifyID(id)
	if final {
		closeNotificationLater(id)
	}
}

// If shows a notification only when enabled is true.
func If(enabled bool, summary, body string, final bool) {
	if enabled {
		Show(summary, body, final)
	}
}

// FinalIf shows a final notification when enabled, otherwise it just schedules
// the currently displayed notification (if any) to close.
func FinalIf(enabled bool, summary, body string) {
	if enabled {
		Show(summary, body, true)
		return
	}
	closeCurrentNotificationLater()
}

// Persistent shows (or updates) a resident notification that does not
// auto-expire, used for the live "recording" status.
func Persistent(summary, body string) {
	id, err := notifyDBusPersistent(summary, body, readNotifyID())
	if err != nil {
		log.Printf("dbus persistent notify failed: %v", err)
		return
	}
	writeNotifyID(id)
}

func closeCurrentNotificationLater() {
	id := readNotifyID()
	if id != "0" {
		closeNotificationLater(id)
	}
}

func closeNotificationLater(id string) {
	cmd := exec.Command(
		"sh", "-c",
		"sleep 3; gdbus call --session --dest org.freedesktop.Notifications --object-path /org/freedesktop/Notifications --method org.freedesktop.Notifications.CloseNotification \"$1\" >/dev/null 2>&1; rm -f \"$2\"",
		"sh", id, notifyIDPath(),
	)
	if err := cmd.Start(); err != nil {
		log.Printf("schedule close notification failed: %v", err)
	}
}

func notifyDBusPersistent(summary, body, replacesID string) (string, error) {
	out, err := exec.Command(
		"gdbus", "call",
		"--session",
		"--dest", "org.freedesktop.Notifications",
		"--object-path", "/org/freedesktop/Notifications",
		"--method", "org.freedesktop.Notifications.Notify",
		"Linux Voice Input",
		replacesID,
		"",
		summary,
		body,
		"[]",
		"{'urgency': <byte 2>, 'resident': <true>}",
		"0",
	).Output()
	if err != nil {
		return "", err
	}
	return parseNotificationID(string(out))
}

func notifyDBusWithTimeout(summary, body, replacesID, timeoutMs string) (string, error) {
	out, err := exec.Command(
		"gdbus", "call",
		"--session",
		"--dest", "org.freedesktop.Notifications",
		"--object-path", "/org/freedesktop/Notifications",
		"--method", "org.freedesktop.Notifications.Notify",
		"Linux Voice Input",
		replacesID,
		"",
		summary,
		body,
		"[]",
		"{'transient': <true>}",
		timeoutMs,
	).Output()
	if err != nil {
		return "", err
	}
	return parseNotificationID(string(out))
}

func parseNotificationID(raw string) (string, error) {
	result := strings.TrimSpace(raw)
	id := strings.Trim(result, "(), ")
	id = strings.TrimPrefix(id, "uint32 ")
	if id == "" {
		return "", fmt.Errorf("empty notification id from %q", result)
	}
	return id, nil
}
