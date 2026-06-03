package input

import (
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

func PasteText(text string, restoreClipboard bool, targetWindow string) error {
	var oldClip string
	if restoreClipboard {
		out, _ := exec.Command("xclip", "-selection", "clipboard", "-o").Output()
		oldClip = string(out)
	}

	cmd := exec.Command("xclip", "-selection", "clipboard")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write to clipboard: %w", err)
	}

	// Focus back to the original window
	if targetWindow != "" {
		if err := FocusWindow(targetWindow); err != nil {
			return fmt.Errorf("focus target window: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := exec.Command("xdotool", "key", "--clearmodifiers", "ctrl+v").Run(); err != nil {
		return fmt.Errorf("simulate paste: %w", err)
	}

	if restoreClipboard && oldClip != "" {
		time.Sleep(800 * time.Millisecond)
		cmd := exec.Command("xclip", "-selection", "clipboard")
		cmd.Stdin = strings.NewReader(oldClip)
		cmd.Run()
	}

	return nil
}
