// Package ui hosts the fyne desktop application for daemon mode: a system tray,
// a settings window, and the global hotkey lifecycle. The recording pipeline
// itself lives in the session package; this package only orchestrates it.
package ui

import (
	"log"
	"sync"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"

	"linux-voice-input/assets"
	"linux-voice-input/config"
	"linux-voice-input/hotkey"
	"linux-voice-input/session"
)

// App wires together the fyne application, system tray, settings window,
// global hotkey handler, and the shared session controller.
type App struct {
	fyneApp    fyne.App
	controller *session.Controller

	mu            sync.Mutex
	cfg           *config.Config
	hotkeyHandler *hotkey.Handler
	hotkeyBound   bool

	settingsWindow fyne.Window

	// updateTrayStatus is set by setupTray to reflect recording state; it is
	// always invoked on the fyne main goroutine.
	updateTrayStatus func(recording bool)
}

// NewApp constructs the daemon UI. It initializes the global hotkey handler,
// builds the tray and settings window, and binds the hotkey if enabled.
func NewApp(cfg *config.Config, controller *session.Controller) *App {
	fyneApplication := fyneapp.NewWithID("com.github.linux-voice-input")
	fyneApplication.SetIcon(assets.AppIcon)
	// Bolder text renders crisper than the thin default font on many Linux
	// displays; this applies app-wide (settings window, download window, ...).
	fyneApplication.Settings().SetTheme(newBoldTheme())

	app := &App{
		fyneApp:    fyneApplication,
		controller: controller,
		cfg:        cfg,
	}

	app.initHotkey()
	app.buildSettingsWindow()
	app.setupTray()

	controller.SetOnStateChange(func(recording bool) {
		fyne.Do(func() {
			if app.updateTrayStatus != nil {
				app.updateTrayStatus(recording)
			}
		})
	})

	return app
}

// initHotkey creates the X11 hotkey handler, wires OnPress to the controller's
// Toggle, starts the event loop goroutine, and binds the hotkey if enabled.
func (a *App) initHotkey() {
	handler, err := hotkey.New()
	if err != nil {
		log.Printf("Global hotkey unavailable (X11 required): %v", err)
		return
	}

	handler.OnPress = a.controller.Toggle
	a.hotkeyHandler = handler

	if a.cfg.HotkeyEnabled {
		a.bindHotkeyLocked(a.cfg.Hotkey)
	}

	// The xgbutil event loop runs on its own X connection, independent of
	// fyne's GL loop, so it is safe to run in a separate goroutine.
	go handler.Listen()
}

// bindHotkeyLocked binds the given hotkey and records the bound state. It does
// not take the mutex; callers that mutate shared state should hold a.mu.
func (a *App) bindHotkeyLocked(combo string) {
	if a.hotkeyHandler == nil {
		return
	}
	if a.hotkeyBound {
		a.hotkeyHandler.Unbind()
		a.hotkeyBound = false
	}
	if err := a.hotkeyHandler.Bind(combo); err != nil {
		log.Printf("Bind hotkey %q failed: %v", combo, err)
		return
	}
	a.hotkeyBound = true
}

func (a *App) unbindHotkeyLocked() {
	if a.hotkeyHandler == nil || !a.hotkeyBound {
		return
	}
	a.hotkeyHandler.Unbind()
	a.hotkeyBound = false
}

// SetHotkeyEnabled enables or disables the global hotkey at runtime and
// persists the change to the config file.
func (a *App) SetHotkeyEnabled(enabled bool) {
	a.mu.Lock()
	a.cfg.HotkeyEnabled = enabled
	if enabled {
		a.bindHotkeyLocked(a.cfg.Hotkey)
	} else {
		a.unbindHotkeyLocked()
	}
	cfgCopy := *a.cfg
	a.mu.Unlock()

	if err := config.Save(&cfgCopy); err != nil {
		log.Printf("Save config failed: %v", err)
	}
}

// HotkeyEnabled reports whether the hotkey is currently enabled in config.
func (a *App) HotkeyEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.HotkeyEnabled
}

// applySavedConfig is invoked by the settings window after the user saves. It
// persists the config, reloads the engine, and rebinds the hotkey.
func (a *App) applySavedConfig(newCfg *config.Config) error {
	if err := config.Save(newCfg); err != nil {
		return err
	}

	a.mu.Lock()
	a.cfg = newCfg
	if newCfg.HotkeyEnabled {
		a.bindHotkeyLocked(newCfg.Hotkey)
	} else {
		a.unbindHotkeyLocked()
	}
	a.mu.Unlock()

	if err := a.controller.Reload(newCfg); err != nil {
		log.Printf("Controller reload failed: %v", err)
		return err
	}

	if a.updateTrayStatus != nil {
		fyne.Do(func() { a.updateTrayStatus(a.controller.IsRecording()) })
	}
	log.Println("Settings applied")
	return nil
}

// showSettings brings the settings window to the foreground.
func (a *App) showSettings() {
	if a.settingsWindow == nil {
		return
	}
	a.settingsWindow.Show()
	a.settingsWindow.RequestFocus()
}

// Run starts the fyne event loop on the calling (main) goroutine and blocks
// until the app quits.
func (a *App) Run() {
	a.fyneApp.Run()
}

// Quit stops the hotkey listener and terminates the fyne app.
func (a *App) Quit() {
	a.mu.Lock()
	handler := a.hotkeyHandler
	a.mu.Unlock()

	if handler != nil {
		handler.Stop()
		handler.Close()
	}
	fyne.Do(func() { a.fyneApp.Quit() })
}
