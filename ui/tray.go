package ui

import (
	"github.com/gogpu/systray"

	"linux-voice-input/assets"
	"linux-voice-input/version"
)

// setupTray builds the system tray icon and its context menu, storing handles to
// the items that update at runtime (status, hotkey checkbox, engine markers).
// The menu layout is:
//
//	状态: 空闲                 (disabled when idle; clickable to cancel while recording)
//	---
//	启用热键监听               (checkbox)
//	启用语音纠错               (checkbox)
//	启用语音统计               (checkbox)
//	查看语音统计
//	---
//	sensevoice|sherpa|whisper   (checkboxes; only the active engine is checked)
//	---
//	下载模型 / 环境检查
//	---
//	打开配置目录 / 重载配置
//	---
//	Linux Voice Input x.y.z    (disabled; shows the running version)
//	退出
//
// All click callbacks run on gogpu/systray's D-Bus goroutine; handlers that do
// slow work (engine reload, download, checks) dispatch to their own goroutines.
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
	a.correctionsItem = menu.AddCheckbox("启用语音纠错", a.CorrectionsEnabled(), func() {
		a.toggleCorrections()
	})
	a.statisticsItem = menu.AddCheckbox("启用语音统计", a.StatisticsEnabled(), func() {
		a.toggleStatistics()
	})
	menu.Add("查看语音统计", func() { a.showStatistics() })

	menu.AddSeparator()

	selectedEngine := a.currentEngine()
	for _, name := range engineNames {
		engineName := name
		item := menu.AddCheckbox(engineName, engineName == selectedEngine, func() {
			a.selectEngine(engineName)
		})
		a.engineItems[engineName] = item
	}

	menu.AddSeparator()

	a.downloadItem = menu.Add("下载模型", func() { a.toggleDownload() })
	menu.Add("环境检查", func() { a.runEnvCheck() })

	menu.AddSeparator()

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

// currentEngine returns the configured engine name under the lock.
func (a *App) currentEngine() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Engine
}
