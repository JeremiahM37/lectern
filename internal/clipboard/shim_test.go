package clipboard

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWlPaste(t *testing.T) {
	cases := []struct {
		args []string
		want Command
	}{
		{[]string{"-l"}, Command{Tool: "wl-paste", Mode: ModeList}},
		{[]string{"--list-types"}, Command{Tool: "wl-paste", Mode: ModeList}},
		{[]string{"--type", "image/png"}, Command{Tool: "wl-paste", Mode: ModeRead, Type: "image/png"}},
		{[]string{"-t", "image/jpg"}, Command{Tool: "wl-paste", Mode: ModeRead, Type: "image/jpeg"}},
		{[]string{"--type=image/webp"}, Command{Tool: "wl-paste", Mode: ModeRead, Type: "image/webp"}},
		{[]string{"-n"}, Command{Tool: "wl-paste", Mode: ModeRead, Type: "text/plain", NoNewline: true}},
		{nil, Command{Tool: "wl-paste", Mode: ModeRead, Type: "text/plain"}},
		{[]string{"-t", "text"}, Command{Tool: "wl-paste", Mode: ModeRead, Type: "text/plain"}},
		{[]string{"-p"}, Command{Tool: "wl-paste", Mode: ModeRead, Type: "text/plain", Primary: true}},
		{[]string{"-nl"}, Command{Tool: "wl-paste", Mode: ModeList, NoNewline: true}},
		{[]string{"--watch", "cat"}, Command{Tool: "wl-paste", Mode: ModeOther}},
		{[]string{"--type"}, Command{Tool: "wl-paste", Mode: ModeOther}},
		{[]string{"--bogus"}, Command{Tool: "wl-paste", Mode: ModeOther}},
	}
	for _, c := range cases {
		if got := Parse("/some/dir/wl-paste", c.args); got != c.want {
			t.Errorf("wl-paste %v: got %+v want %+v", c.args, got, c.want)
		}
	}
}

func TestParseXclip(t *testing.T) {
	cases := []struct {
		args []string
		want Command
	}{
		{[]string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, Command{Tool: "xclip", Mode: ModeList}},
		{[]string{"-selection", "clipboard", "-t", "image/png", "-o"}, Command{Tool: "xclip", Mode: ModeRead, Type: "image/png"}},
		{[]string{"-sel", "clip", "-o"}, Command{Tool: "xclip", Mode: ModeRead, Type: "text/plain", Primary: true}},
		{[]string{"-selection", "primary", "-o"}, Command{Tool: "xclip", Mode: ModeRead, Type: "text/plain", Primary: true}},
		{[]string{"-selection", "clipboard"}, Command{Tool: "xclip", Mode: ModeWrite}},
		{[]string{"-i"}, Command{Tool: "xclip", Mode: ModeWrite}},
		{[]string{"-selection", "clipboard", "-t", "image/png", "-i", "x.png"}, Command{Tool: "xclip", Mode: ModeOther}},
		{[]string{"-selection"}, Command{Tool: "xclip", Mode: ModeOther}},
		{[]string{"-out", "-selection", "clipboard", "-target", "image/jpeg"}, Command{Tool: "xclip", Mode: ModeRead, Type: "image/jpeg"}},
	}
	for _, c := range cases {
		got := Parse("xclip", c.args)
		// "clip" is not "clipboard": xclip treats an unknown abbreviation
		// as not-the-clipboard, which the shim must not serve
		if got != c.want {
			t.Errorf("xclip %v: got %+v want %+v", c.args, got, c.want)
		}
	}
}

