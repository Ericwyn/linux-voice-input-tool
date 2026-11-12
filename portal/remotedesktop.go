package portal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const remoteDesktopInterface = "org.freedesktop.portal.RemoteDesktop"

// Keysyms used for the paste shortcut.
const (
	KeysymControlL = 0xffe3
	KeysymV        = 0x0076
)

const (
	deviceKeyboard      = 1
	persistUntilRevoked = 2
)

// RemoteDesktop is an active RemoteDesktop portal session that can inject
// keyboard events into the focused window.
type RemoteDesktop struct {
	client  *Client
	session dbus.ObjectPath
}

// OpenRemoteDesktop starts a keyboard-only RemoteDesktop session. The first
// time GNOME shows a permission dialog; the returned restore token is stored in
// tokenPath so later sessions start without asking again.
func OpenRemoteDesktop(tokenPath string) (*RemoteDesktop, error) {
	token := readToken(tokenPath)
	rd, newToken, err := openRemoteDesktop(token)
	if err != nil && token != "" && !errors.Is(err, ErrCancelled) {
		// A stale or revoked token can make Start fail; retry with a fresh
		// permission prompt.
		rd, newToken, err = openRemoteDesktop("")
	}
	if err != nil {
		return nil, err
	}
	if newToken != "" && newToken != token {
		writeToken(tokenPath, newToken)
	}
	return rd, nil
}

func openRemoteDesktop(restoreToken string) (*RemoteDesktop, string, error) {
	client, err := Connect()
	if err != nil {
		return nil, "", err
	}
	session, err := client.createSession(remoteDesktopInterface, nil)
	if err != nil {
		client.Close()
		return nil, "", err
	}
	rd := &RemoteDesktop{client: client, session: session}

	opts := map[string]dbus.Variant{
		"types":        dbus.MakeVariant(uint32(deviceKeyboard)),
		"persist_mode": dbus.MakeVariant(uint32(persistUntilRevoked)),
	}
	if restoreToken != "" {
		opts["restore_token"] = dbus.MakeVariant(restoreToken)
	}
	if _, err := client.request(remoteDesktopInterface+".SelectDevices", 30*time.Second, opts, session); err != nil {
		rd.Close()
		return nil, "", err
	}

	// Start may show a dialog; give the user time to answer it.
	results, err := client.request(remoteDesktopInterface+".Start", 2*time.Minute, nil, session, "")
	if err != nil {
		rd.Close()
		return nil, "", err
	}
	if devices, ok := results["devices"].Value().(uint32); ok && devices&deviceKeyboard == 0 {
		rd.Close()
		return nil, "", errors.New("RemoteDesktop session was granted without keyboard access")
	}
	newToken, _ := results["restore_token"].Value().(string)
	return rd, newToken, nil
}

// Key presses or releases a key identified by its X keysym.
func (r *RemoteDesktop) Key(keysym int32, pressed bool) error {
	state := uint32(0)
	if pressed {
		state = 1
	}
	return r.client.conn.Object(busName, objectPath).Call(
		remoteDesktopInterface+".NotifyKeyboardKeysym", 0,
		r.session, map[string]dbus.Variant{}, keysym, state).Err
}

// Paste sends Ctrl+V to the focused window.
func (r *RemoteDesktop) Paste() error {
	steps := []struct {
		keysym  int32
		pressed bool
	}{
		{KeysymControlL, true},
		{KeysymV, true},
		{KeysymV, false},
		{KeysymControlL, false},
	}
	var firstErr error
	for i, s := range steps {
		if err := r.Key(s.keysym, s.pressed); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// Keep going on release steps so no key stays stuck.
			if s.pressed {
				_ = r.Key(KeysymControlL, false)
				break
			}
		}
		if i < len(steps)-1 {
			time.Sleep(15 * time.Millisecond)
		}
	}
	if firstErr != nil {
		return fmt.Errorf("send Ctrl+V via RemoteDesktop portal: %w", firstErr)
	}
	return nil
}

// Close ends the session and its D-Bus connection.
func (r *RemoteDesktop) Close() {
	if r == nil || r.client == nil {
		return
	}
	r.client.closeSession(r.session)
	r.client.Close()
}

func readToken(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeToken(path, token string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
