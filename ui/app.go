// Package ui hosts the daemon-mode system tray built on gogpu/systray, plus the
// global hotkey lifecycle. All user-facing configuration is done by editing the
// config file and reloading it from the tray; there is no settings window.
// Long-running side tasks (model download, environment check) report progress
// through desktop notifications rather than windows. The recording pipeline
// itself lives in the session package; this package only orchestrates it.
package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"

	"github.com/gogpu/systray"

	"linux-voice-input/config"
	"linux-voice-input/doctor"
	"linux-voice-input/hotkey"
	"linux-voice-input/models"
	"linux-voice-input/notify"
	"linux-voice-input/session"
)

// engineNames is the fixed set of selectable engines, in tray display order.
var engineNames = []string{"sensevoice", "sherpa", "whisper"}

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
	tray            *systray.SystemTray
	statusItem      *systray.MenuItem
	hotkeyItem      *systray.MenuItem
	correctionsItem *systray.MenuItem
	engineItems     map[string]*systray.MenuItem
	downloadItem    *systray.MenuItem

	// downloadMu guards downloadCancel, which is non-nil exactly while a model
	// download is in flight. Cancelling it aborts the transfer; the download
	// goroutine clears it and restores the menu label when it returns. It is a
	// separate lock from mu so a click can query download state without
	// contending on the config lock.
	downloadMu     sync.Mutex
	downloadCancel context.CancelFunc
}

