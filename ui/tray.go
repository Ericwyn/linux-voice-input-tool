package ui

import (
	"github.com/gogpu/systray"

	"linux-voice-input/assets"
	"linux-voice-input/version"
)

// setupTray builds the system tray icon and its context menu, storing handles to
// the items that update at runtime (status, hotkey checkbox, info lines). The
// tray is intentionally minimal: engine/correction selection, model download,
// environment check and statistics all moved to the "设置" web panel. The menu
// layout is:
//
//	状态: 空闲                 (disabled when idle; clickable to cancel while recording)
//	---
//	启用热键监听               (checkbox)
//	---
//	引擎: <name>               (disabled info line; change in settings)
//	纠错: <mode>               (disabled info line; change in settings)
//	---
//	设置 / 打开配置目录 / 重载配置
//	---
//	Linux Voice Input x.y.z    (disabled; shows the running version)
//	退出
//
// All click callbacks run on gogpu/systray's D-Bus goroutine; handlers that do
// slow work (reload, open browser) dispatch to their own goroutines.
func (a *App) setupTray() {
	tray := systray.New()
	menu := systray.NewMenu()

	// Status item: disabled (informational) when idle; while recording it is
	// enabled and clicking it cancels the recording, which is the GNOME-friendly
	// fallback for when the notification action button is unavailable.
	a.statusItem = menu.Add("状态: 空闲", func() {
		a.controller.Cancel()
	})
	a.statusItem.SetDisabled(true)

	menu.AddSeparator()

	a.hotkeyItem = menu.AddCheckbox("启用热键监听", a.HotkeyEnabled(), func() {
		a.toggleHotkey()
	})

	menu.AddSeparator()

	// Engine and correction are shown as read-only info lines reflecting the
	// current config; changing them (and model download / env check / stats) now
	// lives in the "设置" web panel, keeping the tray minimal.
	a.engineItem = menu.Add("", nil)
	a.engineItem.SetDisabled(true)
	a.correctionItem = menu.Add("", nil)
	a.correctionItem.SetDisabled(true)
	a.refreshInfoLines()

	menu.AddSeparator()

	menu.Add("设置", func() { a.openSettings() })
	menu.Add("打开配置目录", func() { a.openConfigDir() })
	menu.Add("重载配置", func() { a.reloadConfig() })

	menu.AddSeparator()

	// Version line: a disabled, non-clickable informational item so users can
	// see the running version without opening a terminal.
	versionItem := menu.Add("v"+version.Version, nil)
	versionItem.SetDisabled(true)

	menu.Add("退出", func() { a.Quit() })

	tray.SetIcon(assets.TrayIconPNG).
		SetTooltip("Linux Voice Input - 空闲").
		SetMenu(menu).
		Show()

	a.tray = tray
}
