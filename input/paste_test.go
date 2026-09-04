package input

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/xgb/xproto"
)

type fakePasteClipboardOwner struct {
	beginCalls    int
	waitCalls     int
	restoreCalls  int
	serveSequence uint64
	waitedAfter   uint64
	restored      clipboardSnapshot
	restoreErr    error
}

func (owner *fakePasteClipboardOwner) restore(snapshot clipboardSnapshot) error {
	owner.restoreCalls++
	owner.restored = snapshot.clone()
	return owner.restoreErr
}

func (owner *fakePasteClipboardOwner) beginPaste() uint64 {
	owner.beginCalls++
	return owner.serveSequence
}

func (owner *fakePasteClipboardOwner) waitUntilServedAfter(sequence uint64, _ time.Duration) {
	owner.waitCalls++
	owner.waitedAfter = sequence
}

func TestCaptureAdvertisedTargetsSkipsRefusedFormat(t *testing.T) {
	const (
		utf8Target     xproto.Atom = 101
		refusedTarget  xproto.Atom = 102
		metadataTarget xproto.Atom = 103
	)
	names := map[xproto.Atom]string{
		utf8Target:     "UTF8_STRING",
		refusedTarget:  "text/plain;charset=utf-8",
		metadataTarget: "TARGETS",
	}
	convertCalls := make(map[xproto.Atom]int)

	payloads, err := captureAdvertisedTargets(
		[]xproto.Atom{utf8Target, refusedTarget, metadataTarget, utf8Target},
		func(target xproto.Atom) (string, error) {
			return names[target], nil
		},
		func(target xproto.Atom, _ int) (clipboardPayload, error) {
			convertCalls[target]++
			if target == refusedTarget {
				return clipboardPayload{}, errSelectionTargetRefused
			}
			return clipboardPayload{Type: target, Format: 8, Data: []byte("text")}, nil
		},
	)
	if err != nil {
		t.Fatalf("capture advertised targets: %v", err)
	}
	if len(payloads) != 1 || payloads[0].Target != utf8Target || string(payloads[0].Data) != "text" {
		t.Fatalf("captured payloads = %#v, want only UTF8_STRING", payloads)
	}
	if convertCalls[utf8Target] != 1 || convertCalls[refusedTarget] != 1 || convertCalls[metadataTarget] != 0 {
		t.Fatalf("conversion calls = %#v", convertCalls)
	}
}

func TestCaptureAdvertisedTargetsRejectsCompletelyUnreadableClipboard(t *testing.T) {
	_, err := captureAdvertisedTargets(
		[]xproto.Atom{101},
		func(xproto.Atom) (string, error) {
			return "text/plain;charset=utf-8", nil
		},
		func(xproto.Atom, int) (clipboardPayload, error) {
			return clipboardPayload{}, errSelectionTargetRefused
		},
	)
	if err == nil || !strings.Contains(err.Error(), "refused all advertised payload targets") {
		t.Fatalf("capture error = %v, want all-targets-refused error", err)
	}
}

func TestPasteContinuesWhenClipboardBackupFails(t *testing.T) {
	backupCause := errors.New("clipboard owner is inconsistent")
	owner := &fakePasteClipboardOwner{}
	insertedAt := time.Date(2026, time.August, 28, 19, 0, 0, 0, time.Local)
	startedText := ""
	simulatedPaste := false

	result, err := pasteTextWithRuntime("识别结果", true, "", pasteRuntime{
		captureClipboard: func() (clipboardSnapshot, error) {
			return clipboardSnapshot{}, backupCause
		},
		startClipboardOwner: func(text string) (pasteClipboardOwner, error) {
			startedText = text
			return owner, nil
		},
		focusWindow:      func(string) error { return nil },
		releaseModifiers: func() {},
		simulatePaste: func() error {
			simulatedPaste = true
			return nil
		},
		sleep: func(time.Duration) {},
		now:   func() time.Time { return insertedAt },
	})
	if err != nil {
		t.Fatalf("paste should continue after backup failure: %v", err)
	}
	if startedText != "识别结果" || !simulatedPaste || result.InsertedAt != insertedAt {
		t.Fatalf("paste result = text:%q simulated:%v inserted:%v", startedText, simulatedPaste, result.InsertedAt)
	}
	if !errors.Is(result.ClipboardRestoreError, backupCause) {
		t.Fatalf("clipboard warning = %v, want wrapped backup error", result.ClipboardRestoreError)
	}
	if owner.beginCalls != 0 || owner.waitCalls != 0 || owner.restoreCalls != 0 {
		t.Fatalf("unavailable backup was used: begins=%d waits=%d restores=%d", owner.beginCalls, owner.waitCalls, owner.restoreCalls)
	}
}

func TestPasteRestoresSuccessfulClipboardBackup(t *testing.T) {
	snapshot := clipboardSnapshot{HadOwner: true, Payloads: []clipboardPayload{{
		Target: 101,
		Type:   101,
		Format: 8,
		Data:   []byte("original"),
	}}}
	// Model a clipboard history tool that already fetched the recognized text
	// before Ctrl+V. The paste must wait for a request after this sequence.
	owner := &fakePasteClipboardOwner{serveSequence: 1}

	result, err := pasteTextWithRuntime("识别结果", true, "42", pasteRuntime{
		captureClipboard: func() (clipboardSnapshot, error) { return snapshot, nil },
		startClipboardOwner: func(string) (pasteClipboardOwner, error) {
			return owner, nil
		},
		focusWindow:      func(string) error { return nil },
		releaseModifiers: func() {},
		simulatePaste: func() error {
			if owner.beginCalls != 1 {
				t.Fatalf("paste began before clipboard request marker was captured")
			}
			owner.serveSequence++
			return nil
		},
		sleep: func(time.Duration) {},
		now:   time.Now,
	})
	if err != nil || result.ClipboardRestoreError != nil {
		t.Fatalf("paste result = %#v, error = %v", result, err)
	}
	if owner.beginCalls != 1 || owner.waitCalls != 1 || owner.restoreCalls != 1 {
		t.Fatalf("clipboard restore calls = begins:%d waits:%d restores:%d", owner.beginCalls, owner.waitCalls, owner.restoreCalls)
	}
	if owner.waitedAfter != 1 {
		t.Fatalf("waited after sequence %d, want pre-paste sequence 1", owner.waitedAfter)
	}
	if len(owner.restored.Payloads) != 1 || string(owner.restored.Payloads[0].Data) != "original" {
		t.Fatalf("restored snapshot = %#v", owner.restored)
	}
}
