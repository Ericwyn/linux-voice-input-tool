package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"linux-voice-input/audio"
	"linux-voice-input/config"
	"linux-voice-input/doctor"
	"linux-voice-input/engine"
	"linux-voice-input/models"
	"linux-voice-input/session"
	"linux-voice-input/ui"
)

const socketName = "linux-voice-input.sock"

func socketPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = os.TempDir()
	}
	return filepath.Join(runtimeDir, socketName)
}

// sendCommand connects to a running instance (daemon or standalone recorder)
// and sends a single command. It returns true if a listener accepted it.
func sendCommand(sock, command string) bool {
	conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()

	_, _ = io.WriteString(conn, command+"\n")
	return true
}

func listenSocket(sock string) (net.Listener, error) {
	_ = os.Remove(sock)
	listener, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	return listener, nil
}

func logDir() string {
	return config.AppDir()
}

func cleanupOldLogs(dir string) {
	pattern := filepath.Join(dir, "run-*.log")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -2)
	cutoff = time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, cutoff.Location())

	for _, path := range matches {
		base := filepath.Base(path)
		datePart := strings.TrimSuffix(strings.TrimPrefix(base, "run-"), ".log")
		logDate, err := time.ParseInLocation("2006-01-02", datePart, time.Local)
		if err != nil {
			continue
		}
		if logDate.Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

func setupLogging() *os.File {
	dir := logDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("create log dir failed: %v", err)
		return nil
	}

	cleanupOldLogs(dir)

	logPath := filepath.Join(dir, "run-"+time.Now().Format("2006-01-02")+".log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("open log file failed: %v", err)
		return nil
	}

	log.SetOutput(io.MultiWriter(os.Stderr, file))

	linkPath := filepath.Join(dir, "run.log")
	_ = os.Remove(linkPath)
	if err := os.Symlink(filepath.Base(logPath), linkPath); err != nil {
		log.Printf("create log symlink failed: %v", err)
	}

	log.Printf("Logging to %s", logPath)
	return file
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	logFile := setupLogging()
	if logFile != nil {
		defer logFile.Close()
	}

	mode := parseMode(os.Args[1:])

	// Help needs no config or models; print and exit immediately.
	if mode == modeHelp {
		printUsage()
		return
	}

	// For the toggle (no-arg) path, probe for a running instance before
	// loading any config or models: if the daemon (or a standalone recorder)
	// is listening, just forward a toggle and exit immediately.
	if mode == modeToggle {
		if sendCommand(socketPath(), "toggle") {
			log.Println("Forwarded toggle to running instance")
			return
		}
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Config file: %s", config.ConfigPath())
	log.Printf("Engine: %s", cfg.API.Engine)

	// The utility subcommands do not need a working engine, so handle them
	// before the whisper API-key gate below.
	switch mode {
	case modeDownloadModels:
		runDownloadModels(cfg)
		return
	case modeCheckConfig:
		runCheckConfig(cfg)
		return
	}

	if cfg.API.Engine == "sherpa" {
		log.Printf("Sherpa encoder: %s", cfg.Sherpa.Encoder)
		log.Printf("Sherpa threads: %d", cfg.Sherpa.NumThreads)
	} else {
		if cfg.API.Key == "" && mode != modeDaemon {
			fmt.Printf("请先配置 API Key，编辑配置文件: %s\n", config.ConfigPath())
			os.Exit(1)
		}
		log.Printf("API base URL: %s", cfg.API.BaseURL)
		log.Printf("API model: %s", cfg.API.Model)
	}

	switch mode {
	case modeTest:
		runTestMode(cfg)
	case modeDaemon:
		runDaemon(cfg)
	default:
		runStandalone(cfg)
	}
}

// runDownloadModels downloads any missing sherpa model bundles into the
// configured model directory, printing progress to stdout. Shares the same
// entry point (models.DownloadMissing) as the settings window button. Only the
// sherpa engine uses local models; other engines have nothing to download.
func runDownloadModels(cfg *config.Config) {
	if cfg.API.Engine != "sherpa" {
		fmt.Printf("当前引擎为 %q，无需下载本地模型（仅 sherpa 引擎使用本地模型）。\n", cfg.API.Engine)
		return
	}

	baseDir := cfg.Sherpa.ModelDir
	if baseDir == "" {
		baseDir = config.DefaultModelDir()
	}
	fmt.Printf("模型目录: %s\n", baseDir)
	err := models.DownloadMissing(baseDir, func(msg string) {
		fmt.Println(msg)
	})
	if err != nil {
		fmt.Printf("下载失败: %v\n", err)
		os.Exit(1)
	}
}

// runCheckConfig runs the doctor environment/configuration checks and prints a
// report, exiting non-zero if any check failed. Shares the same entry point
// (doctor.Run) as the settings window button.
func runCheckConfig(cfg *config.Config) {
	results := doctor.Run(cfg)
	fmt.Println("配置与环境检查:")
	for _, r := range results {
		mark := "✓"
		if !r.OK {
			mark = "✗"
		}
		fmt.Printf("  [%s] %s: %s\n", mark, r.Name, r.Detail)
	}
	if !doctor.AllOK(results) {
		fmt.Println("\n存在问题，请按上面提示处理。")
		os.Exit(1)
	}
	fmt.Println("\n全部检查通过。")
}

type runMode int

const (
	modeToggle runMode = iota
	modeDaemon
	modeTest
	modeDownloadModels
	modeCheckConfig
	modeHelp
)

func parseMode(args []string) runMode {
	for _, arg := range args {
		switch arg {
		case "-h", "--help":
			return modeHelp
		case "--test":
			return modeTest
		case "--daemon":
			return modeDaemon
		case "--download-models":
			return modeDownloadModels
		case "--check-config":
			return modeCheckConfig
		}
	}
	return modeToggle
}

// printUsage prints the command-line help. It intentionally needs no config so
// it can run before any config/model loading.
func printUsage() {
	fmt.Print(`Linux Voice Input - 面向 Ubuntu/X11 的轻量语音输入工具

用法:
  linux-voice-input [选项]

选项:
  (无参数)            触发录音开关：有守护进程则转发给它，否则独立运行一次
  --daemon           以常驻守护进程运行（系统托盘 + 全局热键 + 设置窗口）
  --test             测试模式：录音 3 秒并识别，用于验证配置是否正确
  --download-models  下载当前引擎所需的模型（仅 sherpa 引擎需要）
  --check-config     检查配置与环境依赖，并打印报告
  -h, --help         显示本帮助信息

配置文件:
  ` + config.ConfigPath() + `
`)
}

// runStandalone reproduces the original one-shot behavior for when no daemon is
// running: the first invocation records and listens on the socket; a second
// invocation forwards a toggle that stops this recording. Models are loaded on
// demand and released when the process exits.
func runStandalone(cfg *config.Config) {
	sock := socketPath()

	listener, err := listenSocket(sock)
	if err != nil {
		log.Fatalf("Failed to create socket: %v", err)
	}
	defer func() {
		listener.Close()
		os.Remove(sock)
	}()

	if err := audio.Init(); err != nil {
		log.Fatalf("Failed to initialize audio: %v", err)
	}
	defer audio.Terminate()

	controller := session.New(cfg)
	defer controller.Close()

	if err := controller.StartRecording(); err != nil {
		log.Fatalf("Failed to start recording: %v", err)
	}

	stopCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		cmd := strings.TrimSpace(string(buf[:n]))
		// Any recognized command (toggle/stop) ends this one-shot recording.
		if cmd == "toggle" || cmd == "stop" {
			close(stopCh)
		}
	}()

	select {
	case <-stopCh:
		log.Println("Stop command received")
	case <-sigCh:
		log.Println("Signal received, stopping")
	}

	controller.StopAndTranscribe()
}

