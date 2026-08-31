package input

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GetActiveWindow returns the currently focused window ID
func GetActiveWindow() string {
	out, err := exec.Command("xdotool", "getactivewindow").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// FocusWindow focuses the window with the given ID
func FocusWindow(windowID string) error {
	if windowID == "" {
		return nil
	}
	return exec.Command("xdotool", "windowactivate", "--sync", windowID).Run()
}

// PasteResult identifies when text was actually injected into the target
// window. InsertedAt is captured immediately after xdotool successfully emits
// Ctrl+V, before the optional clipboard-restoration delay.
type PasteResult struct {
	InsertedAt            time.Time
	ClipboardRestoreError error
}

type pasteClipboardOwner interface {
	restore(clipboardSnapshot) error
	waitUntilServed(time.Duration)
}

type pasteRuntime struct {
	captureClipboard    func() (clipboardSnapshot, error)
	startClipboardOwner func(string) (pasteClipboardOwner, error)
	focusWindow         func(string) error
	releaseModifiers    func()
	simulatePaste       func() error
	sleep               func(time.Duration)
	now                 func() time.Time
}

func PasteText(text string, restoreClipboard bool, targetWindow string) (PasteResult, error) {
	return pasteTextWithRuntime(text, restoreClipboard, targetWindow, pasteRuntime{
		captureClipboard: captureClipboard,
		startClipboardOwner: func(text string) (pasteClipboardOwner, error) {
			return startClipboardOwner(text)
		},
		focusWindow: FocusWindow,
		releaseModifiers: func() {
			_ = exec.Command("xdotool", "keyup", "alt", "shift", "super", "ctrl", "Meta_L", "Meta_R").Run()
		},
		simulatePaste: func() error {
			return exec.Command("xdotool", "key", "--clearmodifiers", "ctrl+v").Run()
		},
		sleep: time.Sleep,
		now:   time.Now,
	})
}

func pasteTextWithRuntime(text string, restoreClipboard bool, targetWindow string, runtime pasteRuntime) (PasteResult, error) {
	var snapshot clipboardSnapshot
	canRestoreClipboard := restoreClipboard
	var clipboardBackupErr error
	if canRestoreClipboard {
		var err error
		snapshot, err = runtime.captureClipboard()
		if err != nil {
			// Clipboard preservation is secondary to inserting the recognized
			// text. Continue with a one-shot replacement and report the lost
			// restoration capability after Ctrl+V succeeds.
			clipboardBackupErr = fmt.Errorf("back up clipboard: %w", err)
			canRestoreClipboard = false
		}
	}

	owner, err := runtime.startClipboardOwner(text)
	if err != nil {
		return PasteResult{}, fmt.Errorf("write to clipboard: %w", err)
	}
	restoreAfterFailure := func(operationErr error) error {
		if !canRestoreClipboard {
			if clipboardBackupErr != nil {
				return errors.Join(operationErr, clipboardBackupErr)
			}
			return operationErr
		}
		if restoreErr := owner.restore(snapshot); restoreErr != nil {
			return errors.Join(operationErr, fmt.Errorf("restore clipboard: %w", restoreErr))
		}
		return operationErr
	}

	// Focus back to the original window
	if targetWindow != "" {
		if err := runtime.focusWindow(targetWindow); err != nil {
			return PasteResult{}, restoreAfterFailure(fmt.Errorf("focus target window: %w", err))
		}
		runtime.sleep(100 * time.Millisecond)
	}

	// Explicitly release all modifier keys to avoid stuck keys
	// when triggered via keyboard shortcut
	runtime.releaseModifiers()
	runtime.sleep(50 * time.Millisecond)

	if err := runtime.simulatePaste(); err != nil {
		return PasteResult{}, restoreAfterFailure(fmt.Errorf("simulate paste: %w", err))
	}
	result := PasteResult{InsertedAt: runtime.now()}

	if canRestoreClipboard {
		// Most clients request the data immediately after Ctrl+V. Waiting for an
		// actual payload request avoids the old fixed 800 ms race; the timeout is
		// only a fallback for unusual clients that do not request clipboard data.
		owner.waitUntilServed(clipboardRequestTimeout)
		result.ClipboardRestoreError = owner.restore(snapshot)
	} else {
		result.ClipboardRestoreError = clipboardBackupErr
	}

	return result, nil
}