func fakeServer(t *testing.T, status int, types string, body string) (url string, seen *struct{ Auth, Body string }) {
	seen = &struct{ Auth, Body string }{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Auth = r.Header.Get("Authorization")
		var b bytes.Buffer
		b.ReadFrom(r.Body)
		seen.Body = b.String()
		if types != "" {
			w.Header().Set("X-Clipboard-Types", types)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, seen
}

func runShim(tool string, args []string, env []string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = Run(context.Background(), tool, args, env, Streams{In: strings.NewReader(""), Out: &o, Err: &e}, nil)
	return code, o.String(), e.String()
}

func TestShimReadsFromServer(t *testing.T) {
	png := "\x89PNG\r\n\x1a\nDATA"
	url, seen := fakeServer(t, 200, "image/png,text/plain", png)
	env := []string{EnvHookURL + "=" + url, EnvHookToken + "=tok", "PATH=/nonexistent"}
	code, out, _ := runShim("wl-paste", []string{"--type", "image/png"}, env)
	if code != 0 || out != png {
		t.Fatalf("read: %d %q", code, out)
	}
	if seen.Auth != "Bearer tok" || !strings.Contains(seen.Body, `"image/png"`) || !strings.Contains(seen.Body, `"read"`) {
		t.Fatalf("request: %+v", seen)
	}
	code, out, _ = runShim("xclip", []string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, env)
	if code != 0 || out != "image/png\ntext/plain\n" {
		t.Fatalf("list: %d %q", code, out)
	}
}

func TestShimUnavailableBehavesLikeTheRealTools(t *testing.T) {
	url, _ := fakeServer(t, 404, "", `{"detail":"x"}`)
	env := []string{EnvHookURL + "=" + url, EnvHookToken + "=tok", "PATH=/nonexistent"}
	if code, _, e := runShim("wl-paste", []string{"-l"}, env); code != 1 || !strings.Contains(e, "No selection") {
		t.Fatalf("wl-paste -l: %d %q", code, e)
	}
	if code, _, e := runShim("wl-paste", []string{"--type", "image/png"}, env); code != 1 || e == "" {
		t.Fatalf("wl-paste type: %d %q", code, e)
	}
	if code, _, e := runShim("xclip", []string{"-selection", "clipboard", "-t", "image/png", "-o"}, env); code != 1 || !strings.Contains(e, "not available") {
		t.Fatalf("xclip: %d %q", code, e)
	}
	// no session environment at all
	if code, _, _ := runShim("wl-paste", []string{"-l"}, []string{"PATH=/nonexistent"}); code != 1 {
		t.Fatalf("no env: %d", code)
	}
}

func TestShimNeverAsksForUnsupportedThings(t *testing.T) {
	url, seen := fakeServer(t, 200, "text/plain", "x")
	env := []string{EnvHookURL + "=" + url, EnvHookToken + "=tok", "PATH=/nonexistent"}
	for _, args := range [][]string{{"--primary"}, {"--type", "application/x-secret"}, {"--watch", "true"}} {
		seen.Body = ""
		runShim("wl-paste", args, env)
		if seen.Body != "" {
			t.Errorf("wl-paste %v reached the server", args)
		}
	}
	// a write is a no-op that drains stdin
	var o, e bytes.Buffer
	code := Run(context.Background(), "xclip", []string{"-selection", "clipboard"}, env,
		Streams{In: strings.NewReader("copied"), Out: &o, Err: &e}, nil)
	if code != 0 || seen.Body != "" {
		t.Fatalf("write: %d %q", code, seen.Body)
	}
}

func TestShimFallsBackToTheRealToolWhenThereIsADisplay(t *testing.T) {
	dir := t.TempDir()
	shims := filepath.Join(dir, "shims")
	os.MkdirAll(shims, 0o755)
	real := filepath.Join(dir, "real")
	os.MkdirAll(real, 0o755)
	os.WriteFile(filepath.Join(real, "wl-paste"), []byte("#!/bin/sh\necho REAL\n"), 0o755)
	os.WriteFile(filepath.Join(shims, "wl-paste"), []byte("#!/bin/sh\necho SHIM\n"), 0o755)
	env := []string{"PATH=" + shims + ":" + real, EnvShimDir + "=" + shims}
	if _, out, _ := runShim("wl-paste", []string{"-l"}, env); out != "" {
		t.Fatalf("no display: %q", out)
	}
	env = append(env, "WAYLAND_DISPLAY=wayland-1")
	if _, out, _ := runShim("wl-paste", []string{"-l"}, env); strings.TrimSpace(out) != "REAL" {
		t.Fatalf("display: %q", out)
	}
}

func TestWriteShimsIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	for i := 0; i < 2; i++ {
		if err := WriteShims(dir, "/usr/local/bin/lectern"); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "xclip"))
	if !strings.Contains(string(b), `clipboard shim xclip "$@"`) || !strings.Contains(string(b), "/usr/local/bin/lectern") {
		t.Fatalf("%s", b)
	}
}
