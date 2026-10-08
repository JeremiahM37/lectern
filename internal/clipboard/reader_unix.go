//go:build !darwin && !windows

package clipboard

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// NewReader returns the clipboard reader for this machine: wl-paste under
// Wayland, xclip under X11.
func NewReader() Reader { return unixReader{} }

type unixReader struct{}

func have(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (unixReader) Types(ctx context.Context) ([]string, error) {
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "" && have("wl-paste"):
		out, err := run(ctx, "wl-paste", "-l")
		if err != nil {
			return nil, nil // "No selection" is an empty clipboard
		}
		return Servable(strings.Split(string(out), "\n")), nil
	case os.Getenv("DISPLAY") != "" && have("xclip"):
		out, err := run(ctx, "xclip", "-selection", "clipboard", "-t", "TARGETS", "-o")
		if err != nil {
			return nil, nil
		}
		return Servable(strings.Split(string(out), "\n")), nil
	}
	return nil, errors.New("no clipboard tool (install wl-clipboard or xclip)")
}

func (unixReader) Read(ctx context.Context, mime string) ([]byte, error) {
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "" && have("wl-paste"):
		return run(ctx, "wl-paste", "--no-newline", "--type", mime)
	case os.Getenv("DISPLAY") != "" && have("xclip"):
		return run(ctx, "xclip", "-selection", "clipboard", "-t", mime, "-o")
	}
	return nil, errors.New("no clipboard tool")
}
