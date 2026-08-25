// Package session provides a reusable recording/recognition/paste controller
// shared by both the resident daemon (models preloaded once and reused) and
// the standalone one-shot process (models loaded on demand). It is agnostic to
// the recognition engine, delegating all recognition work to engine.Engine and
// engine.Session.
package session

import (
	"context"
	"log"
	"sync"
	"time"

	"linux-voice-input/audio"
	"linux-voice-input/config"
	"linux-voice-input/corrector"
	"linux-voice-input/engine"
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
// during recording. Each notification is a D-Bus call to the notification
// server, so updating on every audio frame would overwhelm the session bus.
const previewNotifyInterval = 300 * time.Millisecond

// Controller owns the recording lifecycle and drives the recognition engine.
// All public methods are safe for concurrent use.
type Controller struct {
	mu  sync.Mutex
	cfg *config.Config

	// engine is the recognition strategy selected by config. It may be nil if
	// the configured engine name is unknown; the recording path guards for it.
	engine engine.Engine
	// preloaded records whether Preload succeeded, so Reload knows whether to
	// re-preload the rebuilt engine.
	preloaded bool

	// correctionService owns the optional neural model and final user
	// dictionary. It is engine-agnostic and runs exactly once after Finish.
	correctionService *corrector.Service

	// Active recording session state.
	recording bool
	recorder  *audio.Recorder

	// activeSession is the recognition session for the current recording,
	// non-nil only while recording.
	activeSession engine.Session
	// feedDone is closed when the audio-feeding goroutine drains the recorder's
	// stream after recording stops.
	feedDone chan struct{}

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

// New creates a controller and builds the recognition engine from config
// without preloading any heavy resources. Suitable for the standalone one-shot
// process where models are loaded lazily per session. An unknown engine name
// is logged and leaves the engine nil; recording will then report an init
// failure rather than crashing.
func New(cfg *config.Config) *Controller {
	c := &Controller{cfg: cfg}
	eng, err := engine.New(cfg)
	if err != nil {
		log.Printf("Engine init failed: %v", err)
	}
	c.engine = eng
	c.correctionService = newCorrectionService(cfg)
	return c
}

// newCorrectionService builds the configured model+dictionary pipeline. An
// invalid model name degrades to dictionary-only instead of disabling all
// user corrections.
func newCorrectionService(cfg *config.Config) *corrector.Service {
	service, err := corrector.NewServiceFromConfig(cfg)
	if err != nil {
		log.Printf("Correction model configuration unavailable, using dictionary only: %v", err)
		fallback := *cfg
		fallback.Corrections.Strategy = ""
		service, _ = corrector.NewServiceFromConfig(&fallback)
	}
	if cfg.Corrections.Enabled {
		log.Printf("Text corrections enabled: strategy=%q dictionary=%s",
			cfg.Corrections.Strategy, cfg.CorrectionsDictPath())
	}
	return service
}

// CorrectionsEnabled reports the live service state.
func (c *Controller) CorrectionsEnabled() bool {
	c.mu.Lock()
	service := c.correctionService
	c.mu.Unlock()
	return service != nil && service.Enabled()
}

// SetCorrectionsEnabled changes only the post-processing service. It does not
// rebuild or reload the ASR engine, so it is safe for the tray checkbox.
func (c *Controller) SetCorrectionsEnabled(enabled bool) {
	c.mu.Lock()
	c.cfg.Corrections.Enabled = enabled
	service := c.correctionService
	c.mu.Unlock()
	if service != nil {
		service.SetEnabled(enabled)
	}
}

// Preload eagerly loads the engine's heavy resources once so that subsequent
// Toggle calls in the daemon reuse them. For engines that need no preloading
// (e.g. whisper) it is a no-op inside the engine.
func (c *Controller) Preload() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.engine == nil {
		return nil
	}
	if err := c.engine.Preload(); err != nil {
		return err
	}
	c.preloaded = true
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

	if c.engine != nil {
		session, err := c.engine.NewSession()
		if err != nil {
			notify.FinalIf(c.cfg.Notifications.TranscribeFailed, "语音输入", "识别引擎初始化失败")
			// Continue recording anyway; the session may still transcribe.
			log.Printf("Recognition session init failed: %v", err)
		} else {
			c.activeSession = session
			c.startFeedLoop(recorder, session)
		}
	}

	log.Println("Recording started")
	go c.notifyStateChange(true)
	return nil
}

// startFeedLoop launches the goroutine that feeds recorded audio frames to the
// session and, for engines that support it, emits throttled live-preview
// notifications. Caller must hold c.mu.
func (c *Controller) startFeedLoop(recorder *audio.Recorder, session engine.Session) {
	done := make(chan struct{})
	c.feedDone = done

	supportsPreview := c.engine.SupportsPreview()
	notificationsStart := c.cfg.Notifications.Start
	audioStream := recorder.AudioStream

	go func() {
		defer close(done)
		// Throttle preview notifications: each notify.Persistent is a D-Bus call
		// to the notification server, so firing it on every ~64ms audio frame
		// would swamp the session bus. Only update when the text changed and at
		// most every previewNotifyInterval.
		var lastPreview string
		var lastNotifyTime time.Time
		for samples := range audioStream {
			session.FeedAudio(samples)
			if !notificationsStart || !supportsPreview {
				continue
			}
			partial := session.PartialText()
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

// StopAndTranscribe stops the active recording, runs recognition, and pastes
// the result into the previously focused window.
func (c *Controller) StopAndTranscribe() {
	c.mu.Lock()
	if !c.recording {
		c.mu.Unlock()
		return
	}

	recorder := c.recorder
	activeSession := c.activeSession
	feedDone := c.feedDone
	startTime := c.startTime
	targetWindow := c.targetWindow
	lock := c.lock

	c.recording = false
	c.recorder = nil
	c.activeSession = nil
	c.feedDone = nil
	c.lock = nil
	c.mu.Unlock()

	go c.notifyStateChange(false)

	// Finally-style guarantee: whichever path ends this recording — success,
	// any error return, or even a panic — the resident (never-expiring) live
	// preview bubble is torn down so it can never linger on screen. It is a
	// no-op once a result/error notification has already replaced the preview,
	// so it only fires when nothing else closed it (notably the silent success
	// path below, which shows no result notification of its own).
	defer notify.EndPreview()

	defer func() {
		if activeSession != nil {
			activeSession.Close()
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
	// PartialText/Finish on the session is safe.
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

	text := c.transcribe(recorder, activeSession)

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

	// On success the preview already showed the text and it is now in the input
	// box, so tear the preview down silently; engines without a preview still
	// get a confirmation notification when enabled.
	notify.FinalSuccess(c.cfg.Notifications.Success, "语音输入", "已输入："+text)
	log.Println("Done")
}

// Cancel aborts the active recording without transcribing or pasting: it stops
// the recorder, drains and closes the recognition session, releases the
// system-wide recording lock, and tears down the live-preview bubble silently
// (no result/error notification). It mirrors StopAndTranscribe's teardown so no
// resource leaks, but deliberately skips recognition and paste. It is a no-op
// when no recording is in progress, and is safe to call from the tray or from a
// notification "取消" action.
func (c *Controller) Cancel() {
	c.mu.Lock()
	if !c.recording {
		c.mu.Unlock()
		return
	}

	recorder := c.recorder
	activeSession := c.activeSession
	feedDone := c.feedDone
	lock := c.lock

	c.recording = false
	c.recorder = nil
	c.activeSession = nil
	c.feedDone = nil
	c.lock = nil
	c.mu.Unlock()

	go c.notifyStateChange(false)

	// Silently drop the live-preview bubble; a cancel shows no result bubble.
	notify.EndPreview()

	if err := recorder.Stop(); err != nil {
		log.Printf("Cancel: stop recording failed: %v", err)
	}
	// Drain the feeding goroutine before closing the session so the closed
	// audio stream is fully consumed and Close is safe.
	if feedDone != nil {
		<-feedDone
	}
	if activeSession != nil {
		activeSession.Close()
	}
	lock.release()

	log.Println("Recording cancelled")
}

// transcribe drives the session to produce the final text. For engines without
// live preview (e.g. whisper) it emits a "recognizing" notification first,
// since there was no preview during recording. A recognition error is reported
// once here and yields empty text.
func (c *Controller) transcribe(recorder *audio.Recorder, activeSession engine.Session) string {
	if activeSession == nil {
		return ""
	}

	if c.engine != nil && !c.engine.SupportsPreview() {
		notify.If(c.cfg.Notifications.Start, "语音输入", "识别中", false)
	}

	text, err := activeSession.Finish(recorder)
	if err != nil {
		notify.FinalIf(c.cfg.Notifications.TranscribeFailed, "语音输入", "识别失败")
		log.Printf("Transcription failed: %v", err)
		return ""
	}

	return c.applyCorrections(text)
}

// applyCorrections runs the engine-agnostic final-text service with a bounded
// context. ONNX calls cannot be force-cancelled mid-kernel; model strategies
// observe the deadline before/after Run, while the cheap final dictionary is
// still allowed to execute as a deterministic fallback.
func (c *Controller) applyCorrections(text string) string {
	c.mu.Lock()
	service := c.correctionService
	timeoutMs := c.cfg.Corrections.TimeoutMs
	c.mu.Unlock()
	if service == nil || !service.Enabled() || text == "" {
		return text
	}

	ctx := context.Background()
	cancel := func() {}
	if timeoutMs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	}
	result := service.Correct(ctx, text)
	cancel()
	for _, failure := range result.Failures {
		log.Printf("Correction strategy %s failed: %v", failure.Strategy, failure.Err)
	}
	if result.Text != text {
		log.Printf("Corrected in %dms via %v: %s -> %s",
			result.Duration.Milliseconds(), result.Applied, text, result.Text)
	}
	return result.Text
}

// Reload swaps the configuration and rebuilds the engine if needed. It is
// intended to be called from the settings window after saving. It is a no-op
// to reload while a recording is in progress (only the config pointer is
// updated).
func (c *Controller) Reload(cfg *config.Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.recording {
		log.Println("Reload skipped: recording in progress")
		c.cfg = cfg
		return nil
	}

	wasPreloaded := c.preloaded
	if c.engine != nil {
		c.engine.Close()
	}
	c.preloaded = false
	c.cfg = cfg

	// Rebuild the correction pipeline so model, threshold and dictionary changes
	// take effect. The heavy ONNX session remains lazy until first use.
	if c.correctionService != nil {
		_ = c.correctionService.Close()
	}
	c.correctionService = newCorrectionService(cfg)

	eng, err := engine.New(cfg)
	if err != nil {
		log.Printf("Engine init failed on reload: %v", err)
		c.engine = nil
		return err
	}
	c.engine = eng

	if wasPreloaded {
		if err := c.engine.Preload(); err != nil {
			return err
		}
		c.preloaded = true
	}
	return nil
}

// Config returns the current configuration pointer.
func (c *Controller) Config() *config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// Close releases all held resources. Safe to call multiple times.
func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.engine != nil {
		c.engine.Close()
	}
	if c.correctionService != nil {
		_ = c.correctionService.Close()
		c.correctionService = nil
	}
	c.preloaded = false
}
