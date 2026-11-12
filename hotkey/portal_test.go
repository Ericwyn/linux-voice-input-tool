package hotkey

import (
	"testing"
	"time"

	"linux-voice-input/portal"
)

func TestPortalWaitReleasedBlocksUntilDeactivated(t *testing.T) {
	h := &portalHandler{presses: make(chan struct{}, 4), stop: make(chan struct{})}
	h.onEvent(portal.ToggleShortcutID, true)

	done := make(chan struct{})
	go func() {
		h.WaitReleased(time.Second)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("WaitReleased returned while the hotkey was still held")
	case <-time.After(30 * time.Millisecond):
	}

	h.onEvent(portal.ToggleShortcutID, false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitReleased did not return after Deactivated")
	}
	if len(h.presses) != 1 {
		t.Fatalf("queued presses = %d, want 1", len(h.presses))
	}
}

func TestPortalWaitReleasedReturnsWhenNotHeld(t *testing.T) {
	h := &portalHandler{presses: make(chan struct{}, 4), stop: make(chan struct{})}
	start := time.Now()
	h.WaitReleased(time.Second)
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("WaitReleased waited although no hotkey was held")
	}
}
