package engine

import (
	"log"
	"sync"
	"time"

	"linux-voice-input/api"
	"linux-voice-input/audio"
	"linux-voice-input/config"
)

// sherpaEngine owns the three preloaded sherpa models (streaming, offline,
// punctuation). In daemon mode they are loaded once via Preload and reused
// across sessions; in one-shot mode they stay nil and each session lazily
// builds and owns what it needs.
type sherpaEngine struct {
	cfg *config.Config
	mu  sync.Mutex

	preStream  *api.SherpaStreamRecognizer
	preOffline *api.SherpaOfflineRecognizer
	prePunct   *api.SherpaPunctuation
}

func newSherpaEngine(cfg *config.Config) *sherpaEngine {
	return &sherpaEngine{cfg: cfg}
}

// SupportsPreview is true: the streaming recognizer produces live text while
// recording.
func (e *sherpaEngine) SupportsPreview() bool { return true }

// Preload builds all three recognizers up front. The streaming recognizer is
// required; a failure there is fatal. The offline and punctuation models are
// best-effort: a failure is logged but not fatal, and sessions will retry
// lazily.
func (e *sherpaEngine) Preload() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	streamRecognizer, err := api.NewSherpaStreamRecognizer(e.cfg.Sherpa)
	if err != nil {
		return err
	}

	offlineRecognizer, err := api.NewSherpaOfflineRecognizer(e.cfg.Sherpa)
	if err != nil {
		log.Printf("Offline recognizer unavailable at preload: %v", err)
	}

	var punct *api.SherpaPunctuation
	if e.cfg.Sherpa.PunctModel != "" {
		punct, err = api.NewSherpaPunctuation(e.cfg.Sherpa.PunctModel)
		if err != nil {
			log.Printf("Punctuation model unavailable at preload: %v", err)
			punct = nil
		}
	}

	e.preStream = streamRecognizer
	e.preOffline = offlineRecognizer
	e.prePunct = punct
	log.Println("Sherpa engine preloaded for reuse")
	return nil
}

// NewSession creates the streaming stream for this recording. When the engine
// was preloaded it reuses the shared streaming recognizer; otherwise it builds
// a per-session recognizer that the session owns and closes when done. A
// failure to build the streaming recognizer is non-fatal: the session still
// works, falling back to the offline model in Finish.
func (e *sherpaEngine) NewSession() (Session, error) {
	e.mu.Lock()
	preStream := e.preStream
	preOffline := e.preOffline
	prePunct := e.prePunct
	e.mu.Unlock()

	recognizer := preStream
	var ownedRecognizer *api.SherpaStreamRecognizer
	if recognizer == nil {
		built, err := api.NewSherpaStreamRecognizer(e.cfg.Sherpa)
		if err != nil {
			log.Printf("Streaming recognizer init failed: %v", err)
		} else {
			recognizer = built
			ownedRecognizer = built
		}
	}

	var stream *api.SherpaStream
	if recognizer != nil {
		stream = recognizer.CreateStream()
	}

	return &sherpaSession{
		cfg:             e.cfg,
		stream:          stream,
		ownedRecognizer: ownedRecognizer,
		preOffline:      preOffline,
		prePunct:        prePunct,
	}, nil
}

// Close releases the preloaded recognizers.
func (e *sherpaEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.preStream != nil {
		e.preStream.Close()
		e.preStream = nil
	}
	if e.preOffline != nil {
		e.preOffline.Close()
		e.preOffline = nil
	}
	if e.prePunct != nil {
		e.prePunct.Close()
		e.prePunct = nil
	}
}

// sherpaSession holds the streaming stream for one recording plus references
// to the offline and punctuation models (shared when preloaded, lazily built
// otherwise).
type sherpaSession struct {
	cfg *config.Config

	// stream may be nil when the streaming recognizer failed to initialize; in
	// that case FeedAudio/PartialText are no-ops and Finish relies on the
	// offline model.
	stream *api.SherpaStream
	// ownedRecognizer is non-nil only in one-shot mode, where this session
	// built its own streaming recognizer and must close it in Close.
	ownedRecognizer *api.SherpaStreamRecognizer

	preOffline *api.SherpaOfflineRecognizer
	prePunct   *api.SherpaPunctuation
}

func (s *sherpaSession) FeedAudio(samples []float32) {
	if s.stream != nil {
		s.stream.FeedAudio(samples)
	}
}

func (s *sherpaSession) PartialText() string {
	if s.stream == nil {
		return ""
	}
	return s.stream.GetPartialResult()
}

// Finish runs the high-accuracy offline recognition on the full audio, then
// restores punctuation. It preserves the original multi-level fallbacks:
//  1. offline model unavailable -> fall back to the streaming result;
//  2. punctuation model unavailable -> return text without punctuation.
//
// The streaming result is also logged as a debug comparison. Because logging
// goes through the MultiWriter set up in main, this line shows up on the
// terminal in test mode and in run.log during normal operation, which is how
// the per-stage timing observability from the old test mode is preserved.
func (s *sherpaSession) Finish(recorder *audio.Recorder) (string, error) {
	if s.stream != nil {
		start := time.Now()
		streamingText := s.stream.Finish()
		log.Printf("Streaming result in %dms: %s", time.Since(start).Milliseconds(), streamingText)
	}

	offline := s.preOffline
	ownedOffline := false
	if offline == nil {
		built, err := api.NewSherpaOfflineRecognizer(s.cfg.Sherpa)
		if err != nil {
			log.Printf("Offline model unavailable, falling back to streaming result: %v", err)
			if s.stream != nil {
				return s.stream.Finish(), nil
			}
			return "", nil
		}
		offline = built
		ownedOffline = true
	}
	if ownedOffline {
		defer offline.Close()
	}

	start := time.Now()
	text, _ := offline.TranscribeAudioSamples(recorder.GetSamplesFloat32())
	log.Printf("Offline recognition done in %dms: %s", time.Since(start).Milliseconds(), text)

	if text == "" || s.cfg.Sherpa.PunctModel == "" {
		return text, nil
	}

	punct := s.prePunct
	ownedPunct := false
	if punct == nil {
		built, err := api.NewSherpaPunctuation(s.cfg.Sherpa.PunctModel)
		if err != nil {
			log.Printf("Punctuation model unavailable: %v", err)
			return text, nil
		}
		punct = built
		ownedPunct = true
	}
	if ownedPunct {
		defer punct.Close()
	}

	start = time.Now()
	text = punct.AddPunct(text)
	log.Printf("Punctuation done in %dms: %s", time.Since(start).Milliseconds(), text)
	return text, nil
}

func (s *sherpaSession) Close() {
	if s.stream != nil {
		s.stream.Close()
		s.stream = nil
	}
	if s.ownedRecognizer != nil {
		s.ownedRecognizer.Close()
		s.ownedRecognizer = nil
	}
}