// runDaemon starts the resident background process: it preloads models, listens
// on the socket for forwarded toggles, registers the global hotkey, and runs
// the fyne app with a system tray and settings window.
func runDaemon(cfg *config.Config) {
	sock := socketPath()

	listener, err := listenSocket(sock)
	if err != nil {
		log.Fatalf("Failed to create socket (is a daemon already running?): %v", err)
	}
	defer func() {
		listener.Close()
		os.Remove(sock)
	}()

	if err := audio.Init(); err != nil {
		log.Fatalf("Failed to initialize audio: %v", err)
	}
	defer audio.Terminate()

	controller := session.New(cfg)
	defer controller.Close()
	if err := controller.Preload(); err != nil {
		log.Printf("Model preload failed (will retry lazily): %v", err)
	}

	go acceptToggleLoop(listener, controller)

	app := ui.NewApp(cfg, controller)
	go handleSignals(app)

	app.Run()
	log.Println("Daemon exiting")
}

// acceptToggleLoop serves the daemon's remote-control socket. Each accepted
// connection sends one command; "toggle" drives the shared controller.
func acceptToggleLoop(listener net.Listener, controller *session.Controller) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 64)
			n, _ := c.Read(buf)
			cmd := strings.TrimSpace(string(buf[:n]))
			if cmd == "toggle" || cmd == "stop" {
				controller.Toggle()
			}
		}(conn)
	}
}

func handleSignals(app *ui.App) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("Signal received, quitting daemon")
	app.Quit()
}

func runTestMode(cfg *config.Config) {
	const testDuration = 3 * time.Second

	if err := audio.Init(); err != nil {
		log.Fatalf("Failed to initialize audio: %v", err)
	}
	defer audio.Terminate()

	eng, err := engine.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create engine: %v", err)
	}
	defer eng.Close()

	sess, err := eng.NewSession()
	if err != nil {
		log.Fatalf("Failed to create session: %v", err)
	}
	defer sess.Close()

	recorder := audio.NewRecorder(cfg.Audio.SampleRate)

	fmt.Println("[TEST] 开始录音，持续 3 秒...")
	if err := recorder.Start(); err != nil {
		log.Fatalf("Failed to start recording: %v", err)
	}

	// Feed audio frames to the session while recording. For engines without a
	// streaming stage (e.g. whisper) FeedAudio is a no-op.
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		for samples := range recorder.AudioStream {
			sess.FeedAudio(samples)
		}
	}()

	time.Sleep(testDuration)
	fmt.Println("[TEST] 录音结束")

	if err := recorder.Stop(); err != nil {
		log.Fatalf("Failed to stop recording: %v", err)
	}
	<-feedDone

	// The per-stage timing (streaming/offline/punctuation) is logged inside the
	// sherpa engine's Finish and surfaces here via the stderr MultiWriter.
	fmt.Println("[TEST] 开始识别...")
	startTime := time.Now()
	text, err := sess.Finish(recorder)
	if err != nil {
		log.Fatalf("Transcription failed: %v", err)
	}

	if text == "" {
		fmt.Println("[TEST] 识别结果: (空，未识别到内容)")
	} else {
		fmt.Printf("[TEST] 识别结果 (总耗时 %dms): %s\n", time.Since(startTime).Milliseconds(), text)
	}
}
