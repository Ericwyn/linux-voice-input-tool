package hotkey

import (
	"fmt"
	"log"
	"strings"

	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil"
	"github.com/BurntSushi/xgbutil/keybind"
	"github.com/BurntSushi/xgbutil/xevent"
)

// convertHotkey converts user-friendly format "ctrl+alt+v" to X11 format "control-mod1-v"
func convertHotkey(key string) string {
	parts := strings.Split(strings.ToLower(key), "+")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case "ctrl", "control":
			parts[i] = "control"
		case "alt":
			parts[i] = "mod1"
		case "super", "win", "meta":
			parts[i] = "mod4"
		case "shift":
			parts[i] = "shift"
		default:
			parts[i] = p
		}
	}
	return strings.Join(parts, "-")
}

type Handler struct {
	xu        *xgbutil.XUtil
	OnPress   func()
	OnRelease func()
}

func New() (*Handler, error) {
	xu, err := xgbutil.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connect to X server: %w", err)
	}
	keybind.Initialize(xu)
	return &Handler{xu: xu}, nil
}

func (h *Handler) Bind(hotkey string) error {
	root := h.xu.RootWin()
	xkey := convertHotkey(hotkey)

	err := keybind.KeyPressFun(func(xu *xgbutil.XUtil, e xevent.KeyPressEvent) {
		if h.OnPress != nil {
			h.OnPress()
		}
	}).Connect(h.xu, root, xkey, true)
	if err != nil {
		return fmt.Errorf("bind key press %s (X11: %s): %w", hotkey, xkey, err)
	}

	err = keybind.KeyReleaseFun(func(xu *xgbutil.XUtil, e xevent.KeyReleaseEvent) {
		if h.OnRelease != nil {
			h.OnRelease()
		}
	}).Connect(h.xu, root, xkey, true)
	if err != nil {
		return fmt.Errorf("bind key release %s (X11: %s): %w", hotkey, xkey, err)
	}

	log.Printf("Hotkey bound: %s (X11: %s)", hotkey, xkey)
	return nil
}

// GrabKeyboard grabs keyboard to detect release of modifier keys
func (h *Handler) GrabKeyboard() {
	xproto.GrabKeyboard(h.xu.Conn(), true, h.xu.RootWin(),
		xproto.TimeCurrentTime, xproto.GrabModeAsync, xproto.GrabModeAsync)
}

func (h *Handler) UngrabKeyboard() {
	xproto.UngrabKeyboard(h.xu.Conn(), xproto.TimeCurrentTime)
}

func (h *Handler) Listen() {
	xevent.Main(h.xu)
}

func (h *Handler) Stop() {
	xevent.Quit(h.xu)
}
