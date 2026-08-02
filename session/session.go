// Package session provides a reusable recording/recognition/paste controller
// shared by both the resident daemon (models preloaded once and reused) and
// the standalone one-shot process (models loaded on demand).
package session

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"linux-voice-input/api"
	"linux-voice-input/audio"
	"linux-voice-input/config"
	"linux-voice-input/input"
	"linux-voice-input/notify"
)

// minRecordingDuration guards against accidental double-taps: recordings
// shorter than this are ignored (not transcribed, not pasted).
const minRecordingDuration = time.Second

// toggleDebounce ignores toggle calls that arrive closer together than this.
// X11 keyboard auto-repeat emits KeyPress events roughly every 30-70ms while a
// key is held; without debouncing, a single hotkey press would rapidly fire
// start/stop/start/stop. A human tapping the key twice is always far slower.
const toggleDebounce = 400 * time.Millisecond

// previewNotifyInterval is the minimum gap between live preview notifications
// during recording. Each notification spawns a gdbus process, so updating on
// every audio frame would overwhelm the session bus.
const previewNotifyInterval = 300 * time.Millisecond

// Controller owns the recording lifecycle and recognition engine. All public
// methods are safe for concurrent use.
type Controller struct {
	mu  sync.Mutex
	cfg *config.Config

	// Preloaded engine components for daemon mode; nil when running one-shot.
	preStream  *api.SherpaStreamRecognizer
	preOffline *api.SherpaOfflineRecognizer
	prePunct   *api.SherpaPunctuation

	// Active recording session state.
	recording bool
	recorder  *audio.Recorder

	sessionStream *api.SherpaStream
	// sessionRecognizer is a per-session streaming recognizer, non-nil only in
	// one-shot mode where it must be closed after the session ends.
	sessionRecognizer *api.SherpaStreamRecognizer
	feedDone          chan struct{}

	startTime    time.Time
	targetWindow string

	// lock is the system-wide recording lock held for the current session,
	// preventing any other process from recording concurrently.
	lock *recordLock

	// lastToggle records when Toggle last acted, used to debounce X11
	// keyboard auto-repeat.
	lastToggle time.Time

	onStateChange func(recording bool)
}

// New creates a controller without preloading any models. Suitable for the
// standalone one-shot process where models are loaded lazily per session.
func New(cfg *config.Config) *Controller {
	return &Controller{cfg: cfg}
}

// Preload loads the sherpa models (streaming, offline, punctuation) once so
// that subsequent Toggle calls in the daemon reuse them. It is a no-op for the
// whisper engine, which transcribes via a remote API.
func (c *Controller) Preload() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cfg.API.Engine != "sherpa" {
		return nil
	}
	return c.loadEngineLocked()
}

// loadEngineLocked builds the sherpa recognizers. Caller must hold c.mu.
func (c *Controller) loadEngineLocked() error {
	streamRecognizer, err := api.NewSherpaStreamRecognizer(c.cfg.Sherpa)
	if err != nil {
		return err
	}

	offlineRecognizer, err := api.NewSherpaOfflineRecognizer(c.cfg.Sherpa)
	if err != nil {
		log.Printf("Offline recognizer unavailable at preload: %v", err)
	}

	var punct *api.SherpaPunctuation
	if c.cfg.Sherpa.PunctModel != "" {
		punct, err = api.NewSherpaPunctuation(c.cfg.Sherpa.PunctModel)
		if err != nil {
			log.Printf("Punctuation model unavailable at preload: %v", err)
			punct = nil
		}
	}

	c.preStream = streamRecognizer
	c.preOffline = offlineRecognizer
	c.prePunct = punct
	log.Println("Sherpa engine preloaded for reuse")
	return nil
}

// SetOnStateChange registers a callback invoked whenever the recording state
// changes, used by the tray to update its status item.
func (c *Controller) SetOnStateChange(fn func(recording bool)) {
	c.mu.Lock()
	c.onStateChange = fn
	c.mu.Unlock()
}

// IsRecording reports whether a recording session is currently active.
func (c *Controller) IsRecording() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recording
}

func (c *Controller) notifyStateChange(recording bool) {
	c.mu.Lock()
	fn := c.onStateChange
	c.mu.Unlock()
	if fn != nil {
		fn(recording)
	}
}

// Toggle starts recording when idle, or stops and transcribes when recording.
// Calls arriving within toggleDebounce of the previous accepted toggle are
// ignored to filter out X11 keyboard auto-repeat.
func (c *Controller) Toggle() {
	c.mu.Lock()
	now := time.Now()
	if now.Sub(c.lastToggle) < toggleDebounce {
		c.mu.Unlock()
		return
	}
	c.lastToggle = now
	recording := c.recording
	c.mu.Unlock()

	if recording {
		c.StopAndTranscribe()
	} else {
		if err := c.StartRecording(); err != nil {
			log.Printf("Start recording failed: %v", err)
		}
	}

	// Re-stamp after the (possibly long) transcription completes. The hotkey
	// callback runs synchronously on the X event loop, so any auto-repeat
	// KeyPress events that arrived while transcribing are queued and delivered
	// now; re-stamping keeps them inside the debounce window so they do not
	// spuriously start a new recording.
	c.mu.Lock()
	c.lastToggle = time.Now()
	c.mu.Unlock()
}

