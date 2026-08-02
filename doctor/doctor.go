// Package doctor performs environment and configuration checks shared by both
// the --check-config CLI command and the settings window's "环境检查" button.
package doctor

import (
	"fmt"
	"os"
	"os/exec"

	"linux-voice-input/config"
	"linux-voice-input/models"
)

// Result is the outcome of a single check.
type Result struct {
	Name   string
	OK     bool
	Detail string
}

// Run executes all checks against the given config and returns their results
// in display order.
func Run(cfg *config.Config) []Result {
	var results []Result

	results = append(results, checkDisplayServer())
	results = append(results, checkCommands()...)
	results = append(results, checkConfigFile())
	results = append(results, checkEngine(cfg)...)

	return results
}

// AllOK reports whether every result passed.
func AllOK(results []Result) bool {
	for _, r := range results {
		if !r.OK {
			return false
		}
	}
	return true
}

func checkDisplayServer() Result {
	sessionType := os.Getenv("XDG_SESSION_TYPE")
	display := os.Getenv("DISPLAY")
	switch {
	case sessionType == "x11" || (sessionType == "" && display != ""):
		return Result{Name: "显示服务器", OK: true, Detail: "X11"}
	case sessionType == "wayland":
		return Result{Name: "显示服务器", OK: false,
			Detail: "检测到 Wayland；xclip/xdotool 粘贴可能不可用，建议使用 X11 会话"}
	default:
		return Result{Name: "显示服务器", OK: false,
			Detail: fmt.Sprintf("无法确定（XDG_SESSION_TYPE=%q, DISPLAY=%q）", sessionType, display)}
	}
}

func checkCommands() []Result {
	var results []Result

	required := []struct{ cmd, purpose string }{
		{"xclip", "写入剪贴板"},
		{"xdotool", "聚焦窗口并模拟 Ctrl+V"},
	}
	for _, item := range required {
		if path, err := exec.LookPath(item.cmd); err == nil {
			results = append(results, Result{Name: item.cmd, OK: true, Detail: path})
		} else {
			results = append(results, Result{Name: item.cmd, OK: false,
				Detail: fmt.Sprintf("未安装（%s）；请安装后重试", item.purpose)})
		}
	}

	// Notifications need at least one of gdbus or notify-send.
	_, gdbusErr := exec.LookPath("gdbus")
	_, notifyErr := exec.LookPath("notify-send")
	if gdbusErr == nil || notifyErr == nil {
		results = append(results, Result{Name: "桌面通知", OK: true,
			Detail: "gdbus/notify-send 可用"})
	} else {
		results = append(results, Result{Name: "桌面通知", OK: false,
			Detail: "gdbus 和 notify-send 均未找到；通知将不可用"})
	}

	return results
}

func checkConfigFile() Result {
	path := config.ConfigPath()
	if _, err := os.Stat(path); err != nil {
		return Result{Name: "配置文件", OK: false,
			Detail: fmt.Sprintf("%s 不存在（首次运行会自动创建）", path)}
	}
	return Result{Name: "配置文件", OK: true, Detail: path}
}

func checkEngine(cfg *config.Config) []Result {
	if cfg.Engine == "whisper" {
		if cfg.Whisper.Key == "" {
			return []Result{{Name: "whisper 引擎", OK: false,
				Detail: "未配置 API Key，在线识别无法工作"}}
		}
		return []Result{{Name: "whisper 引擎", OK: true,
			Detail: "API Key 已配置"}}
	}

	// Local engines (sherpa / sensevoice): verify their model files exist.
	label := "sherpa 模型"
	if cfg.Engine == "sensevoice" {
		label = "SenseVoice 模型"
	}
	baseDir := cfg.ModelBaseDir()
	missing := models.Check(baseDir, models.ForEngine(cfg.Engine))
	if len(missing) == 0 {
		return []Result{{Name: label, OK: true,
			Detail: fmt.Sprintf("全部就绪（%s）", baseDir)}}
	}
	detail := fmt.Sprintf("缺失 %d 个文件（运行 --download-models 或在设置中下载）:", len(missing))
	for _, item := range missing {
		detail += fmt.Sprintf("\n    - %s: %s", item.Model, item.Path)
	}
	return []Result{{Name: label, OK: false, Detail: detail}}
}
