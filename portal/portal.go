// Package portal talks to the XDG desktop portal (org.freedesktop.portal.*)
// over the session D-Bus. On Wayland desktops such as GNOME, sandboxed
// protocols replace the X11 facilities this tool used to rely on: the
// RemoteDesktop portal injects the Ctrl+V keystroke and the GlobalShortcuts
// portal delivers the daemon's global hotkey.
package portal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	busName    = "org.freedesktop.portal.Desktop"
	objectPath = dbus.ObjectPath("/org/freedesktop/portal/desktop")

	requestInterface  = "org.freedesktop.portal.Request"
	sessionInterface  = "org.freedesktop.portal.Session"
	registryInterface = "org.freedesktop.host.portal.Registry"

	// AppID identifies this host (non-sandboxed) application to the portal. It
	// matches the installed desktop entry (<AppID>.desktop) so GNOME can show a
	// proper name in permission dialogs and remember granted permissions.
	AppID = "io.github.ericwyn.LinuxVoiceInput"
)

// ErrCancelled is returned when the user dismisses a portal dialog.
var ErrCancelled = errors.New("portal request cancelled by user")

// IsWayland reports whether the current desktop session is Wayland.
func IsWayland() bool {
	if t := os.Getenv("XDG_SESSION_TYPE"); t != "" {
		return t == "wayland"
	}
	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// Client is a private session-bus connection to the portal service. Each
// Client registers AppID once, which the portal requires to happen before any
// other portal call on the connection.
type Client struct {
	conn *dbus.Conn

	mu       sync.Mutex
	waiters  map[dbus.ObjectPath]chan *dbus.Signal
	handlers map[int]func(*dbus.Signal)
	nextID   int
	closed   bool
}

// Connect opens a new private session-bus connection to the portal.
func Connect() (*Client, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect session bus: %w", err)
	}
	c := &Client{
		conn:     conn,
		waiters:  make(map[dbus.ObjectPath]chan *dbus.Signal),
		handlers: make(map[int]func(*dbus.Signal)),
	}
	signals := make(chan *dbus.Signal, 64)
	conn.Signal(signals)
	go c.dispatch(signals)

	// Older portals (< 1.19) lack the Registry; they fall back to an empty app
	// id, which is fine for RemoteDesktop. Errors are therefore not fatal.
	_ = conn.Object(busName, objectPath).Call(registryInterface+".Register", 0,
		AppID, map[string]dbus.Variant{}).Err
	return c, nil
}

// Close closes the underlying connection; all sessions it owns end with it.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	_ = c.conn.Close()
}

func (c *Client) dispatch(signals <-chan *dbus.Signal) {
	for sig := range signals {
		c.mu.Lock()
		if sig.Name == requestInterface+".Response" {
			if ch, ok := c.waiters[sig.Path]; ok {
				delete(c.waiters, sig.Path)
				ch <- sig
			}
		}
		handlers := make([]func(*dbus.Signal), 0, len(c.handlers))
		for _, h := range c.handlers {
			handlers = append(handlers, h)
		}
		c.mu.Unlock()
		for _, h := range handlers {
			h(sig)
		}
	}
}

// onSignal registers a handler for every signal received on the connection.
// The returned function unregisters it.
func (c *Client) onSignal(match []dbus.MatchOption, h func(*dbus.Signal)) (func(), error) {
	if err := c.conn.AddMatchSignal(match...); err != nil {
		return nil, err
	}
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.handlers[id] = h
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.handlers, id)
		c.mu.Unlock()
		_ = c.conn.RemoveMatchSignal(match...)
	}, nil
}

func newToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "lvi_" + hex.EncodeToString(b)
}

func (c *Client) senderPart() string {
	names := c.conn.Names()
	if len(names) == 0 {
		return ""
	}
	return strings.ReplaceAll(strings.TrimPrefix(names[0], ":"), ".", "_")
}

// request calls a portal method that follows the Request/Response pattern.
// options is the method's trailing a{sv} argument; a handle_token is added to
// it so the response path is known before the call is made (avoiding a race
// with fast responses).
func (c *Client) request(method string, timeout time.Duration, options map[string]dbus.Variant, args ...interface{}) (map[string]dbus.Variant, error) {
	if options == nil {
		options = map[string]dbus.Variant{}
	}
	token := newToken()
	options["handle_token"] = dbus.MakeVariant(token)
	path := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/portal/desktop/request/%s/%s", c.senderPart(), token))

	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath(path),
		dbus.WithMatchInterface(requestInterface),
		dbus.WithMatchMember("Response"),
	}
	if err := c.conn.AddMatchSignal(match...); err != nil {
		return nil, fmt.Errorf("subscribe to portal response: %w", err)
	}
	defer c.conn.RemoveMatchSignal(match...)

	ch := make(chan *dbus.Signal, 1)
	c.mu.Lock()
	c.waiters[path] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiters, path)
		c.mu.Unlock()
	}()

	args = append(args, options)
	var handle dbus.ObjectPath
	if err := c.conn.Object(busName, objectPath).Call(method, 0, args...).Store(&handle); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if handle != path {
		// Very old portals ignore handle_token; follow the returned path.
		c.mu.Lock()
		delete(c.waiters, path)
		c.waiters[handle] = ch
		c.mu.Unlock()
		alt := []dbus.MatchOption{
			dbus.WithMatchObjectPath(handle),
			dbus.WithMatchInterface(requestInterface),
			dbus.WithMatchMember("Response"),
		}
		if err := c.conn.AddMatchSignal(alt...); err == nil {
			defer c.conn.RemoveMatchSignal(alt...)
		}
	}

	select {
	case sig := <-ch:
		if len(sig.Body) < 2 {
			return nil, fmt.Errorf("%s: malformed response", method)
		}
		code, _ := sig.Body[0].(uint32)
		results, _ := sig.Body[1].(map[string]dbus.Variant)
		switch code {
		case 0:
			return results, nil
		case 1:
			return nil, fmt.Errorf("%s: %w", method, ErrCancelled)
		default:
			return nil, fmt.Errorf("%s: portal returned error response %d", method, code)
		}
	case <-time.After(timeout):
		_ = c.conn.Object(busName, handle).Call(requestInterface+".Close", 0).Err
		return nil, fmt.Errorf("%s: timed out after %s", method, timeout)
	}
}

// createSession runs <iface>.CreateSession and returns the session handle.
func (c *Client) createSession(iface string, extra map[string]dbus.Variant) (dbus.ObjectPath, error) {
	opts := map[string]dbus.Variant{"session_handle_token": dbus.MakeVariant(newToken())}
	for k, v := range extra {
		opts[k] = v
	}
	results, err := c.request(iface+".CreateSession", 30*time.Second, opts)
	if err != nil {
		return "", err
	}
	switch h := results["session_handle"].Value().(type) {
	case string:
		return dbus.ObjectPath(h), nil
	case dbus.ObjectPath:
		return h, nil
	}
	return "", fmt.Errorf("%s.CreateSession: missing session_handle", iface)
}

func (c *Client) closeSession(session dbus.ObjectPath) {
	if session == "" {
		return
	}
	_ = c.conn.Object(busName, session).Call(sessionInterface+".Close", 0).Err
}

// HasInterface reports whether the portal service exposes the interface.
func HasInterface(iface string) bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var version uint32
	v, err := conn.Object(busName, objectPath).GetProperty(iface + ".version")
	if err != nil {
		return false
	}
	return v.Store(&version) == nil && version > 0
}
