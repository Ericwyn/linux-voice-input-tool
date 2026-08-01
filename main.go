package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"linux-voice-input/api"
	"linux-voice-input/audio"
	"linux-voice-input/config"
	"linux-voice-input/input"
)

const (
	socketName           = "linux-voice-input.sock"
	minRecordingDuration = time.Second
)

func socketPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = os.TempDir()
	}
	return filepath.Join(runtimeDir, socketName)
}

func sendStopIfRunning(sock string) bool {
	conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()

	_, _ = io.WriteString(conn, "stop\n")
	return true
}

func listenForStop(sock string) (net.Listener, error) {
	_ = os.Remove(sock)
	listener, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	return listener, nil
}

func notifyIDPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = os.TempDir()
	}
	return filepath.Join(runtimeDir, "linux-voice-input.notify-id")
}

func readNotifyID() string {
	data, err := os.ReadFile(notifyIDPath())
	if err != nil {
		return "0"
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "0"
	}
	return id
}

func writeNotifyID(id string) {
	if id == "" || id == "0" {
		return
	}
	if err := os.WriteFile(notifyIDPath(), []byte(id), 0644); err != nil {
		log.Printf("write notification id failed: %v", err)
	}
}

func notify(summary, body string, final bool) {
	id, err := notifyDBus(summary, body, readNotifyID())
	if err != nil {
		log.Printf("dbus notify failed: %v", err)
		cmd := exec.Command("notify-send", "--app-name=Linux Voice Input", "--expire-time=3000", "--hint=boolean:transient:true", summary, body)
		if err := cmd.Run(); err != nil {
			log.Printf("notify-send failed: %v", err)
		}
		return
	}

	writeNotifyID(id)
	if final {
		closeNotificationLater(id)
	}
}

func notifyIf(enabled bool, summary, body string, final bool) {
	if enabled {
		notify(summary, body, final)
	}
}

func notifyFinalIf(enabled bool, summary, body string) {
	if enabled {
		notify(summary, body, true)
		return
	}
	closeCurrentNotificationLater()
}

func notifyPersistent(summary, body string) {
	id, err := notifyDBusPersistent(summary, body, readNotifyID())
	if err != nil {
		log.Printf("dbus persistent notify failed: %v", err)
		return
	}
	writeNotifyID(id)
}

func closeCurrentNotificationLater() {
	id := readNotifyID()
	if id != "0" {
		closeNotificationLater(id)
	}
}

func closeNotificationLater(id string) {
	cmd := exec.Command(
		"sh", "-c",
		"sleep 3; gdbus call --session --dest org.freedesktop.Notifications --object-path /org/freedesktop/Notifications --method org.freedesktop.Notifications.CloseNotification \"$1\" >/dev/null 2>&1; rm -f \"$2\"",
		"sh", id, notifyIDPath(),
	)
	if err := cmd.Start(); err != nil {
		log.Printf("schedule close notification failed: %v", err)
	}
}

func notifyDBus(summary, body, replacesID string) (string, error) {
	return notifyDBusWithTimeout(summary, body, replacesID, "3000")
}

func notifyDBusPersistent(summary, body, replacesID string) (string, error) {
	out, err := exec.Command(
		"gdbus", "call",
		"--session",
		"--dest", "org.freedesktop.Notifications",
		"--object-path", "/org/freedesktop/Notifications",
		"--method", "org.freedesktop.Notifications.Notify",
		"Linux Voice Input",
		replacesID,
		"",
		summary,
		body,
		"[]",
		"{'urgency': <byte 2>, 'resident': <true>}",
		"0",
	).Output()
	if err != nil {
		return "", err
	}

	result := strings.TrimSpace(string(out))
	id := strings.Trim(result, "(), ")
	id = strings.TrimPrefix(id, "uint32 ")
	if id == "" {
		return "", fmt.Errorf("empty notification id from %q", result)
	}
	return id, nil
}

func notifyDBusWithTimeout(summary, body, replacesID, timeoutMs string) (string, error) {
	out, err := exec.Command(
		"gdbus", "call",
		"--session",
		"--dest", "org.freedesktop.Notifications",
		"--object-path", "/org/freedesktop/Notifications",
		"--method", "org.freedesktop.Notifications.Notify",
		"Linux Voice Input",
		replacesID,
		"",
		summary,
		body,
		"[]",
		"{'transient': <true>}",
		timeoutMs,
	).Output()
	if err != nil {
		return "", err
	}

	result := strings.TrimSpace(string(out))
	id := strings.Trim(result, "(), ")
	id = strings.TrimPrefix(id, "uint32 ")
	if id == "" {
		return "", fmt.Errorf("empty notification id from %q", result)
	}
	return id, nil
}

