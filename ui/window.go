package ui

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"linux-voice-input/config"
	"linux-voice-input/doctor"
	"linux-voice-input/models"
)

// settingsForm holds references to every editable widget so that Save can
// collect their values back into a config.Config.
type settingsForm struct {
	engineSelect *widget.Select

	// sherpa fields: a single model directory drives all six model paths.
	modelDir    *widget.Entry
	modelStatus *widget.Label
	numThreads  *widget.Entry
	rule1       *widget.Entry
	rule2       *widget.Entry

	// whisper fields
	apiKey  *widget.Entry
	baseURL *widget.Entry
	model   *widget.Entry

	// common fields
	sampleRate       *widget.Entry
	hotkey           *widget.Entry
	hotkeyEnabled    *widget.Check
	restoreClipboard *widget.Check

	// notification switches keyed by a stable label
	notifChecks map[string]*widget.Check
}

// buildSettingsWindow constructs the settings window and stores it on the App.
// The window is hidden on close (SetCloseIntercept) so the daemon keeps living.
func (a *App) buildSettingsWindow() {
	window := a.fyneApp.NewWindow("Linux Voice Input 设置")
	window.Resize(fyne.NewSize(560, 640))

	form := &settingsForm{notifChecks: make(map[string]*widget.Check)}
	content := a.buildSettingsContent(window, form)

	window.SetContent(content)
	window.SetCloseIntercept(func() {
		window.Hide()
	})
	window.Hide()

	a.settingsWindow = window
}

