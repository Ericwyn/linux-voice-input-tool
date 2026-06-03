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

const socketName = "linux-voice-input.sock"

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

func notify(summary, body string) {
	cmd := exec.Command("notify-send", "--app-name=Linux Voice Input", summary, body)
	if err := cmd.Run(); err != nil {
		log.Printf("notify-send failed: %v", err)
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	sock := socketPath()
	if sendStopIfRunning(sock) {
		notify("语音输入", "停止录音，正在识别...")
		log.Println("Sent stop command to running instance")
		return
	}

	listener, err := listenForStop(sock)
	if err != nil {
		log.Fatalf("Failed to create socket: %v", err)
	}
	defer func() {
		listener.Close()
		os.Remove(sock)
	}()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if cfg.API.Key == "" {
		fmt.Printf("请先配置 API Key，编辑配置文件: %s\n", config.ConfigPath())
		os.Exit(1)
	}

	if err := audio.Init(); err != nil {
		log.Fatalf("Failed to initialize audio: %v", err)
	}
	defer audio.Terminate()

	targetWindow := input.GetActiveWindow()
	log.Printf("Target window: %s", targetWindow)

	apiClient := api.NewClient(cfg.API.BaseURL, cfg.API.Key, cfg.API.Model)
	recorder := audio.NewRecorder(cfg.Audio.SampleRate)

	log.Println("Recording started. Run the command again to stop.")
	if err := recorder.Start(); err != nil {
		notify("语音输入", "录音启动失败")
		log.Fatalf("Failed to start recording: %v", err)
	}
	notify("语音输入", "开始录音，再次触发快捷键停止")
	start := time.Now()

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
		notify("语音输入", "录音停止失败")
		log.Fatalf("Failed to stop recording: %v", err)
	}

	notify("语音输入", fmt.Sprintf("录音 %02d:%02d，正在识别...", int(duration.Seconds())/60, int(duration.Seconds())%60))

	tmpFile := filepath.Join(os.TempDir(), "linux-voice-input.wav")
	log.Println("Saving WAV...")
	if err := recorder.SaveWAV(tmpFile); err != nil {
		notify("语音输入", "保存录音失败")
		log.Fatalf("Failed to save WAV: %v", err)
	}
	defer os.Remove(tmpFile)

	log.Println("Calling transcription API...")
	text, err := apiClient.Transcribe(tmpFile)
	if err != nil {
		notify("语音输入", "识别失败")
		log.Fatalf("Transcription failed: %v", err)
	}

	if text == "" {
		notify("语音输入", "未识别到内容")
		log.Println("Empty transcription result")
		return
	}

	log.Printf("Transcribed: %s", text)
	log.Printf("Pasting to window: %s", targetWindow)

	if err := input.PasteText(text, cfg.RestoreClipboard, targetWindow); err != nil {
		notify("语音输入", "粘贴失败")
		log.Fatalf("Failed to paste: %v", err)
	}

	notify("语音输入", "已输入："+text)
	log.Println("Done")
}
