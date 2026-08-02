package engine

import (
	"fmt"
	"os"
	"path/filepath"

	"linux-voice-input/api"
	"linux-voice-input/audio"
	"linux-voice-input/config"
)

// whisperEngine transcribes via a remote API (SiliconFlow). It holds no heavy
// resources: there is nothing to preload and no live streaming preview.
type whisperEngine struct {
	cfg *config.Config
}

func newWhisperEngine(cfg *config.Config) *whisperEngine {
	return &whisperEngine{cfg: cfg}
}

// Preload is a no-op: the remote API needs no local model loading.
func (e *whisperEngine) Preload() error { return nil }

// SupportsPreview is false: the API only transcribes the complete audio.
func (e *whisperEngine) SupportsPreview() bool { return false }

func (e *whisperEngine) NewSession() (Session, error) {
	return &whisperSession{cfg: e.cfg}, nil
}

func (e *whisperEngine) Close() {}

// whisperSession buffers nothing itself; it hands the recorded audio to the
// remote API only when Finish is called.
type whisperSession struct {
	cfg *config.Config
}

// FeedAudio is a no-op: whisper does not recognize while recording.
func (s *whisperSession) FeedAudio(samples []float32) {}

// PartialText returns an empty string: whisper has no live preview.
func (s *whisperSession) PartialText() string { return "" }

func (s *whisperSession) Finish(recorder *audio.Recorder) (string, error) {
	tmpFile := filepath.Join(os.TempDir(), "linux-voice-input.wav")
	if err := recorder.SaveWAV(tmpFile); err != nil {
		return "", fmt.Errorf("save wav: %w", err)
	}
	defer os.Remove(tmpFile)

	client := api.NewClient(s.cfg.API.BaseURL, s.cfg.API.Key, s.cfg.API.Model)
	return client.Transcribe(tmpFile)
}

func (s *whisperSession) Close() {}
