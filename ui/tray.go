package ui

import (
	"log"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

	"linux-voice-input/assets"
)

// setupTray installs the system tray menu. It is a no-op if the running fyne
// driver does not support a system tray (e.g. a non-desktop environment).
func (a *App) setupTray() {
	deskApp, ok := a.fyneApp.(desktop.App)
	if !ok {
		log.Println("System tray not supported by this driver")
		return
	}

	statusItem := fyne.NewMenuItem("状态: 空闲", nil)
	statusItem.Disabled = true

	hotkeyItem := fyne.NewMenuItem("启用热键监听", nil)
	hotkeyItem.Checked = a.HotkeyEnabled()
	hotkeyItem.Action = func() {
		enabled := !a.HotkeyEnabled()
		a.SetHotkeyEnabled(enabled)
		hotkeyItem.Checked = enabled
		a.refreshTrayMenu(deskApp, statusItem, hotkeyItem)
	}

	settingsItem := fyne.NewMenuItem("打开设置…", func() {
		a.showSettings()
	})

	quitItem := fyne.NewMenuItem("退出", func() {
		a.Quit()
	})

	menu := fyne.NewMenu("Linux Voice Input",
		statusItem,
		fyne.NewMenuItemSeparator(),
		hotkeyItem,
		settingsItem,
		fyne.NewMenuItemSeparator(),
		quitItem,
	)

	deskApp.SetSystemTrayIcon(assets.AppIcon)
	deskApp.SetSystemTrayMenu(menu)

	a.updateTrayStatus = func(recording bool) {
		if recording {
			statusItem.Label = "状态: 录音中…"
		} else {
			statusItem.Label = "状态: 空闲"
		}
		hotkeyItem.Checked = a.HotkeyEnabled()
		a.refreshTrayMenu(deskApp, statusItem, hotkeyItem)
	}
}

// refreshTrayMenu re-applies the menu so that label/checked changes are
// reflected in the tray (fyne does not auto-refresh mutated menu items).
func (a *App) refreshTrayMenu(deskApp desktop.App, statusItem, hotkeyItem *fyne.MenuItem) {
	settingsItem := fyne.NewMenuItem("打开设置…", func() {
		a.showSettings()
	})
	quitItem := fyne.NewMenuItem("退出", func() {
		a.Quit()
	})

	menu := fyne.NewMenu("Linux Voice Input",
		statusItem,
		fyne.NewMenuItemSeparator(),
		hotkeyItem,
		settingsItem,
		fyne.NewMenuItemSeparator(),
		quitItem,
	)
	deskApp.SetSystemTrayMenu(menu)
}
