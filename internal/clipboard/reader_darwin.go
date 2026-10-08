//go:build darwin

package clipboard

import (
	"context"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// NewReader reads the macOS pasteboard: pbpaste for text, osascript for images.
func NewReader() Reader { return darwinReader{} }

type darwinReader struct{}

var classes = map[string]string{"image/png": "PNGf", "image/jpeg": "JPEG", "image/gif": "GIFf"}

func (darwinReader) Types(ctx context.Context) ([]string, error) {
	out, err := run(ctx, "osascript", "-e", "clipboard info")
	if err != nil {
		return nil, err
	}
	info := string(out)
	var t []string
	if strings.Contains(info, "PNGf") || strings.Contains(info, "TIFF") {
		t = append(t, "image/png")
	}
	if strings.Contains(info, "JPEG") {
		t = append(t, "image/jpeg")
	}
	if strings.Contains(info, "GIFf") {
		t = append(t, "image/gif")
	}
	if strings.Contains(info, "«class utf8»") || strings.Contains(info, "string") || strings.Contains(info, "Unicode text") {
		t = append(t, "text/plain")
	}
	return t, nil
}

var dataRe = regexp.MustCompile(`«data [A-Za-z]{4}([0-9A-Fa-f]+)»`)

func (darwinReader) Read(ctx context.Context, mime string) ([]byte, error) {
	if mime == "text/plain" {
		return run(ctx, "pbpaste")
	}
	class, ok := classes[mime]
	if !ok {
		return nil, errors.New("unsupported type")
	}
	out, err := run(ctx, "osascript", "-e", "the clipboard as «class "+class+"»")
	if err != nil && class == "PNGf" {
		// a screenshot copied to the pasteboard may hold only TIFF: let the
		// system convert it
		out, err = run(ctx, "osascript", "-e", "set d to the clipboard as «class PNGf»\nreturn d")
	}
	if err != nil {
		return nil, err
	}
	m := dataRe.FindSubmatch(out)
	if m == nil {
		return nil, errors.New("no image on the clipboard")
	}
	return hex.DecodeString(string(m[1]))
}