// StartRecording begins a new recording session.
func (c *Controller) StartRecording() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.recording {
		return nil
	}

	// Acquire the system-wide recording lock so no other process (another
	// daemon, or a standalone one-shot invocation) can record concurrently.
	lock, err := acquireRecordLock()
	if err != nil {
		notify.FinalIf(c.cfg.Notifications.RecordStartFailed, "语音输入", "已有录音进行中")
		return err
	}

	c.targetWindow = input.GetActiveWindow()
	log.Printf("Target window: %s", c.targetWindow)

	recorder := audio.NewRecorder(c.cfg.Audio.SampleRate)
	if err := recorder.Start(); err != nil {
		lock.release()
		notify.FinalIf(c.cfg.Notifications.RecordStartFailed, "语音输入", "录音启动失败")
		return err
	}

	c.lock = lock
	c.recorder = recorder
	c.recording = true
	c.startTime = time.Now()
	notify.If(c.cfg.Notifications.Start, "语音输入", "录音中", false)

	if c.cfg.API.Engine == "sherpa" {
		recognizer := c.preStream
		if recognizer == nil {
			var err error
			recognizer, err = api.NewSherpaStreamRecognizer(c.cfg.Sherpa)
			if err != nil {
				notify.FinalIf(c.cfg.Notifications.TranscribeFailed, "语音输入", "识别引擎初始化失败")
				// Continue recording anyway; offline model may still transcribe.
				log.Printf("Streaming recognizer init failed: %v", err)
			} else {
				c.sessionRecognizer = recognizer
			}
		}

		if recognizer != nil {
			stream := recognizer.CreateStream()
			c.sessionStream = stream
			done := make(chan struct{})
			c.feedDone = done
			notificationsStart := c.cfg.Notifications.Start
			audioStream := recorder.AudioStream
			go func() {
				defer close(done)
				// Throttle preview notifications: each notify.Persistent spawns
				// a gdbus process, so firing it on every ~64ms audio frame would
				// swamp the session bus. Only update when the text changed and
				// at most every previewNotifyInterval.
				var lastPreview string
				var lastNotifyTime time.Time
				for samples := range audioStream {
					stream.FeedAudio(samples)
					if !notificationsStart {
						continue
					}
					partial := stream.GetPartialResult()
					now := time.Now()
					if partial != "" && partial != lastPreview &&
						now.Sub(lastNotifyTime) >= previewNotifyInterval {
						notify.Persistent("语音输入", "录音中: "+partial)
						lastPreview = partial
						lastNotifyTime = now
					}
				}
			}()
		}
	}

	log.Println("Recording started")
	go c.notifyStateChange(true)
	return nil
}

// StopAndTranscribe stops the active recording, runs recognition, and pastes
// the result into the previously focused window.
func (c *Controller) StopAndTranscribe() {
	c.mu.Lock()
	if !c.recording {
		c.mu.Unlock()
		return
	}

	recorder := c.recorder
	sessionStream := c.sessionStream
	sessionRecognizer := c.sessionRecognizer
	feedDone := c.feedDone
	startTime := c.startTime
	targetWindow := c.targetWindow
	lock := c.lock

	c.recording = false
	c.recorder = nil
	c.sessionStream = nil
	c.sessionRecognizer = nil
	c.feedDone = nil
	c.lock = nil
	c.mu.Unlock()

	go c.notifyStateChange(false)

	defer func() {
		if sessionStream != nil {
			sessionStream.Close()
		}
		if sessionRecognizer != nil {
			sessionRecognizer.Close()
		}
		// Release the system-wide recording lock only after transcription
		// finishes, so a queued toggle cannot start a new recording mid-flight.
		lock.release()
	}()

	if err := recorder.Stop(); err != nil {
		notify.FinalIf(c.cfg.Notifications.RecordStopFailed, "语音输入", "录音停止失败")
		log.Printf("Stop recording failed: %v", err)
		return
	}

	// Wait for the feeding goroutine to drain the closed audio stream so that
	// GetPartialResult/Finish on sessionStream is safe.
	if feedDone != nil {
		<-feedDone
	}

	duration := time.Since(startTime)
	log.Printf("Recording stopped, duration: %.3fs", duration.Seconds())

	if duration < minRecordingDuration {
		log.Printf("Recording ignored, shorter than %.3fs", minRecordingDuration.Seconds())
		notify.FinalIf(c.cfg.Notifications.Ignored, "语音输入", "录音过短，已忽略")
		return
	}

	text := c.transcribe(recorder, sessionStream)

	if text == "" {
		notify.FinalIf(c.cfg.Notifications.EmptyResult, "语音输入", "未识别到内容")
		log.Println("Empty transcription result")
		return
	}

	log.Printf("Transcribed: %s", text)
	if err := input.PasteText(text, c.cfg.RestoreClipboard, targetWindow); err != nil {
		notify.FinalIf(c.cfg.Notifications.PasteFailed, "语音输入", "粘贴失败")
		log.Printf("Paste failed: %v", err)
		return
	}

	notify.FinalIf(c.cfg.Notifications.Success, "语音输入", "已输入："+text)
	log.Println("Done")
}

