package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestFilesSidebarWidth(t *testing.T) {
	cases := []struct{ window, remembered, want int }{
		{100, 0, 28},  // 28%
		{200, 0, 44},  // capped
		{120, 0, 33},  //
		{100, 70, 50}, // a dragged width is kept, but never more than half
		{140, 30, 30},
		{100, 10, 24}, // never below the minimum
	}
	for _, c := range cases {
		if got := filesSidebarWidth(c.window, c.remembered); got != c.want {
			t.Errorf("filesSidebarWidth(%d, %d) = %d, want %d", c.window, c.remembered, got, c.want)
		}
	}
}

func TestFuzzyFilesPrefersWholeNameMatches(t *testing.T) {
	files := []string{"docs/memory/context.md", "go/internal/api/memory_context.go", "go/internal/api/memory_context_test.go", "README.md"}
	got := fuzzyFiles(files, "memory_context", 10)
	if len(got) != 2 || got[0] != "go/internal/api/memory_context.go" {
		t.Fatalf("whole-name match should rank first and the shorter path ahead: %v", got)
	}
	got = fuzzyFiles(files, "memctx", 10)
	if len(got) < 2 || !strings.Contains(got[0], "memory_context") {
		t.Fatalf("scattered letters should still find the file: %v", got)
	}
	if got := fuzzyFiles(files, "zzz", 10); len(got) != 0 {
		t.Fatalf("no match expected: %v", got)
	}
}

func TestFolderStatesShowMostImportantChange(t *testing.T) {
	states := folderStates(map[string]string{
		"src/a.go":        "untracked",
		"src/lib/b.go":    "modified",
		"src/lib/c.go":    "conflict",
		"build/":          "ignored",
		"docs/readme.txt": "added",
	})
	if states["src"] != "conflict" || states["src/lib"] != "conflict" || states["docs"] != "added" {
		t.Fatalf("folder states = %v", states)
	}
	if _, ok := states["build"]; ok {
		t.Fatal("an ignored entry must not colour its parent")
	}
}

func TestFilesRowsHideGitAndIgnoredUntilAsked(t *testing.T) {
	m := &filesModel{
		listings: map[string][]fileEntry{".": {
			{Name: ".git", Path: ".git", Directory: true},
			{Name: "build", Path: "build", Directory: true},
			{Name: "src", Path: "src", Directory: true},
			{Name: "main.go", Path: "main.go"},
		}},
		expanded: map[string]bool{".": true}, failed: map[string]string{},
		status:   map[string]string{"build/": "ignored", "main.go": "modified"},
		dirState: map[string]string{}, touched: map[string]bool{},
		height: 20,
	}
	m.rebuild()
	var names []string
	for _, row := range m.rows {
		names = append(names, row.entry.Name)
	}
	if strings.Join(names, ",") != "src,main.go" {
		t.Fatalf("rows = %v", names)
	}
	m.showIgnored = true
	m.rebuild()
	if len(m.rows) != 4 {
		t.Fatalf("with ignored shown, rows = %d", len(m.rows))
	}
	m.view = viewChanges
	m.rebuild()
	if len(m.rows) != 1 || m.rows[0].entry.Path != "main.go" {
		t.Fatalf("changes view = %+v", m.rows)
	}
}

func TestSanitizeLineDropsTerminalControls(t *testing.T) {
	if got := sanitizeLine("ok\x1b]52;c;aGk=\x07 \u009b2J done"); got != "ok]52;c;aGk= 2J done" {
		t.Fatalf("sanitizeLine = %q", got)
	}
}

// fakeWorkspace serves one file through the files API.
type fakeWorkspace struct {
	mu      sync.Mutex
	content string
	sha     string
	writes  []string // the X-Lectern-Base of each save
}

func (f *fakeWorkspace) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/api/term/session/7/file" || r.URL.Query().Get("path") != "notes.txt" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case "GET":
			w.Header().Set("X-Lectern-Sha256", f.sha)
			io.WriteString(w, f.content)
		case "PUT":
			base := r.Header.Get("X-Lectern-Base")
			f.writes = append(f.writes, base)
			if base != "any" && base != f.sha {
				w.WriteHeader(409)
				io.WriteString(w, `{"sha256":"`+f.sha+`"}`)
				return
			}
			data, _ := io.ReadAll(r.Body)
			f.content, f.sha = string(data), "new"
			io.WriteString(w, `{}`)
		}
	})
}

func TestEditWorkspaceFileSavesWithTheHashItOpened(t *testing.T) {
	ws := &fakeWorkspace{content: "one\n", sha: "aaa"}
	srv := httptest.NewServer(ws.handler(t))
	defer srv.Close()
	t.Setenv("VISUAL", "sed -i s/one/two/")
	var out bytes.Buffer
	if err := editWorkspaceFile(newFilesAPI("session", "7", srv.URL, ""), "notes.txt", strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if ws.content != "two\n" || len(ws.writes) != 1 || ws.writes[0] != "aaa" {
		t.Fatalf("content %q writes %v", ws.content, ws.writes)
	}
}

func TestEditWorkspaceFileUnchangedDoesNotWrite(t *testing.T) {
	ws := &fakeWorkspace{content: "one\n", sha: "aaa"}
	srv := httptest.NewServer(ws.handler(t))
	defer srv.Close()
	t.Setenv("VISUAL", "true")
	if err := editWorkspaceFile(newFilesAPI("session", "7", srv.URL, ""), "notes.txt", strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(ws.writes) != 0 {
		t.Fatalf("an unchanged file must not be saved: %v", ws.writes)
	}
}

func TestEditWorkspaceFileAsksOnConflict(t *testing.T) {
	ws := &fakeWorkspace{content: "one\n", sha: "aaa"}
	srv := httptest.NewServer(ws.handler(t))
	defer srv.Close()
	// The "agent" rewrites the file while the editor is open.
	t.Setenv("VISUAL", "sed -i s/one/mine/")
	api := newFilesAPI("session", "7", srv.URL, "")
	api.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PUT" && len(ws.writes) == 0 {
			ws.mu.Lock()
			ws.content, ws.sha = "theirs\n", "bbb"
			ws.mu.Unlock()
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := editWorkspaceFile(api, "notes.txt", strings.NewReader("k\n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if ws.content != "theirs\n" || !strings.Contains(out.String(), "changed on disk") {
		t.Fatalf("keep theirs: content %q, said %q", ws.content, out.String())
	}

	ws.content, ws.sha, ws.writes = "one\n", "aaa", nil
	out.Reset()
	if err := editWorkspaceFile(api, "notes.txt", strings.NewReader("o\n"), &out); err != nil {
		t.Fatal(err)
	}
	if ws.content != "mine\n" || len(ws.writes) != 2 || ws.writes[1] != "any" {
		t.Fatalf("overwrite: content %q writes %v", ws.content, ws.writes)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAttachConfigBindsTheFilePane(t *testing.T) {
	plan, err := newNativeWrapPlan(t.TempDir(), "/tmp/sock", nativeControls{Kind: "session", ID: "4", Base: "http://127.0.0.1:9110"}, []string{"tmux", "attach"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.conf, "bind-key -T prefix f run-shell -b") || !strings.Contains(plan.conf, "toggle '#{window_id}' '#{window_width}' '#{client_name}'") {
		t.Fatalf("no Ctrl+] f binding in:\n%s", plan.conf)
	}
	if !strings.Contains(plan.filesBody, terminalFilesFlag) || !strings.Contains(plan.filesBody, insertSocketEnv+"=/tmp/sock") {
		t.Fatalf("files script = %q", plan.filesBody)
	}
}
