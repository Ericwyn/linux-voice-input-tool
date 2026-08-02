// Package doctor performs environment and configuration checks shared by both
// the --check-config CLI command and the settings window's "环境检查" button.
package doctor

import (
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"

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

	results = append(results, checkNotificationService())

	return results
}

// checkNotificationService probes the session bus for a running
// org.freedesktop.Notifications server. Notifications are now spoken directly
// over D-Bus (no gdbus / notify-send binary required), so the meaningful check
// is whether a spec-compliant notification server is actually available.
func checkNotificationService() Result {
	conn, err := dbus.SessionBus()
	if err != nil {
		return Result{Name: "桌面通知", OK: false,
			Detail: fmt.Sprintf("无法连接会话 D-Bus：%v", err)}
	}

	var owner string
	call := conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0,
		"org.freedesktop.Notifications")
	if call.Err != nil || call.Store(&owner) != nil || owner == "" {
		return Result{Name: "桌面通知", OK: false,
			Detail: "未检测到通知服务（org.freedesktop.Notifications）；请确认桌面通知守护进程正在运行"}
	}
	return Result{Name: "桌面通知", OK: true,
		Detail: "D-Bus 通知服务可用"}
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

	// Local engines (sherpa / sensevoice) both link the sherpa-onnx C library
	// and load model files. Check the native library first: without it the
	// engine cannot run at all, so it is the more fundamental dependency and is
	// reported ahead of the models.
	var results []Result
	results = append(results, checkSherpaLibrary())

	label := "sherpa 模型"
	if cfg.Engine == "sensevoice" {
		label = "SenseVoice 模型"
	}
	baseDir := cfg.ModelBaseDir()
	missing := models.Check(baseDir, models.ForEngine(cfg.Engine))
	if len(missing) == 0 {
		results = append(results, Result{Name: label, OK: true,
			Detail: fmt.Sprintf("全部就绪（%s）", baseDir)})
		return results
	}
	detail := fmt.Sprintf("缺失 %d 个文件（运行 --download-models 或在设置中下载）:", len(missing))
	for _, item := range missing {
		detail += fmt.Sprintf("\n    - %s: %s", item.Model, item.Path)
	}
	results = append(results, Result{Name: label, OK: false, Detail: detail})
	return results
}

// checkSherpaLibrary verifies that the sherpa-onnx native shared libraries the
// local engines depend on can be resolved. Those libraries (libsherpa-onnx-c-api
// and libonnxruntime) are linked as hard NEEDED dependencies of this binary via
// CGO, with an rpath baked in at build time that points into the Go module
// cache. It reads its own ELF dynamic section to discover exactly which
// sherpa-onnx libraries are required and where the linker is told to find them,
// then resolves each one the way the dynamic linker would. This gives users a
// visible, resolved library path in the report and a clear diagnostic in the
// edge cases where the library is unresolved (e.g. a binary copied to a machine
// whose module-cache rpath no longer exists).
func checkSherpaLibrary() Result {
	const name = "sherpa-onnx 库"

	exePath, err := os.Executable()
	if err != nil {
		return Result{Name: name, OK: false,
			Detail: fmt.Sprintf("无法定位当前可执行文件：%v", err)}
	}

	file, err := elf.Open(exePath)
	if err != nil {
		return Result{Name: name, OK: false,
			Detail: fmt.Sprintf("无法解析可执行文件的动态段：%v", err)}
	}
	defer file.Close()

	imported, err := file.ImportedLibraries()
	if err != nil {
		return Result{Name: name, OK: false,
			Detail: fmt.Sprintf("无法读取动态依赖列表：%v", err)}
	}

	var neededSherpaLibs []string
	for _, lib := range imported {
		if strings.Contains(lib, "sherpa-onnx") || strings.Contains(lib, "onnxruntime") {
			neededSherpaLibs = append(neededSherpaLibs, lib)
		}
	}
	if len(neededSherpaLibs) == 0 {
		return Result{Name: name, OK: false,
			Detail: "本二进制未链接 sherpa-onnx 库，本地引擎无法运行（可能是使用了不含 CGO 的构建）"}
	}

	searchDirs := sharedLibrarySearchDirs(file, exePath)

	var resolvedDirs []string
	var unresolved []string
	for _, lib := range neededSherpaLibs {
		if dir := resolveSharedLibrary(lib, searchDirs); dir != "" {
			resolvedDirs = appendUnique(resolvedDirs, dir)
		} else {
			unresolved = append(unresolved, lib)
		}
	}

	if len(unresolved) > 0 {
		detail := fmt.Sprintf("缺少 %d 个 sherpa-onnx 库，本地引擎（sherpa / sensevoice）无法运行：", len(unresolved))
		for _, lib := range unresolved {
			detail += fmt.Sprintf("\n    - %s", lib)
		}
		detail += "\n  请通过源码重新编译（go build 会自动引入该库），或改用 whisper 在线引擎。"
		return Result{Name: name, OK: false, Detail: detail}
	}

	return Result{Name: name, OK: true,
		Detail: fmt.Sprintf("全部就绪（%s）", strings.Join(resolvedDirs, ", "))}
}

