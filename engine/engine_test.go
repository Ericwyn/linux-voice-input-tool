package engine

import (
	"fmt"
	"testing"

	"linux-voice-input/audio"
	"linux-voice-input/config"
)

// Compile-time assertions that every engine satisfies the interfaces.
var (
	_ Engine  = (*sherpaEngine)(nil)
	_ Engine  = (*senseVoiceEngine)(nil)
	_ Engine  = (*whisperEngine)(nil)
	_ Session = (*sherpaSession)(nil)
	_ Session = (*senseVoiceSession)(nil)
	_ Session = (*whisperSession)(nil)
)

func TestNewEngine(t *testing.T) {
	tests := []struct {
		name       string
		engineName string
		wantErr    bool
		wantType   string
	}{
		{name: "sherpa", engineName: "sherpa", wantType: "*engine.sherpaEngine"},
		{name: "sensevoice", engineName: "sensevoice", wantType: "*engine.senseVoiceEngine"},
		{name: "whisper", engineName: "whisper", wantType: "*engine.whisperEngine"},
		{name: "unknown", engineName: "does-not-exist", wantErr: true},
		{name: "empty", engineName: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Engine = tt.engineName

			eng, err := New(cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("New(%q) expected error, got engine %T", tt.engineName, eng)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q) unexpected error: %v", tt.engineName, err)
			}
			if got := typeName(eng); got != tt.wantType {
				t.Fatalf("New(%q) type = %s, want %s", tt.engineName, got, tt.wantType)
			}
		})
	}
}

func TestWhisperEngineNoPreview(t *testing.T) {
	cfg := &config.Config{}
	cfg.Engine = "whisper"

	eng := newWhisperEngine(cfg)
	if eng.SupportsPreview() {
		t.Fatal("whisper SupportsPreview() = true, want false")
	}
	// Preload is a no-op and must not error.
	if err := eng.Preload(); err != nil {
		t.Fatalf("whisper Preload() error: %v", err)
	}
}

func TestWhisperSession(t *testing.T) {
	cfg := &config.Config{}
	cfg.Engine = "whisper"

	sess, err := newWhisperEngine(cfg).NewSession()
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}

	// FeedAudio is a no-op and must not panic.
	sess.FeedAudio([]float32{0.1, -0.2, 0.3})

	if got := sess.PartialText(); got != "" {
		t.Fatalf("whisper PartialText() = %q, want empty", got)
	}

	// With an empty recorder there is no audio to save, so Finish must return
	// an error rather than attempting a remote call.
	recorder := audio.NewRecorder(16000)
	if _, err := sess.Finish(recorder); err == nil {
		t.Fatal("whisper Finish() with empty recorder expected error, got nil")
	}
}

// TestSenseVoicePreviewToggle verifies the preview interval drives whether the
// engine advertises live-preview support.
func TestSenseVoicePreviewToggle(t *testing.T) {
	tests := []struct {
		name       string
		intervalMs int
		want       bool
	}{
		{name: "enabled default", intervalMs: 1000, want: true},
		{name: "disabled zero", intervalMs: 0, want: false},
		{name: "disabled negative", intervalMs: -1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Engine = "sensevoice"
			cfg.SenseVoice.PreviewIntervalMs = tt.intervalMs

			eng := newSenseVoiceEngine(cfg)
			if got := eng.SupportsPreview(); got != tt.want {
				t.Fatalf("SupportsPreview() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSenseVoiceSessionNoModel verifies that when the model cannot be built
// (empty path), NewSession returns an inert session that neither panics nor
// blocks: FeedAudio is a no-op, PartialText is empty, and Finish yields empty
// text with no error.
func TestSenseVoiceSessionNoModel(t *testing.T) {
	cfg := &config.Config{}
	cfg.Engine = "sensevoice"
	// Leave SenseVoice.Model empty so the recognizer fails to build.
	cfg.SenseVoice.PreviewIntervalMs = 1000

	sess, err := newSenseVoiceEngine(cfg).NewSession()
	if err != nil {
		t.Fatalf("NewSession() error: %v", err)
	}
	defer sess.Close()

	// FeedAudio must not panic on the inert session.
	sess.FeedAudio([]float32{0.1, -0.2, 0.3})

	if got := sess.PartialText(); got != "" {
		t.Fatalf("PartialText() = %q, want empty", got)
	}

	recorder := audio.NewRecorder(16000)
	text, err := sess.Finish(recorder)
	if err != nil {
		t.Fatalf("Finish() error: %v", err)
	}
	if text != "" {
		t.Fatalf("Finish() text = %q, want empty", text)
	}
}

func typeName(v interface{}) string {
	return fmt.Sprintf("%T", v)
}