// NewApp constructs the daemon UI: it initializes the global hotkey handler,
// builds the tray menu, wires the recording state-change callback, and registers
// the notification "取消" action so clicking it cancels the active recording.
func NewApp(cfg *config.Config, controller *session.Controller) *App {
	app := &App{
		controller:  controller,
		cfg:         cfg,
		saveConfig:  config.Save,
		engineItems: make(map[string]*systray.MenuItem),
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

// CorrectionsEnabled reports whether final-text correction is enabled in the
// live UI configuration.
func (a *App) CorrectionsEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Corrections.Enabled
}

// toggleCorrections flips the post-recognition correction service, persists
// the setting, and updates the tray checkbox. Unlike an engine switch it never
// reloads SenseVoice; the selected correction model initializes lazily on the
// next final transcription.
func (a *App) toggleCorrections() {
	a.mu.Lock()
	enabled := !a.cfg.Corrections.Enabled
	a.cfg.Corrections.Enabled = enabled
	cfgCopy := *a.cfg
	saveConfig := a.saveConfig
	a.mu.Unlock()

	a.controller.SetCorrectionsEnabled(enabled)
	if saveConfig == nil {
		saveConfig = config.Save
	}
	if err := saveConfig(&cfgCopy); err != nil {
		log.Printf("Save correction toggle failed: %v", err)
	}
	if a.correctionsItem != nil {
		a.correctionsItem.SetChecked(enabled)
	}
	if a.tray != nil {
		a.notifyCorrectionToggle(enabled, &cfgCopy)
	}
	log.Printf("Text corrections enabled=%v", enabled)
}

func (a *App) notifyCorrectionToggle(enabled bool, cfg *config.Config) {
	if !enabled {
		notify.NotifyAux("语音纠错", "已关闭", true)
		return
	}
	if cfg.Corrections.Strategy == "" {
		notify.NotifyAux("语音纠错", "已启用（仅用户词典）", true)
		return
	}
	set := models.ForCorrectionStrategy(cfg.Corrections.Strategy)
	if len(set) == 0 {
		notify.NotifyAux("语音纠错", "配置的模型策略无效，请检查配置文件", true)
		return
	}
	missing := models.Check(cfg.CorrectionsModelDir(), set)
	if len(missing) > 0 {
		notify.NotifyAux("语音纠错",
			fmt.Sprintf("已启用，但缺少 %d 个模型文件；请点托盘“下载模型”自动获取", len(missing)), true)
		return
	}
	notify.NotifyAux("语音纠错", "已启用，模型已就绪", true)
}

// selectEngine switches the active recognition engine from the tray: it persists
// the new engine, reloads the controller, refreshes the engine menu markers, and
// then runs an immediate model check, notifying the user to download models when
// any are missing. The work runs on its own goroutine so a slow engine reload
// (which may reload local models) does not block the D-Bus menu callback.
func (a *App) selectEngine(name string) {
	go func() {
		a.mu.Lock()
		same := a.cfg.Engine == name
		a.cfg.Engine = name
		cfgCopy := *a.cfg
		a.mu.Unlock()

		if !same {
			if err := config.Save(&cfgCopy); err != nil {
				log.Printf("Save config failed: %v", err)
				notify.NotifyAux("语音输入", "保存配置失败: "+err.Error(), true)
				return
			}
			if err := a.controller.Reload(&cfgCopy); err != nil {
				log.Printf("Controller reload failed: %v", err)
			}
			a.refreshEngineItems(name)
		}
		a.checkModelsNotify(name, cfgCopy.ModelBaseDir())
	}()
}

// refreshEngineItems checks only the currently selected engine's menu item and
// unchecks the rest, so the engine list behaves as a single-select group with
// the same checkmark styling as the hotkey toggle.
func (a *App) refreshEngineItems(selected string) {
	for name, item := range a.engineItems {
		item.SetChecked(name == selected)
	}
}

// checkModelsNotify checks the local model set for the given engine and reports
// the result via a notification. Engines with no local models (whisper) simply
// confirm the switch.
func (a *App) checkModelsNotify(engineName, baseDir string) {
	set := models.ForEngine(engineName)
	if len(set) == 0 {
		notify.NotifyAux("语音输入", "已切换到 "+engineName+"（在线引擎，无需本地模型）", true)
		return
	}
	missing := models.Check(baseDir, set)
	if len(missing) == 0 {
		notify.NotifyAux("语音输入", "已切换到 "+engineName+"，模型已就绪", true)
		return
	}
	notify.NotifyAux("语音输入",
		fmt.Sprintf("已切换到 %s，但缺少 %d 个模型文件，请点托盘“下载模型”获取", engineName, len(missing)),
		true)
}

// toggleDownload is wired to the "下载模型 / 中断下载" menu item. It doubles as
// start and stop: when a download is already running it cancels it; otherwise it
// starts a new one. The single item flips its label between the two states so
// the user always sees the action a click will perform.
func (a *App) toggleDownload() {
	a.downloadMu.Lock()
	inFlight := a.downloadCancel != nil
	a.downloadMu.Unlock()

	if inFlight {
		a.cancelDownload()
		return
	}
	a.startDownload()
}

// cancelDownload aborts an in-flight download if one is running. The download
// goroutine observes the cancelled context, reports the interruption, and
// restores the menu label as it unwinds, so this only needs to trip the cancel
// function. It is a no-op when nothing is downloading.
func (a *App) cancelDownload() {
	a.downloadMu.Lock()
	cancel := a.downloadCancel
	a.downloadMu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// startDownload downloads every model required by the live config: recognition
// models plus the selected correction model when enabled, followed by the
// shared ONNX Runtime libraries. It runs off the D-Bus callback goroutine and
// remains cancellable through the same menu item.
func (a *App) startDownload() {
	a.mu.Lock()
	request := models.DownloadRequest{
		EngineName:         a.cfg.Engine,
		EngineBaseDir:      a.cfg.ModelBaseDir(),
		CorrectionsEnabled: a.cfg.Corrections.Enabled,
		CorrectionStrategy: a.cfg.Corrections.Strategy,
		CorrectionBaseDir:  a.cfg.CorrectionsModelDir(),
	}
	a.mu.Unlock()

	groups, err := models.RequiredDownloads(request)
	if err != nil {
		notify.NotifyAux("下载模型", "模型配置错误: "+err.Error(), true)
		return
	}
	if len(groups) == 0 {
		notify.NotifyAux("下载模型", "当前配置无需本地模型", true)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	a.downloadMu.Lock()
	if a.downloadCancel != nil {
		// A download is already running; ignore this second start.
		a.downloadMu.Unlock()
		cancel()
		return
	}
	a.downloadCancel = cancel
	a.downloadMu.Unlock()

	a.setDownloadItemLabel("中断下载")

	go func() {
		defer func() {
			cancel()
			a.downloadMu.Lock()
			a.downloadCancel = nil
			a.downloadMu.Unlock()
			a.setDownloadItemLabel("下载模型")
		}()

		onStatus := func(msg string) { notify.NotifyAux("下载模型", msg, false) }
		onProgress := func(p models.DownloadProgress) { notify.NotifyAux("下载模型", p.String(), false) }

		for _, group := range groups {
			onStatus(fmt.Sprintf("%s目录: %s", group.Label, group.BaseDir))
			if err := models.DownloadMissing(ctx, group.BaseDir, group.Models, onStatus, onProgress); err != nil {
				a.reportDownloadError(ctx, err)
				return
			}
		}
		// Local engines also need the sherpa-onnx shared libraries next to the
		// binary (found via its $ORIGIN/lib rpath).
		libDir := config.RuntimeLibDir()
		if err := models.DownloadRuntimeLibrary(ctx, libDir, onStatus, onProgress); err != nil {
			a.reportDownloadError(ctx, err)
			return
		}
		notify.NotifyAux("下载模型", "全部模型与运行库已就绪", true)
	}()
}

// reportDownloadError distinguishes a user-triggered interruption (cancelled
// context) from a genuine transfer failure and notifies accordingly. A
// cancellation surfaces as a context error either directly or wrapped by the
// download helpers, so both the returned err and ctx are inspected.
func (a *App) reportDownloadError(ctx context.Context, err error) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		notify.NotifyAux("下载模型", "已中断下载", true)
		return
	}
	notify.NotifyAux("下载模型", "下载失败: "+err.Error(), true)
}

// setDownloadItemLabel updates the download menu item's label, guarding against
// a nil item (the tray may be unavailable in a headless environment).
func (a *App) setDownloadItemLabel(label string) {
	if a.downloadItem != nil {
		a.downloadItem.SetLabel(label)
	}
}

// runEnvCheck runs the shared doctor checks and shows the report as a single
// multi-line notification. It runs on its own goroutine because some checks make
// D-Bus/exec calls.
func (a *App) runEnvCheck() {
	a.mu.Lock()
	cfgCopy := *a.cfg
	a.mu.Unlock()

	go func() {
		results := doctor.Run(&cfgCopy)
		var builder strings.Builder
		for _, r := range results {
			mark := "✓"
			if !r.OK {
				mark = "✗"
			}
			builder.WriteString(fmt.Sprintf("[%s] %s: %s\n", mark, r.Name, r.Detail))
		}
		notify.NotifyAux("环境检查", strings.TrimRight(builder.String(), "\n"), true)
	}()
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
		engineName := newCfg.Engine
		hotkeyEnabled := newCfg.HotkeyEnabled
		correctionsEnabled := newCfg.Corrections.Enabled
		a.mu.Unlock()

		if err := a.controller.Reload(newCfg); err != nil {
			log.Printf("Controller reload failed: %v", err)
		}

		a.refreshEngineItems(engineName)
		if a.hotkeyItem != nil {
			a.hotkeyItem.SetChecked(hotkeyEnabled)
		}
		if a.correctionsItem != nil {
			a.correctionsItem.SetChecked(correctionsEnabled)
		}
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