// sharedLibrarySearchDirs returns the directories to search for a NEEDED shared
// library. It reads the executable's DT_RPATH / DT_RUNPATH dynamic entries and
// delegates the ordering and $ORIGIN expansion to composeSearchDirs (kept as a
// separate pure function so the path logic is unit-testable without fabricating
// an ELF).
func sharedLibrarySearchDirs(file *elf.File, exePath string) []string {
	runpaths, _ := file.DynString(elf.DT_RUNPATH)
	rpaths, _ := file.DynString(elf.DT_RPATH)
	return composeSearchDirs(runpaths, rpaths, filepath.Dir(exePath), os.Getenv("LD_LIBRARY_PATH"))
}

// composeSearchDirs builds the shared-library search directories in a pragmatic
// approximation of the dynamic linker's own order: DT_RPATH (only honored when
// no DT_RUNPATH is present), then LD_LIBRARY_PATH, then DT_RUNPATH, then the
// common system library directories. Each rpath/runpath value may itself be a
// colon-separated list, and $ORIGIN (or ${ORIGIN}) in an entry is expanded to
// exeDir, matching the loader's behavior. ld.so.cache is not parsed; the
// sherpa-onnx libraries live in the baked-in rpath, so the listed sources cover
// the real cases.
func composeSearchDirs(runpaths, rpaths []string, exeDir, ldLibraryPath string) []string {
	expandOrigin := func(entry string) string {
		return strings.ReplaceAll(strings.ReplaceAll(entry, "${ORIGIN}", exeDir), "$ORIGIN", exeDir)
	}
	splitEntries := func(value string) []string {
		var dirs []string
		for _, entry := range strings.Split(value, ":") {
			entry = strings.TrimSpace(entry)
			if entry != "" {
				dirs = append(dirs, expandOrigin(entry))
			}
		}
		return dirs
	}

	var dirs []string

	// DT_RPATH is consulted only when DT_RUNPATH is absent.
	if len(runpaths) == 0 {
		for _, value := range rpaths {
			dirs = append(dirs, splitEntries(value)...)
		}
	}

	dirs = append(dirs, splitEntries(ldLibraryPath)...)

	for _, value := range runpaths {
		dirs = append(dirs, splitEntries(value)...)
	}

	dirs = append(dirs,
		"/lib/x86_64-linux-gnu",
		"/usr/lib/x86_64-linux-gnu",
		"/lib",
		"/usr/lib",
		"/usr/local/lib",
	)

	return dirs
}

// resolveSharedLibrary returns the directory in which libName is found among the
// given search directories, or an empty string if it is not present in any of
// them.
func resolveSharedLibrary(libName string, searchDirs []string) string {
	for _, dir := range searchDirs {
		candidate := filepath.Join(dir, libName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return dir
		}
	}
	return ""
}

// appendUnique appends value to slice only if it is not already present.
func appendUnique(slice []string, value string) []string {
	for _, existing := range slice {
		if existing == value {
			return slice
		}
	}
	return append(slice, value)
}