// buildSettingsContent builds all widgets, initializes them from the current
// config, and returns the top-level container.
func (a *App) buildSettingsContent(window fyne.Window, form *settingsForm) fyne.CanvasObject {
	cfg := a.controller.Config()

	// --- Engine selector ---
	form.engineSelect = widget.NewSelect([]string{"sherpa", "whisper"}, nil)
	form.engineSelect.SetSelected(cfg.API.Engine)

	engineForm := widget.NewForm(
		hintedItem("识别引擎", form.engineSelect,
			"sherpa = 本地离线识别（推荐，无需联网）；whisper = 在线 API 识别（需网络和密钥）"),
	)

	// --- Sherpa group: single model directory + check/download ---
	baseDir := cfg.Sherpa.ModelDir
	if baseDir == "" {
		baseDir = config.DefaultModelDir()
	}
	form.modelDir = widget.NewEntry()
	form.modelDir.SetText(baseDir)
	browseButton := widget.NewButton("浏览…", func() {
		folderDialog := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil || uri == nil {
				return
			}
			form.modelDir.SetText(uri.Path())
		}, window)
		folderDialog.Show()
	})
	modelDirRow := container.NewBorder(nil, nil, nil, browseButton, form.modelDir)

	form.modelStatus = widget.NewLabel("")
	form.modelStatus.Wrapping = fyne.TextWrapWord

	checkButton := widget.NewButton("检查模型", func() {
		a.checkModels(form.modelDir.Text, form.modelStatus)
	})
	downloadButton := widget.NewButton("下载缺失模型", func() {
		a.downloadModels(window, form.modelDir.Text, form.modelStatus, form)
	})

	form.numThreads = widget.NewEntry()
	form.numThreads.SetText(strconv.Itoa(cfg.Sherpa.NumThreads))
	form.rule1 = widget.NewEntry()
	form.rule1.SetText(strconv.FormatFloat(float64(cfg.Sherpa.Rule1MinTrailingSilence), 'f', -1, 32))
	form.rule2 = widget.NewEntry()
	form.rule2.SetText(strconv.FormatFloat(float64(cfg.Sherpa.Rule2MinTrailingSilence), 'f', -1, 32))

	sherpaForm := widget.NewForm(
		hintedItem("模型目录", modelDirRow,
			"存放语音识别模型的文件夹。若缺失，点下方“下载缺失模型”自动获取"),
		hintedItem("推理线程数", form.numThreads,
			"识别时占用的 CPU 线程数，越大越快但更耗电，一般填 4"),
		hintedItem("长停顿断句 (秒)", form.rule1,
			"说话停顿超过这么久就当作一句话结束，默认 2.4"),
		hintedItem("短停顿断句 (秒)", form.rule2,
			"较短停顿的断句阈值，默认 1.2；一般保持默认即可"),
	)
	sherpaGroup := container.NewVBox(
		sherpaForm,
		container.NewHBox(checkButton, downloadButton),
		form.modelStatus,
	)

	// --- Whisper group ---
	form.apiKey = widget.NewPasswordEntry()
	form.apiKey.SetText(cfg.API.Key)
	form.baseURL = widget.NewEntry()
	form.baseURL.SetText(cfg.API.BaseURL)
	form.model = widget.NewEntry()
	form.model.SetText(cfg.API.Model)

	whisperGroup := widget.NewForm(
		hintedItem("API Key", form.apiKey,
			"在线识别服务（SiliconFlow）的密钥，使用 whisper 引擎时必填"),
		hintedItem("接口地址", form.baseURL,
			"在线识别服务的 API 地址，一般保持默认"),
		hintedItem("模型名称", form.model,
			"在线识别使用的模型，一般保持默认"),
	)

	// Engine switch toggles which group is visible.
	updateEngineVisibility := func(engine string) {
		if engine == "whisper" {
			sherpaGroup.Hide()
			whisperGroup.Show()
		} else {
			whisperGroup.Hide()
			sherpaGroup.Show()
		}
	}
	form.engineSelect.OnChanged = updateEngineVisibility
	updateEngineVisibility(cfg.API.Engine)

	// --- Common group ---
	form.sampleRate = widget.NewEntry()
	form.sampleRate.SetText(strconv.Itoa(cfg.Audio.SampleRate))
	form.hotkey = widget.NewEntry()
	form.hotkey.SetText(cfg.Hotkey)
	form.hotkeyEnabled = widget.NewCheck("启用全局热键监听", nil)
	form.hotkeyEnabled.SetChecked(cfg.HotkeyEnabled)
	form.restoreClipboard = widget.NewCheck("粘贴后恢复剪贴板", nil)
	form.restoreClipboard.SetChecked(cfg.RestoreClipboard)

	commonForm := widget.NewForm(
		hintedItem("全局热键", form.hotkey,
			"按一次开始录音，再按一次结束并输入。组合如 alt+shift+g"),
		hintedItem("采样率", form.sampleRate,
			"录音采样率，默认 16000，一般不用改"),
	)
	commonGroup := container.NewVBox(
		commonForm,
		form.hotkeyEnabled,
		form.restoreClipboard,
	)

	// --- Notifications (collapsible) ---
	notifContent := a.buildNotificationChecks(cfg, form)
	notifAccordion := widget.NewAccordion(
		widget.NewAccordionItem("通知开关", notifContent),
	)

	// --- Buttons ---
	saveButton := widget.NewButton("保存", func() {
		if err := a.saveSettings(form); err != nil {
			dialog.ShowError(err, window)
			return
		}
		dialog.ShowInformation("已保存", "设置已保存并生效", window)
	})
	saveButton.Importance = widget.HighImportance

	cancelButton := widget.NewButton("取消", func() {
		window.Hide()
	})

	doctorButton := widget.NewButton("环境检查", func() {
		a.runDoctorCheck(window)
	})

	buttons := container.NewHBox(saveButton, cancelButton, doctorButton)

	body := container.NewVBox(
		widget.NewLabelWithStyle("识别引擎", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		engineForm,
		sherpaGroup,
		whisperGroup,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("通用", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		commonGroup,
		widget.NewSeparator(),
		notifAccordion,
	)

	scroll := container.NewVScroll(body)
	return container.NewBorder(nil, buttons, nil, nil, scroll)
}

// checkModels inspects the model directory and reports which files are missing.
func (a *App) checkModels(baseDir string, status *widget.Label) {
	missing := models.Check(baseDir)
	if len(missing) == 0 {
		status.SetText("全部模型已就绪 ✓")
		return
	}
	text := fmt.Sprintf("缺失 %d 个文件:\n", len(missing))
	for _, item := range missing {
		text += fmt.Sprintf("• %s: %s\n", item.Model, item.Path)
	}
	status.SetText(text)
}

// downloadModels downloads and extracts any missing model bundles into baseDir,
// updating the status label with progress. It runs off the UI goroutine.
func (a *App) downloadModels(window fyne.Window, baseDir string, status *widget.Label, form *settingsForm) {
	if baseDir == "" {
		dialog.ShowError(fmt.Errorf("请先填写模型目录"), window)
		return
	}

	fyne.Do(func() { status.SetText("准备下载…") })

	go func() {
		err := models.DownloadMissing(baseDir, func(msg string) {
			fyne.Do(func() { status.SetText(msg) })
		})
		fyne.Do(func() {
			if err != nil {
				status.SetText("下载失败: " + err.Error())
				dialog.ShowError(err, window)
				return
			}
			a.checkModels(baseDir, status)
		})
	}()
}

// buildNotificationChecks creates the 9 notification toggles bound to the
// current config values.
func (a *App) buildNotificationChecks(cfg *config.Config, form *settingsForm) fyne.CanvasObject {
	type notifDef struct {
		label   string
		initial bool
	}
	defs := []notifDef{
		{"开始录音", cfg.Notifications.Start},
		{"识别成功", cfg.Notifications.Success},
		{"录音过短忽略", cfg.Notifications.Ignored},
		{"录音启动失败", cfg.Notifications.RecordStartFailed},
		{"录音停止失败", cfg.Notifications.RecordStopFailed},
		{"保存失败", cfg.Notifications.SaveFailed},
		{"识别失败", cfg.Notifications.TranscribeFailed},
		{"结果为空", cfg.Notifications.EmptyResult},
		{"粘贴失败", cfg.Notifications.PasteFailed},
	}

	checks := make([]fyne.CanvasObject, 0, len(defs))
	for _, def := range defs {
		check := widget.NewCheck(def.label, nil)
		check.SetChecked(def.initial)
		form.notifChecks[def.label] = check
		checks = append(checks, check)
	}
	return container.NewVBox(checks...)
}

// saveSettings collects widget values into a new config, applies and persists.
func (a *App) saveSettings(form *settingsForm) error {
	cfg := a.controller.Config()
	newCfg := *cfg // shallow copy; nested structs are value types

	newCfg.API.Engine = form.engineSelect.Selected

	// The single model directory is the source of truth; derive the six paths.
	newCfg.Sherpa.ModelDir = form.modelDir.Text
	newCfg.ResolveModelPaths()

	if threads, err := strconv.Atoi(form.numThreads.Text); err == nil && threads > 0 {
		newCfg.Sherpa.NumThreads = threads
	}
	if rule1, err := strconv.ParseFloat(form.rule1.Text, 32); err == nil {
		newCfg.Sherpa.Rule1MinTrailingSilence = float32(rule1)
	}
	if rule2, err := strconv.ParseFloat(form.rule2.Text, 32); err == nil {
		newCfg.Sherpa.Rule2MinTrailingSilence = float32(rule2)
	}

	newCfg.API.Key = form.apiKey.Text
	newCfg.API.BaseURL = form.baseURL.Text
	newCfg.API.Model = form.model.Text

	if rate, err := strconv.Atoi(form.sampleRate.Text); err == nil && rate > 0 {
		newCfg.Audio.SampleRate = rate
	}
	newCfg.Hotkey = form.hotkey.Text
	newCfg.HotkeyEnabled = form.hotkeyEnabled.Checked
	newCfg.RestoreClipboard = form.restoreClipboard.Checked

	newCfg.Notifications.Start = form.notifChecks["开始录音"].Checked
	newCfg.Notifications.Success = form.notifChecks["识别成功"].Checked
	newCfg.Notifications.Ignored = form.notifChecks["录音过短忽略"].Checked
	newCfg.Notifications.RecordStartFailed = form.notifChecks["录音启动失败"].Checked
	newCfg.Notifications.RecordStopFailed = form.notifChecks["录音停止失败"].Checked
	newCfg.Notifications.SaveFailed = form.notifChecks["保存失败"].Checked
	newCfg.Notifications.TranscribeFailed = form.notifChecks["识别失败"].Checked
	newCfg.Notifications.EmptyResult = form.notifChecks["结果为空"].Checked
	newCfg.Notifications.PasteFailed = form.notifChecks["粘贴失败"].Checked

	log.Printf("Saving settings: engine=%s modelDir=%s hotkey=%s enabled=%v", newCfg.API.Engine, newCfg.Sherpa.ModelDir, newCfg.Hotkey, newCfg.HotkeyEnabled)
	return a.applySavedConfig(&newCfg)
}

// runDoctorCheck runs the shared doctor checks against the current config and
// shows the report in a dialog. It uses the same doctor.Run entry point as the
// --check-config CLI command.
func (a *App) runDoctorCheck(window fyne.Window) {
	results := doctor.Run(a.controller.Config())
	var builder strings.Builder
	for _, r := range results {
		mark := "✓"
		if !r.OK {
			mark = "✗"
		}
		builder.WriteString(fmt.Sprintf("[%s] %s: %s\n", mark, r.Name, r.Detail))
	}

	report := widget.NewLabel(builder.String())
	report.Wrapping = fyne.TextWrapWord
	dialog.ShowCustom("环境检查", "关闭",
		container.NewVScroll(report), window)
}

// hintedItem builds a form item that lays the label to the left of the control
// on a single row, with a short explanatory hint shown beneath it.
func hintedItem(label string, control fyne.CanvasObject, hint string) *widget.FormItem {
	item := widget.NewFormItem(label, control)
	item.HintText = hint
	return item
}
