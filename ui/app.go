// Package ui hosts the daemon-mode system tray built on gogpu/systray, plus the
// global hotkey lifecycle. All user-facing configuration is done by editing the
// config file and reloading it from the tray; there is no settings window.
// Long-running side tasks (model download, environment check) report progress
// through desktop notifications rather than windows. The recording pipeline
// itself lives in the session package; this package only orchestrates it.
package ui

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/gogpu/systray"

	"linux-voice-input/config"
	"linux-voice-input/doctor"
	"linux-voice-input/hotkey"
	"linux-voice-input/models"
	"linux-voice-input/notify"
	"linux-voice-input/session"
	"linux-voice-input/settings"
	"linux-voice-input/stats"
)

// App wires together the system tray, global hotkey handler, and the shared
// session controller.
type App struct {
	controller *session.Controller
	// saveConfig is injectable for focused tray behavior tests.
	saveConfig func(*config.Config) error

	mu            sync.Mutex
	cfg           *config.Config
	hotkeyHandler *hotkey.Handler
	hotkeyBound   bool

	// tray and the menu-item handles are created in setupTray and updated live
	// from menu callbacks and the recording state-change callback.
	tray       *systray.SystemTray
	statusItem *systray.MenuItem
	hotkeyItem *systray.MenuItem
	// engineItem and correctionItem are read-only info lines showing the current
	// engine and correction mode; selection moved to the settings web panel.
	engineItem     *systray.MenuItem
	correctionItem *systray.MenuItem

	// settingsServer is the on-demand loopback web UI for editing config and
	// running tools (download / env check / stats). It is created lazily on the
	// first "设置" click and then stays up (Start is idempotent); saving through
	// it triggers reloadConfig via OnSaved.
	settingsServer *settings.Server
}

