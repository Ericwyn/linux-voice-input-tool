// Package engine defines the speech-recognition engine strategy. Each engine
// (local sherpa pipeline, remote whisper API, or future engines) implements
// the Engine and Session interfaces, and engine.New is the single factory that
// maps a configured engine name to a concrete implementation. This keeps the
// session controller free of any engine-specific branching.
package engine

import (
	"fmt"

	"linux-voice-input/audio"
	"linux-voice-input/config"
)

// Engine is a long-lived recognition engine instance. It owns heavy resources
// such as loaded models or a remote client, can be preloaded once by the
// daemon and reused across many recording sessions. All methods must be safe
// for concurrent use.
type Engine interface {
	// Preload eagerly loads the heavy resources the engine needs (e.g. local
	// models). The daemon calls it once at startup. For engines that need no
	// preloading (e.g. whisper) it is a no-op. When a non-fatal part fails to
	// load, implementations should log and return nil, leaving the rest to be
	// lazily loaded later.
	Preload() error

	// NewSession opens the recognition session for a single recording. It is
	// called once each time recording starts.
	NewSession() (Session, error)

	// SupportsPreview reports whether the engine can produce live preview text
	// while recording is in progress. sherpa returns true, whisper false. The
	// controller uses this to decide whether to poll PartialText during
	// recording and whether to emit a "recognizing" notification before
	// Finish.
	SupportsPreview() bool

	// Close releases all heavy resources held by the engine. Safe to call
	// multiple times.
	Close()
}

// Session represents the recognition work for a single recording and holds any
// per-recording transient state.
type Session interface {
	// FeedAudio accepts one frame of audio (float32, 16kHz, range [-1, 1]).
	// Engines that recognize while recording feed it to their streaming
	// decoder; others treat it as a no-op.
	FeedAudio(samples []float32)

	// PartialText returns the current live preview text. Engines that do not
	// support preview return an empty string.
	PartialText() string

	// Finish produces the final transcription after recording stops. The
	// recorder provides the complete audio (as float32 samples or a saved WAV)
	// for offline or remote recognition.
	Finish(recorder *audio.Recorder) (string, error)

	// Close releases the session's transient resources. Safe to call multiple
	// times.
	Close()
}

// New creates the engine matching the configured engine name. This is the
// single entry point to add a new engine: implement Engine/Session and add a
// case here.
func New(cfg *config.Config) (Engine, error) {
	switch cfg.API.Engine {
	case "sherpa":
		return newSherpaEngine(cfg), nil
	case "whisper":
		return newWhisperEngine(cfg), nil
	default:
		return nil, fmt.Errorf("unknown engine: %q", cfg.API.Engine)
	}
}
