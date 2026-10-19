package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Ctrl+Y also writes the OS clipboard, not only the OSC 52 sequence. OSC 52 is
// ignored by some terminals and by tmux without set-clipboard, so the native
// tool is the copy that always lands. A missing tool is not an error: the
// terminal copy has already gone out, so the write is quiet.

// nativeClipboardTimeout bounds one clipboard tool run.
const nativeClipboardTimeout = 2 * time.Second

// nativeCopy writes text to the OS clipboard. Tests replace it, so a test run
// never puts its text on the developer's clipboard.
var nativeCopy = writeNativeClipboard

// errNoNativeClipboard means no clipboard tool is installed for this platform.
var errNoNativeClipboard = errors.New("no clipboard tool found")

// nativeCopyTool picks the clipboard writer for a platform: the command name
// and its arguments. ok is false when none is installed. have reports whether
// a command is on PATH; env reads one environment variable.
func nativeCopyTool(goos string, env func(string) string, have func(string) bool) (name string, args []string, ok bool) {
	switch goos {
	case "darwin":
		return onPath("pbcopy", have)
	case "windows":
		return onPath("clip.exe", have)
	case "linux":
		// Wayland first when it is running, since xclip under XWayland
		// writes a selection that native Wayland apps do not see.
		if env("WAYLAND_DISPLAY") != "" && have("wl-copy") {
			return "wl-copy", nil, true
		}
		if env("DISPLAY") != "" && have("xclip") {
			return "xclip", []string{"-selection", "clipboard"}, true
		}
		if env("DISPLAY") != "" && have("xsel") {
			return "xsel", []string{"--clipboard", "--input"}, true
		}
	}
	return "", nil, false
}

// onPath is the tool name with no arguments when it is installed.
func onPath(name string, have func(string) bool) (string, []string, bool) {
	if have(name) {
		return name, nil, true
	}
	return "", nil, false
}

// writeNativeClipboard puts text on the OS clipboard with the platform tool.
func writeNativeClipboard(text string) error {
	name, args, ok := nativeCopyTool(runtime.GOOS, os.Getenv, func(c string) bool {
		_, err := exec.LookPath(c)
		return err == nil
	})
	if !ok {
		return errNoNativeClipboard
	}
	ctx, cancel := context.WithTimeout(context.Background(), nativeClipboardTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