// transcribe runs the appropriate recognition path and returns the final text.
func (c *Controller) transcribe(recorder *audio.Recorder, sessionStream *api.SherpaStream) string {
	if c.cfg.API.Engine == "sherpa" {
		return c.transcribeSherpa(recorder, sessionStream)
	}
	return c.transcribeWhisper(recorder)
}

func (c *Controller) transcribeSherpa(recorder *audio.Recorder, sessionStream *api.SherpaStream) string {
	var text string

	offline := c.preOffline
	ownedOffline := false
	if offline == nil {
		var err error
		offline, err = api.NewSherpaOfflineRecognizer(c.cfg.Sherpa)
		if err != nil {
			log.Printf("Offline model unavailable, falling back to streaming result: %v", err)
			if sessionStream != nil {
				return sessionStream.Finish()
			}
			return ""
		}
		ownedOffline = true
	}
	if ownedOffline {
		defer offline.Close()
	}

	startTime := time.Now()
	samples := recorder.GetSamplesFloat32()
	text, _ = offline.TranscribeAudioSamples(samples)
	log.Printf("Offline recognition done in %dms: %s", time.Since(startTime).Milliseconds(), text)

	if text == "" || c.cfg.Sherpa.PunctModel == "" {
		return text
	}

	punct := c.prePunct
	ownedPunct := false
	if punct == nil {
		var err error
		punct, err = api.NewSherpaPunctuation(c.cfg.Sherpa.PunctModel)
		if err != nil {
			log.Printf("Punctuation model unavailable: %v", err)
			return text
		}
		ownedPunct = true
	}
	if ownedPunct {
		defer punct.Close()
	}

	startTime = time.Now()
	text = punct.AddPunct(text)
	log.Printf("Punctuation done in %dms: %s", time.Since(startTime).Milliseconds(), text)
	return text
}

func (c *Controller) transcribeWhisper(recorder *audio.Recorder) string {
	notify.If(c.cfg.Notifications.Start, "语音输入", "识别中", false)

	tmpFile := filepath.Join(os.TempDir(), "linux-voice-input.wav")
	if err := recorder.SaveWAV(tmpFile); err != nil {
		notify.FinalIf(c.cfg.Notifications.SaveFailed, "语音输入", "保存录音失败")
		log.Printf("Save WAV failed: %v", err)
		return ""
	}
	defer os.Remove(tmpFile)

	apiClient := api.NewClient(c.cfg.API.BaseURL, c.cfg.API.Key, c.cfg.API.Model)
	text, err := apiClient.Transcribe(tmpFile)
	if err != nil {
		notify.FinalIf(c.cfg.Notifications.TranscribeFailed, "语音输入", "识别失败")
		log.Printf("Transcription failed: %v", err)
		return ""
	}
	return text
}

// Reload swaps the configuration and rebuilds the preloaded engine if needed.
// It is intended to be called from the settings window after saving. It is a
// no-op to reload while a recording is in progress.
func (c *Controller) Reload(cfg *config.Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.recording {
		log.Println("Reload skipped: recording in progress")
		c.cfg = cfg
		return nil
	}

	hadPreload := c.preStream != nil || c.preOffline != nil || c.prePunct != nil
	c.closeEngineLocked()
	c.cfg = cfg

	if hadPreload && cfg.API.Engine == "sherpa" {
		return c.loadEngineLocked()
	}
	return nil
}

// Config returns the current configuration pointer.
func (c *Controller) Config() *config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// closeEngineLocked releases preloaded models. Caller must hold c.mu.
func (c *Controller) closeEngineLocked() {
	if c.preStream != nil {
		c.preStream.Close()
		c.preStream = nil
	}
	if c.preOffline != nil {
		c.preOffline.Close()
		c.preOffline = nil
	}
	if c.prePunct != nil {
		c.prePunct.Close()
		c.prePunct = nil
	}
}

// Close releases all held resources. Safe to call multiple times.
func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeEngineLocked()
}
