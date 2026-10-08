package clipboard

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Reader reads the clipboard of the machine the client runs on.
type Reader interface {
	// Types lists the MIME types on the clipboard, normalized to the ones the
	// bridge serves (image/* and text/plain). Empty when it is empty.
	Types(ctx context.Context) ([]string, error)
	// Read returns one type's bytes.
	Read(ctx context.Context, mime string) ([]byte, error)
}

// cleanEnv is the environment for a clipboard tool: the Lectern shim
// directory is removed from PATH so the client never calls its own shim.
func cleanEnv() []string {
	shims := os.Getenv(EnvShimDir)
	env := os.Environ()
	if shims == "" {
		return env
	}
	out := env[:0:0]
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			var keep []string
			for _, d := range filepath.SplitList(e[5:]) {
				if filepath.Clean(d) != filepath.Clean(shims) {
					keep = append(keep, d)
				}
			}
			e = "PATH=" + strings.Join(keep, string(os.PathListSeparator))
		}
		out = append(out, e)
	}
	return out
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = cleanEnv()
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Servable filters a raw type list to what the bridge carries, in a stable
// order: images first.
func Servable(raw []string) []string {
	var imgs, txt []string
	seen := map[string]bool{}
	for _, t := range raw {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "image/jpg" {
			t = "image/jpeg"
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		switch {
		case strings.HasPrefix(t, "image/") && AllowedType(t):
			imgs = append(imgs, t)
		case t == "text/plain" || t == "text/plain;charset=utf-8" || t == "utf8_string" || t == "string" || t == "text":
			if len(txt) == 0 {
				txt = append(txt, "text/plain")
			}
		}
	}
	return append(imgs, txt...)
}

// IsImage reports whether the type is a servable image.
func IsImage(t string) bool { return strings.HasPrefix(t, "image/") }
