package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func (f *fileRig) get(p string) (int, []byte, http.Header) {
	f.h.t.Helper()
	resp, err := http.Get(f.h.URL + f.base + p)
	if err != nil {
		f.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header
}

// A bare file name an agent prints is only a link if it names a workspace
// file; exists answers without reading it, and never looks outside.
func TestTerminalLinkExists(t *testing.T) {
	f := newFileRig(t)
	if err := os.WriteFile(filepath.Join(f.root, "report.pdf"), []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"report.pdf": true, "./report.pdf": true, "Jeremiah_Mackey_Cerebras.pdf": false} {
		code, raw, _ := f.get("/exists?path=" + url.QueryEscape(name))
		var out struct{ Exists bool }
		json.Unmarshal(raw, &out)
		if code != 200 || out.Exists != want {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
	}
	if code, raw, _ := f.get("/exists?path=" + url.QueryEscape("../"+filepath.Base(filepath.Dir(f.outside)))); code != 400 {
		t.Fatalf("outside through exists: %d %s", code, raw)
	}
}

// An absolute or ~/ path outside the workspace opens read-only for a person:
// the bytes, the real path, and the same size cap as workspace files.
func TestOutsideWorkspaceFilesOpenReadOnly(t *testing.T) {
	f := newFileRig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".formwork", "application-testing-20260927")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4\n% résumé\n")
	name := filepath.Join(dir, "Jeremiah_Mackey_Cerebras.pdf")
	if err := os.WriteFile(name, pdf, 0o600); err != nil {
		t.Fatal(err)
	}

	code, raw, header := f.get("/external?path=" + url.QueryEscape(name))
	if code != 200 || string(raw) != string(pdf) {
		t.Fatalf("absolute: %d %q", code, raw)
	}
	if got, _ := url.PathUnescape(header.Get("X-Lectern-Path")); got != name {
		t.Fatalf("real path %q", got)
	}
	if !strings.Contains(header.Get("Content-Disposition"), "Jeremiah_Mackey_Cerebras.pdf") {
		t.Fatalf("disposition %q", header.Get("Content-Disposition"))
	}
	code, raw, _ = f.get("/external?path=" + url.QueryEscape("~/.formwork/application-testing-20260927/Jeremiah_Mackey_Cerebras.pdf"))
	if code != 200 || string(raw) != string(pdf) {
		t.Fatalf("~/ path: %d %q", code, raw)
	}

	code, raw, _ = f.get("/external-stat?path=" + url.QueryEscape(name))
	var st struct {
		Exists, Regular bool
		Size            int
		Path            string
	}
	json.Unmarshal(raw, &st)
	if code != 200 || !st.Exists || !st.Regular || st.Size != len(pdf) || st.Path != name {
		t.Fatalf("stat: %d %s", code, raw)
	}
	code, raw, _ = f.get("/external-stat?path=" + url.QueryEscape(dir))
	json.Unmarshal(raw, &st)
	if code != 200 || !st.Exists || st.Regular {
		t.Fatalf("folder stat: %d %s", code, raw)
	}
	code, raw, _ = f.get("/external-stat?path=" + url.QueryEscape(filepath.Join(dir, "missing.pdf")))
	if code != 200 || !strings.Contains(string(raw), `"exists":false`) {
		t.Fatalf("missing stat: %d %s", code, raw)
	}

	// Refusals: a folder, a missing file, a relative path, a device, and a file over the cap.
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, 25<<20+1); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		dir:                               "regular file",
		filepath.Join(dir, "missing.pdf"): "No such file",
		"notes/todo.md":                   "absolute or ~/",
		"/dev/zero":                       "regular file",
		big:                               "25 MiB",
	} {
		code, raw, _ := f.get("/external?path=" + url.QueryEscape(p))
		if code != 400 || !strings.Contains(string(raw), want) {
			t.Fatalf("%s: %d %s", p, code, raw)
		}
	}
}

// Reading outside the workspace is for people: in tailscale mode a process on
// this machine (an agent) is refused, and without a token nobody gets in.
func TestOutsideWorkspaceFilesNeedAHuman(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Auth = "tailscale"; c.TailscaleUsers = "nobody@example.com" })
	for _, p := range []string{"/external?path=%2Fetc%2Fhosts", "/external-stat?path=%2Fetc%2Fhosts"} {
		if code, body := h.request("GET", "/api/term/session/1"+p, nil, nil); code != 403 {
			t.Fatalf("local %s: %d %s", p, code, body)
		}
	}
	h = newHarness(t, func(c *config.Config) { c.AuthToken = "files-secret"; c.Auth = "token" })
	for _, p := range []string{"/exists?path=x", "/external?path=%2Fetc%2Fhosts", "/external-stat?path=%2Fetc%2Fhosts"} {
		if code, _ := h.request("GET", "/api/term/session/1"+p, nil, nil); code != 401 {
			t.Fatalf("ungated %s: %d", p, code)
		}
	}
}
