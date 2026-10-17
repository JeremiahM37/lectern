package tui

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// Text from the clipboard, for Ctrl+V when the clipboard holds no image. The
// helpers are the same optional OS tools the image path uses; a missing one
// is reported and the paste is skipped, never a failure.

// clipboardTextReader returns the plain text on the clipboard. Each input box
// holds one, so tests can put a fake clipboard in its place.
type clipboardTextReader func() (string, error)

// errNoClipboardText is the text read's answer for a clipboard with no text.
var errNoClipboardText = errors.New("No text on the clipboard")

// systemClipboardText reads plain text from the OS clipboard.
func systemClipboardText() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clipTimeout)
	defer cancel()
	var out []byte
	switch runtime.GOOS {
	case "linux":
		var ok bool
		if out, ok = linuxClipboardText(ctx); !ok {
			return "", errNoClipboardTool
		}
	case "darwin":
		out = runClipboardCmd(ctx, "pbpaste")
	case "windows":
		out = runClipboardCmd(ctx, "powershell", "-NoProfile", "-NonInteractive", "-STA", "-Command", "Get-Clipboard -Raw")
	default:
		return "", errNoClipboardTool
	}
	text := strings.ReplaceAll(string(out), "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return "", errNoClipboardText
	}
	return text, nil
}

// linuxClipboardText asks wl-paste (Wayland) or xclip (X11) for text. The
// bool is false when neither helper is installed. A helper that ran but found
// no text returns empty output with true.
func linuxClipboardText(ctx context.Context) ([]byte, bool) {
	if _, err := exec.LookPath("wl-paste"); err == nil {
		return runClipboardCmd(ctx, "wl-paste", "--no-newline", "--type", "text"), true
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return runClipboardCmd(ctx, "xclip", "-selection", "clipboard", "-o"), true
	}
	return nil, false
}

// clipTextFound is what an image read returns when the clipboard holds text
// and no image. It carries the text, so the probe's result reaches the input
// without a second message type.
type clipTextFound struct{ text string }

func (c clipTextFound) Error() string { return "the clipboard holds text" }