func closeNotification(id string) error {
	return exec.Command(
		"gdbus", "call",
		"--session",
		"--dest", "org.freedesktop.Notifications",
		"--object-path", "/org/freedesktop/Notifications",
		"--method", "org.freedesktop.Notifications.CloseNotification",
		id,
	).Run()
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

	testMode := len(os.Args) > 1 && os.Args[1] == "--test"

	if !testMode {
		sock := socketPath()
		if sendStopIfRunning(sock) {
			log.Println("Sent stop command to running instance")
			return
		}
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Config file: %s", config.ConfigPath())
	log.Printf("Engine: %s", cfg.API.Engine)
	if cfg.API.Engine == "sherpa" {
		log.Printf("Sherpa encoder: %s", cfg.Sherpa.Encoder)
		log.Printf("Sherpa decoder: %s", cfg.Sherpa.Decoder)
		log.Printf("Sherpa tokens: %s", cfg.Sherpa.Tokens)
		log.Printf("Sherpa threads: %d", cfg.Sherpa.NumThreads)
	} else {
		if cfg.API.Key == "" {
			fmt.Printf("请先配置 API Key，编辑配置文件: %s\n", config.ConfigPath())
			os.Exit(1)
		}
		log.Printf("API base URL: %s", cfg.API.BaseURL)
		log.Printf("API model: %s", cfg.API.Model)
	}

	if testMode {
		runTestMode(cfg)
		return
	}

	runNormalMode(cfg)
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
		text = sherpaStream.Finish()
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

func runNormalMode(cfg *config.Config) {
	sock := socketPath()

	listener, err := listenForStop(sock)
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

	targetWindow := input.GetActiveWindow()
	log.Printf("Target window: %s", targetWindow)

	recorder := audio.NewRecorder(cfg.Audio.SampleRate)

	log.Println("Recording started. Run the command again to stop.")
	if err := recorder.Start(); err != nil {
		notifyFinalIf(cfg.Notifications.RecordStartFailed, "语音输入", "录音启动失败")
		log.Fatalf("Failed to start recording: %v", err)
	}
	notifyIf(cfg.Notifications.Start, "语音输入", "录音中", false)
	start := time.Now()

	// Start streaming recognition in background if using sherpa engine
	var sherpaStream *api.SherpaStream
	var sherpaRecognizer *api.SherpaStreamRecognizer
	if cfg.API.Engine == "sherpa" {
		sherpaRecognizer, err = api.NewSherpaStreamRecognizer(cfg.Sherpa)
		if err != nil {
			notifyFinalIf(cfg.Notifications.TranscribeFailed, "语音输入", "识别引擎初始化失败")
			log.Fatalf("Failed to create sherpa recognizer: %v", err)
		}
		defer sherpaRecognizer.Close()

		sherpaStream = sherpaRecognizer.CreateStream()
		defer sherpaStream.Close()

		go func() {
			for samples := range recorder.AudioStream {
				sherpaStream.FeedAudio(samples)
				partialText := sherpaStream.GetPartialResult()
				if partialText != "" && cfg.Notifications.Start {
					notifyPersistent("语音输入", "录音中: "+partialText)
				}
			}
		}()
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
		if cmd == "stop" {
			close(stopCh)
		}
	}()

	select {
	case <-stopCh:
		log.Println("Stop command received")
	case <-sigCh:
		log.Println("Signal received, stopping")
	}

	duration := time.Since(start)
	log.Printf("Recording stopped, duration: %02d:%02d", int(duration.Seconds())/60, int(duration.Seconds())%60)

	log.Println("Stopping audio stream...")
	if err := recorder.Stop(); err != nil {
		notifyFinalIf(cfg.Notifications.RecordStopFailed, "语音输入", "录音停止失败")
		log.Fatalf("Failed to stop recording: %v", err)
	}

	if duration < minRecordingDuration {
		log.Printf("Recording ignored because duration %.3fs is shorter than %.3fs", duration.Seconds(), minRecordingDuration.Seconds())
		notifyFinalIf(cfg.Notifications.Ignored, "语音输入", "录音过短，已忽略")
		return
	}

	var text string
	if cfg.API.Engine == "sherpa" {
		log.Println("Finalizing streaming recognition...")
		text = sherpaStream.Finish()
	} else {
		notifyIf(cfg.Notifications.Start, "语音输入", "识别中", false)

		tmpFile := filepath.Join(os.TempDir(), "linux-voice-input.wav")
		log.Println("Saving WAV...")
		if err := recorder.SaveWAV(tmpFile); err != nil {
			notifyFinalIf(cfg.Notifications.SaveFailed, "语音输入", "保存录音失败")
			log.Fatalf("Failed to save WAV: %v", err)
		}
		defer os.Remove(tmpFile)

		log.Println("Calling transcription API...")
		apiClient := api.NewClient(cfg.API.BaseURL, cfg.API.Key, cfg.API.Model)
		text, err = apiClient.Transcribe(tmpFile)
		if err != nil {
			notifyFinalIf(cfg.Notifications.TranscribeFailed, "语音输入", "识别失败")
			log.Fatalf("Transcription failed: %v", err)
		}
	}

	if text == "" {
		notifyFinalIf(cfg.Notifications.EmptyResult, "语音输入", "未识别到内容")
		log.Println("Empty transcription result")
		return
	}

	log.Printf("Transcribed: %s", text)
	log.Printf("Pasting to window: %s", targetWindow)

	if err := input.PasteText(text, cfg.RestoreClipboard, targetWindow); err != nil {
		notifyFinalIf(cfg.Notifications.PasteFailed, "语音输入", "粘贴失败")
		log.Fatalf("Failed to paste: %v", err)
	}

	notifyFinalIf(cfg.Notifications.Success, "语音输入", "已输入："+text)
	log.Println("Done")
}
