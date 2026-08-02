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

	"linux-voice-input/api"
	"linux-voice-input/audio"
	"linux-voice-input/config"
	"linux-voice-input/doctor"
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
// entry point (models.DownloadMissing) as the settings window button.
func runDownloadModels(cfg *config.Config) {
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
)

func parseMode(args []string) runMode {
	for _, arg := range args {
		switch arg {
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
	testWavPath := filepath.Join(config.AppDir(), "test-temp.wav")

	if err := audio.Init(); err != nil {
		log.Fatalf("Failed to initialize audio: %v", err)
	}
	defer audio.Terminate()

	recorder := audio.NewRecorder(cfg.Audio.SampleRate)

	fmt.Println("[TEST] 开始录音，持续 3 秒...")
	if err := recorder.Start(); err != nil {
		log.Fatalf("Failed to start recording: %v", err)
	}

	var sherpaStream *api.SherpaStream
	var sherpaRecognizer *api.SherpaStreamRecognizer
	if cfg.API.Engine == "sherpa" {
		var err error
		sherpaRecognizer, err = api.NewSherpaStreamRecognizer(cfg.Sherpa)
		if err != nil {
			log.Fatalf("Failed to create sherpa recognizer: %v", err)
		}
		defer sherpaRecognizer.Close()

		sherpaStream = sherpaRecognizer.CreateStream()
		defer sherpaStream.Close()

		go func() {
			for samples := range recorder.AudioStream {
				sherpaStream.FeedAudio(samples)
			}
		}()
	}

	time.Sleep(testDuration)
	fmt.Println("[TEST] 录音结束")

	if err := recorder.Stop(); err != nil {
		log.Fatalf("Failed to stop recording: %v", err)
	}

	fmt.Printf("[TEST] 保存录音到: %s\n", testWavPath)
	if err := recorder.SaveWAV(testWavPath); err != nil {
		log.Fatalf("Failed to save WAV: %v", err)
	}

	fmt.Println("[TEST] 开始识别...")
	var text string
	if cfg.API.Engine == "sherpa" {
		startTime := time.Now()
		streamingText := sherpaStream.Finish()
		fmt.Printf("[TEST] 流式识别结果 (耗时 %dms): %s\n", time.Since(startTime).Milliseconds(), streamingText)

		fmt.Println("[TEST] 使用离线大模型重新识别...")
		startTime = time.Now()
		offlineRecognizer, offlineErr := api.NewSherpaOfflineRecognizer(cfg.Sherpa)
		if offlineErr != nil {
			fmt.Printf("[TEST] 离线模型不可用，使用流式结果: %v\n", offlineErr)
			text = streamingText
		} else {
			defer offlineRecognizer.Close()
			samples := recorder.GetSamplesFloat32()
			text, _ = offlineRecognizer.TranscribeAudioSamples(samples)
			fmt.Printf("[TEST] 离线模型识别结果 (耗时 %dms): %s\n", time.Since(startTime).Milliseconds(), text)
		}

		if text != "" && cfg.Sherpa.PunctModel != "" {
			fmt.Println("[TEST] 添加标点...")
			startTime = time.Now()
			punct, punctErr := api.NewSherpaPunctuation(cfg.Sherpa.PunctModel)
			if punctErr != nil {
				fmt.Printf("[TEST] 标点模型不可用: %v\n", punctErr)
			} else {
				defer punct.Close()
				text = punct.AddPunct(text)
				fmt.Printf("[TEST] 加标点后 (耗时 %dms): %s\n", time.Since(startTime).Milliseconds(), text)
			}
		}
	} else {
		apiClient := api.NewClient(cfg.API.BaseURL, cfg.API.Key, cfg.API.Model)
		var err error
		text, err = apiClient.Transcribe(testWavPath)
		if err != nil {
			log.Fatalf("Transcription failed: %v", err)
		}
	}

	if text == "" {
		fmt.Println("[TEST] 识别结果: (空，未识别到内容)")
	} else {
		fmt.Printf("[TEST] 识别结果: %s\n", text)
	}
}
