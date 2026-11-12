package portal

import (
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const globalShortcutsInterface = "org.freedesktop.portal.GlobalShortcuts"

// ToggleShortcutID is the portal shortcut id used for the record toggle.
const ToggleShortcutID = "toggle-recording"

// GlobalShortcuts is a GlobalShortcuts portal session. GNOME asks the user to
// confirm (and optionally change) the key combination the first time; the
// binding is remembered per application afterwards.
type GlobalShortcuts struct {
	client      *Client
	session     dbus.ObjectPath
	unsubscribe func()
}

// OpenGlobalShortcuts creates a session and subscribes to activation signals.
// onEvent receives the shortcut id with active=true when the shortcut is
// pressed and active=false when it is released (Deactivated).
func OpenGlobalShortcuts(onEvent func(id string, active bool)) (*GlobalShortcuts, error) {
	client, err := Connect()
	if err != nil {
		return nil, err
	}
	session, err := client.createSession(globalShortcutsInterface, nil)
	if err != nil {
		client.Close()
		return nil, err
	}
	g := &GlobalShortcuts{client: client, session: session}
	unsub, err := client.onSignal([]dbus.MatchOption{
		dbus.WithMatchInterface(globalShortcutsInterface),
	}, func(sig *dbus.Signal) {
		var active bool
		switch sig.Name {
		case globalShortcutsInterface + ".Activated":
			active = true
		case globalShortcutsInterface + ".Deactivated":
			active = false
		default:
			return
		}
		if len(sig.Body) < 2 {
			return
		}
		if path, _ := sig.Body[0].(dbus.ObjectPath); path != session {
			return
		}
		if id, ok := sig.Body[1].(string); ok && onEvent != nil {
			onEvent(id, active)
		}
	})
	if err != nil {
		g.Close()
		return nil, fmt.Errorf("subscribe to shortcut activation: %w", err)
	}
	g.unsubscribe = unsub
	return g, nil
}

// shortcut is the D-Bus (sa{sv}) struct used by BindShortcuts.
type shortcut struct {
	ID      string
	Options map[string]dbus.Variant
}

// Bind registers one shortcut. preferred is a user-style combo such as
// "alt+shift+g". It is only a hint: GNOME uses it the first time and then
// keeps the user's stored choice (GNOME does not implement
// ConfigureShortcuts, so changes are made in Settings → Apps). It returns the
// trigger description the desktop actually assigned, if reported.
func (g *GlobalShortcuts) Bind(id, description, preferred string) (string, error) {
	opts := map[string]dbus.Variant{"description": dbus.MakeVariant(description)}
	if trigger := ToPortalTrigger(preferred); trigger != "" {
		opts["preferred_trigger"] = dbus.MakeVariant(trigger)
	}
	shortcuts := []shortcut{{ID: id, Options: opts}}
	// The dialog stays open until the user answers it.
	results, err := g.client.request(globalShortcutsInterface+".BindShortcuts", 5*time.Minute, nil,
		g.session, shortcuts, "")
	if err != nil {
		return "", err
	}
	return assignedTrigger(results, id), nil
}

func assignedTrigger(results map[string]dbus.Variant, id string) string {
	v, ok := results["shortcuts"]
	if !ok {
		return ""
	}
	var list []shortcut
	if err := dbus.Store([]interface{}{v.Value()}, &list); err != nil {
		return ""
	}
	for _, s := range list {
		if s.ID != id {
			continue
		}
		if d, ok := s.Options["trigger_description"].Value().(string); ok {
			return d
		}
	}
	return ""
}

// Close ends the session; the desktop stops delivering the shortcut.
func (g *GlobalShortcuts) Close() {
	if g == nil || g.client == nil {
		return
	}
	if g.unsubscribe != nil {
		g.unsubscribe()
	}
	g.client.closeSession(g.session)
	g.client.Close()
}

// ToPortalTrigger converts "ctrl+alt+v" into the XDG shortcuts specification
// format "CTRL+ALT+v".
func ToPortalTrigger(combo string) string {
	parts := strings.Split(combo, "+")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		switch strings.ToLower(p) {
		case "ctrl", "control":
			out = append(out, "CTRL")
		case "alt":
			out = append(out, "ALT")
		case "shift":
			out = append(out, "SHIFT")
		case "super", "win", "meta", "logo":
			out = append(out, "LOGO")
		default:
			if len(p) == 1 {
				p = strings.ToLower(p)
			}
			out = append(out, p)
		}
	}
	return strings.Join(out, "+")
}