// NewApp constructs the daemon UI: it initializes the global hotkey handler,
// builds the tray menu, wires the recording state-change callback, and registers
// the notification "取消" action so clicking it cancels the active recording.
func NewApp(cfg *config.Config, controller *session.Controller) *App {
	app := &App{
		controller: controller,
		cfg:        cfg,
		saveConfig: config.Save,
	}

	app.initHotkey()
	app.setupTray()

	controller.SetOnStateChange(app.updateStatus)

	// The notification "取消" button (when the server supports actions) and the
	// tray status item both route to Cancel; Cancel is a no-op when idle.
	notify.SetActionHandler(func(action string) {
		if action == notify.CancelAction {
			controller.Cancel()
		}
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

	// The xgbutil event loop runs on its own X connection, independent of the
	// tray's D-Bus loop, so it is safe to run in a separate goroutine.
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

// HotkeyEnabled reports whether the hotkey is currently enabled in config.
func (a *App) HotkeyEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.HotkeyEnabled
}

// toggleHotkey flips the global hotkey on/off at runtime, binds/unbinds
// accordingly, persists the change, and updates the tray checkbox. It is wired
// to the "启用热键监听" menu item.
func (a *App) toggleHotkey() {
	a.mu.Lock()
	enabled := !a.cfg.HotkeyEnabled
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
	if a.hotkeyItem != nil {
		a.hotkeyItem.SetChecked(enabled)
	}
}

// refreshInfoLines updates the read-only engine and correction tray labels to
// reflect the live config. Engine/correction selection now lives in the
// settings web panel; these lines are purely informational.
func (a *App) refreshInfoLines() {
	a.mu.Lock()
	engine := a.cfg.Engine
	mode := config.NormalizeCorrectionMode(a.cfg.CorrectionMode)
	a.mu.Unlock()
	if a.engineItem != nil {
		a.engineItem.SetLabel("引擎: " + engine)
	}
	if a.correctionItem != nil {
		a.correctionItem.SetLabel("纠错: " + correctionModeLabel(mode))
	}
}

// correctionModeLabel maps a mode id to its Chinese display name.
func correctionModeLabel(mode string) string {
	switch mode {
	case config.CorrectionModeLocal:
		return "本地"
	case config.CorrectionModeLLM:
		return "LLM"
	case config.CorrectionModeCombined:
		return "本地+LLM"
	default:
		return "关闭"
	}
}

// statsProvider builds the settings.Options.Stats callback: it flushes pending
// events, then aggregates today / rolling-30-day / all-time windows into the
// display-ready shape the settings page renders. Engine/correction toggles and
// the former tray statistics view are gone; this backs the stats web panel.
func (a *App) statsProvider() (settings.StatsResponse, error) {
	if err := a.controller.FlushStatistics(); err != nil {
		log.Printf("Flush statistics before view failed: %v", err)
	}
	root := config.StatsDir()
	now := time.Now()
	today, err := stats.LoadRecent(root, now, 1)
	if err != nil {
		return settings.StatsResponse{}, err
	}
	recent, err := stats.LoadRecent(root, now, 30)
	if err != nil {
		return settings.StatsResponse{}, err
	}
	all, err := stats.LoadAll(root)
	if err != nil {
		return settings.StatsResponse{}, err
	}
	a.mu.Lock()
	enabled := a.cfg.Statistics.Enabled
	a.mu.Unlock()
	return settings.StatsResponse{
		Enabled: enabled,
		Today:   statsPeriod("今日", today),
		Recent:  statsPeriod("最近 30 天", recent),
		All:     statsPeriod("全部累计", all),
	}, nil
}

// statsPeriod converts one aggregate into the display-ready settings shape.
func statsPeriod(label string, agg stats.Aggregate) settings.StatsPeriod {
	c := agg.Totals
	return settings.StatsPeriod{
		Label:             label,
		Days:              agg.Days,
		SuccessfulInputs:  c.SuccessfulInputs,
		Characters:        c.Characters,
		HanCharacters:     c.HanCharacters,
		CorrectedInputs:   c.CorrectedInputs,
		RecordingDuration: stats.FormatDuration(c.RecordingDurationMS),
		InputDuration:     stats.FormatDuration(c.InputDurationMS),
	}
}

// doctorProvider builds the settings.Options.Doctor callback: it runs the shared
// environment checks against the live config and maps them to the settings
// shape. It replaces the former tray "环境检查" item.
func (a *App) doctorProvider() []settings.DoctorItem {
	a.mu.Lock()
	cfgCopy := *a.cfg
	a.mu.Unlock()
	results := doctor.Run(&cfgCopy)
	items := make([]settings.DoctorItem, 0, len(results))
	for _, r := range results {
		items = append(items, settings.DoctorItem{Name: r.Name, OK: r.OK, Detail: r.Detail})
	}
	return items
}

// downloadProvider builds the settings.DownloadFunc: it downloads every model
// the live config needs (recognition + optional correction) plus the shared
// ONNX runtime, emitting progress lines to the settings download console. It
// replaces the former tray "下载模型" item.
func (a *App) downloadProvider(ctx context.Context, emit func(string)) error {
	a.mu.Lock()
	request := models.DownloadRequest{
		EngineName:         a.cfg.Engine,
		EngineBaseDir:      a.cfg.ModelBaseDir(),
		CorrectionsEnabled: a.cfg.UseLocalCorrection(),
		CorrectionStrategy: a.cfg.Corrections.Strategy,
		CorrectionBaseDir:  a.cfg.CorrectionsModelDir(),
	}
	a.mu.Unlock()

	groups, err := models.RequiredDownloads(request)
	if err != nil {
		return fmt.Errorf("模型配置错误: %w", err)
	}
	if len(groups) == 0 {
		emit("当前配置无需本地模型")
	}

	onStatus := func(msg string) { emit(msg) }
	onProgress := func(p models.DownloadProgress) { emit(p.String()) }

	for _, group := range groups {
		emit(fmt.Sprintf("%s目录: %s", group.Label, group.BaseDir))
		if err := models.DownloadMissing(ctx, group.BaseDir, group.Models, onStatus, onProgress); err != nil {
			return err
		}
	}
	// Local engines also need the sherpa-onnx shared libraries next to the
	// binary (found via its $ORIGIN/lib rpath).
	libDir := config.RuntimeLibDir()
	emit("运行库目录: " + libDir)
	if err := models.DownloadRuntimeLibrary(ctx, libDir, onStatus, onProgress); err != nil {
		return err
	}
	emit("全部模型与运行库已就绪")
	return nil
}

// openConfigDir opens the application directory (holding config.yaml and logs)
// in the user's file manager via xdg-open.
func (a *App) openConfigDir() {
	dir := config.AppDir()
	if err := exec.Command("xdg-open", dir).Start(); err != nil {
		log.Printf("open config dir failed: %v", err)
		notify.NotifyAux("语音输入", "打开配置目录失败: "+err.Error(), true)
	}
}

// openSettings starts the loopback settings web UI (idempotent) and opens it in
// the default browser. The server is created lazily on first use and then stays
// up for the daemon's lifetime. Saving through the page writes config.yaml and
// fires OnSaved, which reuses the existing reloadConfig path so changes apply
// live without a restart. Runs on its own goroutine off the D-Bus callback.
func (a *App) openSettings() {
	go func() {
		a.mu.Lock()
		if a.settingsServer == nil {
			a.settingsServer = settings.NewServer(settings.Options{
				// OnSaved runs on the server's request goroutine; reloadConfig
				// itself dispatches to its own goroutine, so this returns fast.
				OnSaved:  a.reloadConfig,
				Stats:    a.statsProvider,
				Doctor:   a.doctorProvider,
				Download: a.downloadProvider,
			})
		}
		server := a.settingsServer
		a.mu.Unlock()

		if err := server.Start(); err != nil {
			log.Printf("Start settings server failed: %v", err)
			notify.NotifyAux("语音输入", "启动设置服务失败: "+err.Error(), true)
			return
		}
		if err := exec.Command("xdg-open", server.URL()).Start(); err != nil {
			log.Printf("open settings page failed: %v", err)
			// Fall back to telling the user the address so they can open it
			// manually; the token is in the URL.
			notify.NotifyAux("语音输入", "请在浏览器打开: "+server.URL(), true)
		}
	}()
}

// reloadConfig re-reads the config file from disk, applies it to the controller
// and hotkey, and refreshes the tray to reflect the new values. It is wired to
// the "重载配置" menu item so edits made in the config file take effect without
// restarting the daemon. It runs on its own goroutine since a reload may reload
// local models.
func (a *App) reloadConfig() {
	go func() {
		newCfg, err := config.Load()
		if err != nil {
			log.Printf("Reload config failed: %v", err)
			notify.NotifyAux("语音输入", "重载配置失败: "+err.Error(), true)
			return
		}

		a.mu.Lock()
		a.cfg = newCfg
		if newCfg.HotkeyEnabled {
			a.bindHotkeyLocked(newCfg.Hotkey)
		} else {
			a.unbindHotkeyLocked()
		}
		hotkeyEnabled := newCfg.HotkeyEnabled
		a.mu.Unlock()

		if err := a.controller.Reload(newCfg); err != nil {
			log.Printf("Controller reload failed: %v", err)
		}

		if a.hotkeyItem != nil {
			a.hotkeyItem.SetChecked(hotkeyEnabled)
		}
		a.refreshInfoLines()
		notify.NotifyAux("语音输入", "配置已重载", true)
	}()
}

// updateStatus reflects the recording state on the tray: the status item becomes
// clickable (to cancel) while recording and disabled when idle, and the tooltip
// tracks the state. It is registered as the controller's state-change callback
// and may be invoked from a background goroutine.
func (a *App) updateStatus(recording bool) {
	if a.statusItem != nil {
		if recording {
			a.statusItem.SetLabel("状态: 录音中…（点击取消）")
			a.statusItem.SetDisabled(false)
		} else {
			a.statusItem.SetLabel("状态: 空闲")
			a.statusItem.SetDisabled(true)
		}
	}
	if a.tray != nil {
		if recording {
			a.tray.SetTooltip("Linux Voice Input - 录音中")
		} else {
			a.tray.SetTooltip("Linux Voice Input - 空闲")
		}
	}
}

// Run blocks on the systray message loop until Quit is called.
func (a *App) Run() {
	if a.tray == nil {
		log.Println("System tray unavailable; nothing to run")
		return
	}
	if err := a.tray.Run(); err != nil {
		log.Printf("systray run error: %v", err)
	}
}

// Quit stops the hotkey listener and removes the tray, which unblocks Run.
func (a *App) Quit() {
	a.mu.Lock()
	handler := a.hotkeyHandler
	tray := a.tray
	a.mu.Unlock()

	if handler != nil {
		handler.Stop()
		handler.Close()
	}
	if tray != nil {
		tray.Remove()
	}
}
