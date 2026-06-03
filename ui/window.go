package ui

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

type State int

const (
	StateIdle State = iota
	StateRecording
	StateTranscribing
	StateDone
	StateError
)

type Window struct {
	app    fyne.App
	win    fyne.Window
	label  *widget.Label
	btn    *widget.Button
	OnBtn  func()
}

func New() *Window {
	a := app.New()
	w := a.NewWindow("Voice Input")
	w.Resize(fyne.NewSize(300, 120))

	label := widget.NewLabel("点击按钮开始录音")
	label.Alignment = fyne.TextAlignCenter

	win := &Window{
		app:   a,
		win:   w,
		label: label,
	}

	btn := widget.NewButton("🎤 开始录音", func() {
		if win.OnBtn != nil {
			win.OnBtn()
		}
	})
	win.btn = btn

	w.SetContent(container.NewVBox(
		container.NewCenter(label),
		container.NewCenter(btn),
	))
	w.SetFixedSize(true)

	return win
}

func (w *Window) SetState(state State, msg ...string) {
	text := w.stateText(state, msg...)
	fyne.Do(func() {
		w.label.SetText(text)
		switch state {
		case StateIdle:
			w.btn.SetText("🎤 开始录音")
			w.btn.Enable()
		case StateRecording:
			w.btn.SetText("⏹ 停止录音")
			w.btn.Enable()
		case StateTranscribing:
			w.btn.SetText("⏳ 识别中...")
			w.btn.Disable()
		case StateDone:
			w.btn.SetText("🎤 开始录音")
			w.btn.Enable()
		case StateError:
			w.btn.SetText("🎤 开始录音")
			w.btn.Enable()
		}
	})
}

func (w *Window) SetRecordingDuration(d time.Duration) {
	text := fmt.Sprintf("🎤 录音中... (%02d:%02d)", int(d.Seconds())/60, int(d.Seconds())%60)
	fyne.Do(func() {
		w.label.SetText(text)
	})
}

func (w *Window) stateText(state State, msg ...string) string {
	switch state {
	case StateIdle:
		return "点击按钮开始录音"
	case StateRecording:
		return "🎤 录音中..."
	case StateTranscribing:
		if len(msg) > 0 {
			return "⏳ 识别中... (录音 " + msg[0] + ")"
		}
		return "⏳ 识别中..."
	case StateDone:
		if len(msg) > 0 {
			return "✅ " + msg[0]
		}
		return "✅ 输入完成"
	case StateError:
		if len(msg) > 0 {
			return "❌ " + msg[0]
		}
		return "❌ 识别失败"
	}
	return ""
}

func (w *Window) Run() {
	w.win.ShowAndRun()
}

func (w *Window) Quit() {
	fyne.Do(func() {
		w.app.Quit()
	})
}
