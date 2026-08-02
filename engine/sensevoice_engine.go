package engine

import (
	"log"
	"sync"
	"time"

	"linux-voice-input/api"
	"linux-voice-input/audio"
	"linux-voice-input/config"
)

// senseVoiceEngine owns a single preloaded SenseVoice recognizer. SenseVoice is
// a non-streaming model, so unlike sherpa there is no separate streaming model:
// one recognizer both produces the live preview (by periodically re-running on
// the audio accumulated so far) and the final transcription. In daemon mode the
// recognizer is loaded once via Preload and reused; in one-shot mode it stays
// nil and each session lazily builds and owns its own.
type senseVoiceEngine struct {
	cfg *config.Config
	mu  sync.Mutex

	pre *api.SenseVoiceRecognizer
}

func newSenseVoiceEngine(cfg *config.Config) *senseVoiceEngine {
	return &senseVoiceEngine{cfg: cfg}
}

// SupportsPreview is driven by the configured preview interval: a positive
// interval enables the pseudo-streaming preview (periodic re-recognition),
// while a non-positive one disables it so recognition runs only at Finish.
func (e *senseVoiceEngine) SupportsPreview() bool {
	return e.cfg.SenseVoice.PreviewIntervalMs > 0
}

// Preload builds the recognizer up front for reuse. Like the sherpa engine's
// non-fatal parts, a failure here is logged but not fatal: sessions retry
// lazily, so a missing model degrades to a clear per-session failure rather
// than crashing the daemon.
func (e *senseVoiceEngine) Preload() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	recognizer, err := api.NewSenseVoiceRecognizer(e.cfg.SenseVoice)
	if err != nil {
		log.Printf("SenseVoice recognizer unavailable at preload: %v", err)
		return nil
	}
	e.pre = recognizer
	log.Println("SenseVoice engine preloaded for reuse")
	return nil
}

// NewSession opens a recognition session for one recording. It reuses the
// preloaded recognizer when available; otherwise it builds a per-session one
// that the session owns and closes. If building fails (e.g. model missing), it
// returns an inert session whose Finish yields empty text, matching the
// controller's "session init failed" degradation without crashing.
func (e *senseVoiceEngine) NewSession() (Session, error) {
	e.mu.Lock()
	pre := e.pre
	e.mu.Unlock()

	recognizer := pre
	var ownedRecognizer *api.SenseVoiceRecognizer
	if recognizer == nil {
		built, err := api.NewSenseVoiceRecognizer(e.cfg.SenseVoice)
		if err != nil {
			log.Printf("SenseVoice recognizer init failed: %v", err)
			return &senseVoiceSession{}, nil
		}
		recognizer = built
		ownedRecognizer = built
	}

	previewInterval := time.Duration(e.cfg.SenseVoice.PreviewIntervalMs) * time.Millisecond
	session := &senseVoiceSession{
		recognizer:      recognizer,
		ownedRecognizer: ownedRecognizer,
		previewInterval: previewInterval,
	}
	session.start()
	return session, nil
}

// Close releases the preloaded recognizer.
func (e *senseVoiceEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pre != nil {
		e.pre.Close()
		e.pre = nil
	}
}

// senseVoiceSession holds one recording's accumulated audio and a background
// loop that periodically re-recognizes it to produce a live preview. Because
// SenseVoice is non-streaming, "preview" means re-running the whole accumulated
// buffer on a timer; the result only converges as more audio arrives, so unlike
// the sherpa two-model path the preview and the final result come from the same
// model and never diverge.
type senseVoiceSession struct {
	// recognizer is nil when the model failed to load; in that case FeedAudio is
	// a no-op, PartialText is empty, and Finish returns empty text.
	recognizer *api.SenseVoiceRecognizer
	// ownedRecognizer is non-nil only in one-shot mode, where this session built
	// its own recognizer and must close it.
	ownedRecognizer *api.SenseVoiceRecognizer
	previewInterval time.Duration

	mu      sync.Mutex
	buffer  []float32 // audio accumulated so far (appended by FeedAudio)
	preview string    // most recent preview text, read cheaply by PartialText
	// inflight guards against preview re-runs piling up: if one re-run is still
	// running when the next tick fires, that tick is skipped (drop-frame). This
	// keeps long recordings, where a single full re-run can exceed the interval,
	// from queuing work indefinitely.
	inflight bool

	stopCh chan struct{}
	doneCh chan struct{}
}

// start launches the background preview loop when preview is enabled and a
// recognizer is available. With no recognizer or a non-positive interval it
// does nothing, leaving the session in a preview-less mode.
func (s *senseVoiceSession) start() {
	if s.recognizer == nil || s.previewInterval <= 0 {
		return
	}
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	go s.previewLoop()
}

// FeedAudio appends one frame to the accumulated buffer. It is intentionally
// cheap (no recognition): the background loop does the heavy work.
func (s *senseVoiceSession) FeedAudio(samples []float32) {
	if s.recognizer == nil {
		return
	}
	s.mu.Lock()
	s.buffer = append(s.buffer, samples...)
	s.mu.Unlock()
}

// PartialText returns the cached preview text. It never triggers recognition,
// so the controller can poll it on every audio frame without cost.
func (s *senseVoiceSession) PartialText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preview
}

// previewLoop periodically snapshots the accumulated audio and re-recognizes it,
// caching the result for PartialText. It exits when stopCh is closed.
func (s *senseVoiceSession) previewLoop() {
	defer close(s.doneCh)

	ticker := time.NewTicker(s.previewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			if s.inflight {
				// Previous re-run still going; skip this tick (drop-frame).
				s.mu.Unlock()
				continue
			}
			if len(s.buffer) == 0 {
				s.mu.Unlock()
				continue
			}
			snapshot := make([]float32, len(s.buffer))
			copy(snapshot, s.buffer)
			s.inflight = true
			s.mu.Unlock()

			start := time.Now()
			text, err := s.recognizer.Transcribe(snapshot)

			s.mu.Lock()
			s.inflight = false
			if err == nil && text != "" {
				s.preview = text
			}
			s.mu.Unlock()

			if err != nil {
				log.Printf("SenseVoice preview error: %v", err)
			} else {
				log.Printf("SenseVoice preview in %dms: %s", time.Since(start).Milliseconds(), text)
			}
		}
	}
}

// Finish stops the preview loop, waits for any in-flight re-run to finish, then
// runs one authoritative recognition over the full recorded audio and returns
// the final text. Using the recorder's complete samples (the same source the
// other engines use) keeps the final result consistent with the preview.
func (s *senseVoiceSession) Finish(recorder *audio.Recorder) (string, error) {
	if s.recognizer == nil {
		return "", nil
	}
	s.stopPreview()

	start := time.Now()
	text, err := s.recognizer.Transcribe(recorder.GetSamplesFloat32())
	log.Printf("SenseVoice recognition done in %dms: %s", time.Since(start).Milliseconds(), text)
	return text, err
}

// Close stops the preview loop (idempotent) and releases the recognizer if this
// session owns it.
func (s *senseVoiceSession) Close() {
	s.stopPreview()
	if s.ownedRecognizer != nil {
		s.ownedRecognizer.Close()
		s.ownedRecognizer = nil
	}
}

// stopPreview signals the preview loop to exit and waits for it, ensuring no
// background re-run touches the recognizer after this returns. Safe to call
// multiple times.
func (s *senseVoiceSession) stopPreview() {
	if s.stopCh == nil {
		return
	}
	close(s.stopCh)
	<-s.doneCh
	s.stopCh = nil
}
