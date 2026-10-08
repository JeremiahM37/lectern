//go:build windows

package clipboard

import (
	"context"
	"encoding/base64"
	"strings"
)

// NewReader reads the Windows clipboard through PowerShell.
func NewReader() Reader { return windowsReader{} }

type windowsReader struct{}

const psPrefix = "Add-Type -AssemblyName System.Windows.Forms,System.Drawing; "

func ps(ctx context.Context, script string) ([]byte, error) {
	return run(ctx, "powershell", "-NoProfile", "-NonInteractive", "-STA", "-Command", psPrefix+script)
}

func (windowsReader) Types(ctx context.Context) ([]string, error) {
	out, err := ps(ctx, "if ([Windows.Forms.Clipboard]::ContainsImage()) { 'image' }; if ([Windows.Forms.Clipboard]::ContainsText()) { 'text' }")
	if err != nil {
		return nil, err
	}
	var t []string
	for _, l := range strings.Fields(string(out)) {
		switch l {
		case "image":
			t = append(t, "image/png")
		case "text":
			t = append(t, "text/plain")
		}
	}
	return t, nil
}

func (windowsReader) Read(ctx context.Context, mime string) ([]byte, error) {
	if mime == "text/plain" {
		out, err := ps(ctx, "[Console]::OutputEncoding=[Text.Encoding]::UTF8; [Windows.Forms.Clipboard]::GetText()")
		return []byte(strings.TrimRight(string(out), "\r\n")), err
	}
	// Get-Clipboard -Format Image, saved as PNG and carried as base64
	out, err := ps(ctx, "$i = Get-Clipboard -Format Image; if ($i) { $m = New-Object IO.MemoryStream; $i.Save($m, [Drawing.Imaging.ImageFormat]::Png); [Convert]::ToBase64String($m.ToArray()) }")
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
}
