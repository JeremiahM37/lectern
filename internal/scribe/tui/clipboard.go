package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Image input from the clipboard and from pasted file paths. The OS helpers
// are optional: a missing helper is a notice in the input, never a failure.

// clipboardReader returns the image on the clipboard and its media type. Each
// input box holds one, so tests can put a fake clipboard in its place.
type clipboardReader func() (data []byte, mediaType string, err error)

// imageProbeMsg is the result of a clipboard image read started by Ctrl+V.
type imageProbeMsg struct {
	tab  int
	data []byte
	mt   string
	err  error
}

// clipImageMax is the largest image attached. The API takes 5 MB of base64,
// which is about 3.75 MB of raw bytes. A larger image is shrunk to fit (see
// image_shrink.go), and refused only when it cannot be.
const clipImageMax = 3_750_000

// clipTimeout bounds one clipboard helper run.
const clipTimeout = 4 * time.Second

var (
	errNoClipboardImage = errors.New("No image on the clipboard")
	errNoClipboardTool  = errors.New("No clipboard tool found; install wl-clipboard or xclip")
)

// systemClipboardImage reads an image from the OS clipboard with the helper
// for this platform.
func systemClipboardImage() ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clipTimeout)
	defer cancel()
	var data []byte
	var err error
	switch runtime.GOOS {
	case "linux":
		data, err = linuxClipboardImage(ctx)
	case "darwin":
		data, err = darwinClipboardImage(ctx)
	case "windows":
		data, err = windowsClipboardImage(ctx)
	default:
		return nil, "", errNoClipboardTool
	}
	if err != nil {
		return nil, "", err
	}
	mt := sniffImage(data)
	if mt == "" {
		return nil, "", errNoClipboardImage
	}
	// An oversize image is shrunk when it is attached (attachFitted), which
	// is also where the user is told about it.
	return data, mt, nil
}

// linuxClipboardImage tries wl-paste (Wayland), then xclip (X11).
func linuxClipboardImage(ctx context.Context) ([]byte, error) {
	helpers := []struct {
		name string
		args []string
	}{
		{"wl-paste", []string{"--type", "image/png"}},
		{"xclip", []string{"-selection", "clipboard", "-t", "image/png", "-o"}},
	}
	found := false
	for _, h := range helpers {
		if _, err := exec.LookPath(h.name); err != nil {
			continue
		}
		found = true
		if out := runClipboardCmd(ctx, h.name, h.args...); len(out) > 0 {
			return out, nil
		}
	}
	if !found {
		return nil, errNoClipboardTool
	}
	return nil, errNoClipboardImage
}

// darwinClipboardImage uses pngpaste when it is installed. Otherwise AppleScript
// writes the clipboard's PNG data to a temp file. The path comes in through the
// environment, so it needs no quoting.
func darwinClipboardImage(ctx context.Context) ([]byte, error) {
	if _, err := exec.LookPath("pngpaste"); err == nil {
		if out := runClipboardCmd(ctx, "pngpaste", "-"); len(out) > 0 {
			return out, nil
		}
	}
	f, err := os.CreateTemp("", "scribe-clip-*.png")
	if err != nil {
		return nil, errNoClipboardImage
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	script := `set fh to open for access POSIX file (system attribute "SCRIBE_CLIP_OUT") with write permission
set eof of fh to 0
write (the clipboard as «class PNGf») to fh
close access fh`
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	cmd.Env = append(os.Environ(), "SCRIBE_CLIP_OUT="+path)
	if err := cmd.Run(); err != nil {
		return nil, errNoClipboardImage
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errNoClipboardImage
	}
	return data, nil
}

// windowsClipboardImage asks PowerShell for the clipboard bitmap and gets it
// back as base64 PNG. Clipboard access needs a single-threaded apartment (-STA).
// Get-Clipboard cannot return image bytes, so this goes through
// System.Windows.Forms instead.
func windowsClipboardImage(ctx context.Context) ([]byte, error) {
	script := `Add-Type -AssemblyName System.Windows.Forms,System.Drawing; ` +
		`$img = [System.Windows.Forms.Clipboard]::GetImage(); if ($null -eq $img) { exit 1 }; ` +
		`$ms = New-Object System.IO.MemoryStream; $img.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png); ` +
		`[Console]::Out.Write([Convert]::ToBase64String($ms.ToArray()))`
	out := runClipboardCmd(ctx, "powershell", "-NoProfile", "-NonInteractive", "-STA", "-Command", script)
	if len(out) == 0 {
		return nil, errNoClipboardImage
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, errNoClipboardImage
	}
	return data, nil
}

// runClipboardCmd runs a helper and returns its stdout, or nil if it fails.
func runClipboardCmd(ctx context.Context, name string, args ...string) []byte {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil
	}
	return out
}

// sniffImage names the media type from the file's magic bytes, or returns ""
// when the bytes are not a PNG, JPEG, GIF or WebP image.
func sniffImage(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// imagePathFromPaste reports whether a bracketed paste is one existing file
// path to a png, jpg, gif or webp image. Quotes, file:// URLs and terminal
// backslash-escaped spaces are accepted. Anything else is ordinary text.
func imagePathFromPaste(text string) (string, bool) {
	p := strings.TrimSpace(text)
	if p == "" || strings.ContainsAny(p, "\r\n") {
		return "", false
	}
	if n := len(p); n >= 2 && (p[0] == '\'' || p[0] == '"') && p[n-1] == p[0] {
		p = p[1 : n-1]
	}
	if strings.HasPrefix(p, "file://") {
		u, err := url.PathUnescape(strings.TrimPrefix(p, "file://"))
		if err != nil {
			return "", false
		}
		p = u
	} else {
		p = strings.ReplaceAll(p, `\ `, " ")
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
	default:
		return "", false
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", false
	}
	return p, true
}

// loadImageFile reads an image file for attaching.
func loadImageFile(path string) ([]byte, string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if st.Size() > shrinkMaxInput {
		return nil, "", imageTooBig(int(st.Size()))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if len(data) > clipImageMax {
		// attachFitted shrinks it, or refuses with the size error. Only an
		// image can be shrunk, so a big file that is not one gets that too.
		return data, sniffImage(data), nil
	}
	mt := sniffImage(data)
	if mt == "" {
		return nil, "", errors.New("not a PNG, JPEG, GIF or WebP image")
	}
	return data, mt, nil
}

// imageTooBig explains the size limit for an image of n bytes.
func imageTooBig(n int) error {
	return fmt.Errorf("Image is %.1f MB; the limit is 3.75 MB", float64(n)/1e6)
}

// osc52 is the escape sequence that asks the terminal to put text on the
// system clipboard. It works over ssh and needs no helper program.
func osc52(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
}

// clipboardOut is where copyToTerminal writes; tests point it away from the
// real terminal.
var clipboardOut io.Writer = os.Stdout

// copyToTerminal sends text to the clipboard through the terminal (OSC 52).
func copyToTerminal(text string) {
	fmt.Fprint(clipboardOut, osc52(text))
}
