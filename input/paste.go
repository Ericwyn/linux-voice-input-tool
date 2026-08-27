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

func PasteText(text string, restoreClipboard bool, targetWindow string) (PasteResult, error) {
	var snapshot clipboardSnapshot
	if restoreClipboard {
		var err error
		snapshot, err = captureClipboard()
		if err != nil {
			return PasteResult{}, fmt.Errorf("back up clipboard: %w", err)
		}
	}

	owner, err := startClipboardOwner(text)
	if err != nil {
		return PasteResult{}, fmt.Errorf("write to clipboard: %w", err)
	}
	restoreAfterFailure := func(operationErr error) error {
		if !restoreClipboard {
			return operationErr
		}
		if restoreErr := owner.restore(snapshot); restoreErr != nil {
			return errors.Join(operationErr, fmt.Errorf("restore clipboard: %w", restoreErr))
		}
		return operationErr
	}

	// Focus back to the original window
	if targetWindow != "" {
		if err := FocusWindow(targetWindow); err != nil {
			return PasteResult{}, restoreAfterFailure(fmt.Errorf("focus target window: %w", err))
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Explicitly release all modifier keys to avoid stuck keys
	// when triggered via keyboard shortcut
	exec.Command("xdotool", "keyup", "alt", "shift", "super", "ctrl", "Meta_L", "Meta_R").Run()
	time.Sleep(50 * time.Millisecond)

	if err := exec.Command("xdotool", "key", "--clearmodifiers", "ctrl+v").Run(); err != nil {
		return PasteResult{}, restoreAfterFailure(fmt.Errorf("simulate paste: %w", err))
	}
	result := PasteResult{InsertedAt: time.Now()}

	if restoreClipboard {
		// Most clients request the data immediately after Ctrl+V. Waiting for an
		// actual payload request avoids the old fixed 800 ms race; the timeout is
		// only a fallback for unusual clients that do not request clipboard data.
		owner.waitUntilServed(clipboardRequestTimeout)
		result.ClipboardRestoreError = owner.restore(snapshot)
	}

	return result, nil
}
