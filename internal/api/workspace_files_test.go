package api_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// fileRig is a real local target with a real workspace, driven through the
// HTTP API exactly as the browser drives it.
type fileRig struct {
	h       *harness
	root    string
	base    string
	outside string
}

func newFileRig(t *testing.T) *fileRig {
	t.Helper()
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	root := t.TempDir()
	mustRun(t, root, "git", "init", "-q")
	// A live tmux session keeps the session tracked, as a real attachment is.
	tmuxName := fmt.Sprintf("edit-%d", time.Now().UnixNano())
	mustRun(t, root, "tmux", "new-session", "-d", "-s", tmuxName, "bash --norc")
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "edit", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: "edit", Workdir: root, TmuxSession: tmuxName, Status: "idle"})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &fileRig{h: h, root: root, base: fmt.Sprintf("/api/term/session/%d", session.ID), outside: outside}
}

func (f *fileRig) put(name string, body []byte, baseHash string) (int, obj) {
	f.h.t.Helper()
	req, _ := http.NewRequest("PUT", f.h.URL+f.base+"/file?path="+url.QueryEscape(name), bytes.NewReader(body))
	if baseHash != "" {
		req.Header.Set("X-Lectern-Base", baseHash)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out obj
	json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (f *fileRig) read(name string) (int, []byte, string) {
	f.h.t.Helper()
	resp, err := http.Get(f.h.URL + f.base + "/file?path=" + url.QueryEscape(name))
	if err != nil {
		f.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header.Get("X-Lectern-Sha256")
}

func (f *fileRig) op(body obj) (int, obj) {
	f.h.t.Helper()
	code, raw := f.h.request("POST", f.base+"/fileops", body, nil)
	var out obj
	json.Unmarshal(raw, &out)
	return code, out
}

func TestWorkspaceEditSavesWithConflictDetection(t *testing.T) {
	f := newFileRig(t)
	file := filepath.Join(f.root, "main.go")
	os.WriteFile(file, []byte("package main\n"), 0o755)
	code, data, hash := f.read("main.go")
	if code != 200 || string(data) != "package main\n" || len(hash) != 64 {
		t.Fatalf("read: %d %q %q", code, data, hash)
	}
	// A save without saying what it started from is refused outright.
	if code, _ := f.put("main.go", []byte("x"), ""); code != 428 {
		t.Fatalf("missing base: %d", code)
	}
	code, saved := f.put("main.go", []byte("package main\n\nfunc main() {}\n"), hash)
	if code != 200 {
		t.Fatalf("save: %d %v", code, saved)
	}
	if got, _ := os.ReadFile(file); string(got) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("disk: %q", got)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode not preserved: %v", info.Mode())
	}
	// Someone else (an agent) changes the file; saving from the old hash conflicts
	// and reports what is on disk now, leaving the agent's bytes alone.
	os.WriteFile(file, []byte("agent edit\n"), 0o755)
	code, conflict := f.put("main.go", []byte("mine\n"), saved.str("sha256"))
	if code != 409 || conflict.str("sha256") == "" || conflict.str("sha256") == saved.str("sha256") {
		t.Fatalf("conflict: %d %v", code, conflict)
	}
	if got, _ := os.ReadFile(file); string(got) != "agent edit\n" {
		t.Fatalf("conflict overwrote: %q", got)
	}
	// The stat endpoint reports the same hash, so an editor can notice early.
	var st obj
	f.h.decode("GET", f.base+"/stat?path=main.go", nil, 200, &st)
	if st.str("sha256") != conflict.str("sha256") {
		t.Fatalf("stat: %v", st)
	}
	// Deliberate overwrite and create-only semantics.
	if code, _ := f.put("main.go", []byte("mine\n"), "any"); code != 200 {
		t.Fatalf("overwrite: %d", code)
	}
	if code, _ := f.put("main.go", []byte("again\n"), "absent"); code != 409 {
		t.Fatalf("create over existing: %d", code)
	}
	if code, out := f.put("docs/new.md", []byte("# New\n"), "absent"); code != 400 {
		t.Fatalf("create in missing folder: %d %v", code, out)
	}
	os.Mkdir(filepath.Join(f.root, "docs"), 0o755)
	if code, _ := f.put("docs/new.md", []byte("# New\n"), "absent"); code != 200 {
		t.Fatalf("create: %d", code)
	}
	// No staged copies are left behind, in the workspace or the temp folder.
	matches, _ := filepath.Glob(filepath.Join(f.root, "*lectern-save*"))
	staged, _ := filepath.Glob("/tmp/.lectern-edit-*")
	if len(matches) != 0 || len(staged) != 0 {
		t.Fatalf("leftovers: %v %v", matches, staged)
	}
}

func TestWorkspaceWritesStayInsideTheWorkspace(t *testing.T) {
	f := newFileRig(t)
	os.Symlink(f.outside, filepath.Join(f.root, "escape.txt"))
	os.Symlink(filepath.Dir(f.outside), filepath.Join(f.root, "escape-dir"))
	os.WriteFile(filepath.Join(f.root, "inside.txt"), []byte("inside"), 0o644)
	os.Symlink("inside.txt", filepath.Join(f.root, "alias.txt"))
	for _, name := range []string{"../outside.txt", f.outside, "escape.txt", "escape-dir/outside.txt", "escape-dir/new.txt", "sub/../../x.txt"} {
		if code, out := f.put(name, []byte("owned"), "any"); code != 400 {
			t.Fatalf("write %s: %d %v", name, code, out)
		}
	}
	if got, _ := os.ReadFile(f.outside); string(got) != "outside" {
		t.Fatalf("outside changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.outside), "new.txt")); err == nil {
		t.Fatal("created a file through an escaping symlink")
	}
	// A symlink that stays inside writes its real target and remains a link.
	if code, _ := f.put("alias.txt", []byte("via alias"), "any"); code != 200 {
		t.Fatalf("inside alias: %d", code)
	}
	if got, _ := os.ReadFile(filepath.Join(f.root, "inside.txt")); string(got) != "via alias" {
		t.Fatalf("alias target: %q", got)
	}
	if info, _ := os.Lstat(filepath.Join(f.root, "alias.txt")); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("alias replaced by a regular file")
	}
	// Operations: none may leave the workspace or remove its root.
	for _, req := range []obj{
		{"op": "delete", "path": "."}, {"op": "delete", "path": ""}, {"op": "delete", "path": "../outside.txt"},
		{"op": "delete", "path": "escape-dir/outside.txt"}, {"op": "rename", "path": "inside.txt", "to": "../stolen.txt"},
		{"op": "rename", "path": "inside.txt", "to": "escape-dir/stolen.txt"}, {"op": "mkdir", "path": "escape-dir/made"},
		{"op": "create", "path": "../made.txt"}, {"op": "delete", "path": ".git"}, {"op": "chmod", "path": "inside.txt"},
	} {
		if code, out := f.op(req); code != 400 {
			t.Fatalf("%v: %d %v", req, code, out)
		}
	}
	if _, err := os.Stat(f.outside); err != nil {
		t.Fatal("outside file removed")
	}
	// Deleting the escaping link removes the link, never what it points to.
	if code, _ := f.op(obj{"op": "delete", "path": "escape.txt"}); code != 200 {
		t.Fatalf("delete link: %d", code)
	}
	if _, err := os.Lstat(filepath.Join(f.root, "escape.txt")); err == nil {
		t.Fatal("link still present")
	}
	if got, _ := os.ReadFile(f.outside); string(got) != "outside" {
		t.Fatal("link target touched")
	}
}

func TestWorkspaceFileOperations(t *testing.T) {
	f := newFileRig(t)
	steps := []obj{
		{"op": "mkdir", "path": "src"},
		{"op": "create", "path": "src/a.txt"},
		{"op": "mkdir", "path": "src/nested"},
		{"op": "create", "path": "src/nested/b.txt"},
		{"op": "rename", "path": "src/a.txt", "to": "src/nested/a.txt"},
		{"op": "rename", "path": "src/nested", "to": "moved"},
	}
	for _, step := range steps {
		if code, out := f.op(step); code != 200 {
			t.Fatalf("%v: %d %v", step, code, out)
		}
	}
	for _, name := range []string{"moved/a.txt", "moved/b.txt"} {
		if _, err := os.Stat(filepath.Join(f.root, name)); err != nil {
			t.Fatalf("missing %s", name)
		}
	}
	if code, _ := f.op(obj{"op": "create", "path": "moved/a.txt"}); code != 400 {
		t.Fatal("create replaced an existing file")
	}
	if code, _ := f.op(obj{"op": "rename", "path": "moved/a.txt", "to": "moved/b.txt"}); code != 400 {
		t.Fatal("rename replaced an existing file")
	}
	if code, _ := f.op(obj{"op": "rename", "path": "moved", "to": "moved/inner"}); code != 400 {
		t.Fatal("folder moved into itself")
	}
	if code, _ := f.op(obj{"op": "delete", "path": "moved"}); code != 200 {
		t.Fatal("recursive delete")
	}
	if _, err := os.Stat(filepath.Join(f.root, "moved")); err == nil {
		t.Fatal("folder still present")
	}
	// Folder download: a zip of the folder, never following an escaping link.
	os.MkdirAll(filepath.Join(f.root, "pack", "deep"), 0o755)
	os.WriteFile(filepath.Join(f.root, "pack", "one.txt"), []byte("one"), 0o644)
	os.WriteFile(filepath.Join(f.root, "pack", "deep", "two.txt"), []byte("two"), 0o644)
	os.Symlink(f.outside, filepath.Join(f.root, "pack", "leak.txt"))
	resp, err := http.Get(f.h.URL + f.base + "/archive?path=pack")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("archive: %v %v", err, resp)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "pack.zip") {
		t.Fatalf("name: %s", resp.Header.Get("Content-Disposition"))
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range archive.File {
		names = append(names, file.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "deep/two.txt,one.txt" {
		t.Fatalf("archive entries: %v", names)
	}
	if code, _ := f.h.request("GET", f.base+"/archive?path=..", nil, nil); code != 400 {
		t.Fatal("archived outside the workspace")
	}
}

func TestWorkspaceWriteSizeCap(t *testing.T) {
	f := newFileRig(t)
	if code, _ := f.put("big.bin", make([]byte, (25<<20)+1), "any"); code != 413 {
		t.Fatalf("oversize write: %d", code)
	}
	if _, err := os.Stat(filepath.Join(f.root, "big.bin")); err == nil {
		t.Fatal("oversize file written")
	}
}

func TestWorkspaceIndexStatusAndSearch(t *testing.T) {
	f := newFileRig(t)
	write := func(name, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(f.root, name)), 0o755)
		os.WriteFile(filepath.Join(f.root, name), []byte(body), 0o644)
	}
	write(".gitignore", "build/\n*.log\n")
	write("src/server.go", "package src\n\nfunc HandleRequest() {}\nfunc handlerequest() {}\n")
	write("README.md", "Handle it.\nhandled\n")
	mustRun(t, f.root, "git", "add", ".")
	mustRun(t, f.root, "git", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "init")
	write("src/server.go", "package src\n\nfunc HandleRequest() {}\nfunc handlerequest() {}\n// changed\n")
	write("notes.txt", "Handle new\n")
	write("build/out.js", "HandleRequest()\n")
	write("debug.log", "HandleRequest\n")
	os.Symlink(filepath.Dir(f.outside), filepath.Join(f.root, "linked"))
	os.WriteFile(f.outside, []byte("HandleRequest outside\n"), 0o600)

	var index struct {
		Files, Ignored []string
		Source         string
	}
	f.h.decode("GET", f.base+"/index", nil, 200, &index)
	sort.Strings(index.Files)
	if index.Source != "git" || strings.Join(index.Files, ",") != ".gitignore,README.md,linked,notes.txt,src/server.go" {
		t.Fatalf("index: %+v", index)
	}
	sort.Strings(index.Ignored)
	if strings.Join(index.Ignored, ",") != "build/out.js,debug.log" {
		t.Fatalf("ignored: %+v", index.Ignored)
	}

	var status struct {
		Repository bool
		Status     map[string]string
	}
	f.h.decode("GET", f.base+"/git-status", nil, 200, &status)
	if !status.Repository || status.Status["src/server.go"] != "modified" || status.Status["notes.txt"] != "untracked" || status.Status["build/"] != "ignored" {
		t.Fatalf("status: %+v", status)
	}

	type hit struct {
		Path         string
		Line, Column int
		Text         string
	}
	search := func(query string) []hit {
		var out struct{ Results []hit }
		f.h.decode("GET", f.base+"/search?"+query, nil, 200, &out)
		return out.Results
	}
	// Fixed string, case-insensitive by default; ignored files and the
	// escaping symlink never appear.
	hits := search("q=handlerequest")
	var where []string
	for _, h := range hits {
		where = append(where, fmt.Sprintf("%s:%d:%d", h.Path, h.Line, h.Column))
	}
	sort.Strings(where)
	if strings.Join(where, ",") != "src/server.go:3:6,src/server.go:4:6" {
		t.Fatalf("insensitive: %v", where)
	}
	if hits := search("q=HandleRequest&case=1"); len(hits) != 1 || hits[0].Line != 3 {
		t.Fatalf("case: %+v", hits)
	}
	if hits := search("q=" + url.QueryEscape("Handle\\w+\\(") + "&regex=1&case=1"); len(hits) != 1 || hits[0].Path != "src/server.go" {
		t.Fatalf("regex: %+v", hits)
	}
	if hits := search("q=handle&word=1"); len(hits) != 2 {
		// "Handle it." and "Handle new"; never "handled" or HandleRequest.
		t.Fatalf("word: %+v", hits)
	}
	if hits := search("q=handle&include=*.md"); len(hits) != 2 || hits[0].Path != "README.md" {
		t.Fatalf("include: %+v", hits)
	}
	if code, _ := f.h.request("GET", f.base+"/search?q="+url.QueryEscape("(")+"&regex=1", nil, nil); code != 400 {
		t.Fatal("invalid regex accepted")
	}
	if code, _ := f.h.request("GET", f.base+"/search?q=", nil, nil); code != 400 {
		t.Fatal("empty search accepted")
	}
}

// Quick Open's server half: a 5,000-file repository is listed well inside
// the budget that leaves the browser its share of 200 ms.
func TestWorkspaceIndexScalesToFiveThousandFiles(t *testing.T) {
	f := newFileRig(t)
	for i := 0; i < 5000; i++ {
		dir := filepath.Join(f.root, fmt.Sprintf("pkg%02d", i%50))
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%04d.go", i)), []byte("package x\n"), 0o644)
	}
	mustRun(t, f.root, "git", "add", ".")
	var best time.Duration
	for attempt := 0; attempt < 3; attempt++ {
		started := time.Now()
		var index struct{ Files []string }
		f.h.decode("GET", f.base+"/index", nil, 200, &index)
		if len(index.Files) != 5000 {
			t.Fatalf("files: %d", len(index.Files))
		}
		if took := time.Since(started); best == 0 || took < best {
			best = took
		}
	}
	t.Logf("index of 5,000 files: best %s", best)
	if best > 2*time.Second {
		t.Fatalf("index too slow: %s", best)
	}
}

func TestWorkspaceWritesNeedAHuman(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Auth = "tailscale"; c.TailscaleUsers = "nobody@example.com" })
	req, _ := http.NewRequest("PUT", h.URL+"/api/term/session/1/file?path=x", strings.NewReader("x"))
	req.Header.Set("X-Lectern-Base", "any")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("local write: %d", resp.StatusCode)
	}
	if code, body := h.request("POST", "/api/term/session/1/fileops", obj{"op": "delete", "path": "x"}, nil); code != 403 {
		t.Fatalf("local delete: %d %s", code, body)
	}
}

func TestWorkspaceFileEndpointsNeedAuth(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AuthToken = "files-secret"; c.Auth = "token" })
	for _, p := range []string{"/stat?path=x", "/index", "/git-status", "/search?q=x", "/archive?path=x"} {
		if code, _ := h.request("GET", "/api/term/session/1"+p, nil, nil); code != 401 {
			t.Fatalf("ungated %s: %d", p, code)
		}
	}
	for _, method := range []string{"PUT /file?path=x", "POST /fileops"} {
		parts := strings.SplitN(method, " ", 2)
		if code, _ := h.request(parts[0], "/api/term/session/1"+parts[1], obj{}, nil); code != 401 {
			t.Fatalf("ungated %s: %d", method, code)
		}
	}
}

// Without git (and without ripgrep on this target), Quick Open walks the tree
// and search falls back to a plain scan with the same options and limits.
func TestWorkspaceIndexAndSearchWithoutGit(t *testing.T) {
	f := newFileRig(t)
	os.RemoveAll(filepath.Join(f.root, ".git"))
	os.MkdirAll(filepath.Join(f.root, "src"), 0o755)
	os.MkdirAll(filepath.Join(f.root, "node_modules", "dep"), 0o755)
	os.WriteFile(filepath.Join(f.root, "src", "main.go"), []byte("package main\n// Needle here\n"), 0o644)
	os.WriteFile(filepath.Join(f.root, "node_modules", "dep", "x.js"), []byte("needle\n"), 0o644)
	os.Symlink(filepath.Dir(f.outside), filepath.Join(f.root, "linked"))
	os.WriteFile(f.outside, []byte("needle outside\n"), 0o600)
	var index struct {
		Files  []string
		Source string
	}
	f.h.decode("GET", f.base+"/index", nil, 200, &index)
	if index.Source != "walk" && index.Source != "ripgrep" {
		t.Fatalf("source: %+v", index)
	}
	joined := strings.Join(index.Files, ",")
	if !strings.Contains(joined, "src/main.go") || strings.Contains(joined, "outside") {
		t.Fatalf("files: %v", index.Files)
	}
	var out struct {
		Results []struct {
			Path string
			Line int
		}
		Source string
	}
	f.h.decode("GET", f.base+"/search?q=needle", nil, 200, &out)
	if len(out.Results) != 1 || out.Results[0].Path != "src/main.go" || out.Results[0].Line != 2 {
		t.Fatalf("search: %+v", out)
	}
	f.h.decode("GET", f.base+"/search?q=needle&ignored=1", nil, 200, &out)
	if len(out.Results) != 2 {
		t.Fatalf("search with ignored: %+v", out)
	}
}

// The watch long-poll answers when a shown folder changes, and waits out its
// timeout when nothing does.
func TestWorkspaceWatchReportsChanges(t *testing.T) {
	f := newFileRig(t)
	os.MkdirAll(filepath.Join(f.root, "src"), 0o755)
	var first obj
	f.h.decode("POST", f.base+"/watch", obj{"dirs": []string{".", "src"}, "timeout": 5}, 200, &first)
	token := first.str("token")
	if token == "" || first["changed"] != false {
		t.Fatalf("first: %v", first)
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		os.WriteFile(filepath.Join(f.root, "src", "new.go"), []byte("x"), 0o644)
	}()
	started := time.Now()
	var changed obj
	f.h.decode("POST", f.base+"/watch", obj{"dirs": []string{".", "src"}, "token": token, "timeout": 10}, 200, &changed)
	if changed["changed"] != true || changed.str("token") == token || time.Since(started) > 5*time.Second {
		t.Fatalf("change: %v after %s", changed, time.Since(started))
	}
	t.Logf("watch mode %s, change seen after %s", changed.str("mode"), time.Since(started))
	var quiet obj
	started = time.Now()
	f.h.decode("POST", f.base+"/watch", obj{"dirs": []string{"."}, "token": "", "timeout": 1}, 200, &quiet)
	f.h.decode("POST", f.base+"/watch", obj{"dirs": []string{"."}, "token": quiet.str("token"), "timeout": 1}, 200, &quiet)
	if quiet["changed"] != false || time.Since(started) < 900*time.Millisecond {
		t.Fatalf("quiet: %v", quiet)
	}
	if code, _ := f.h.request("POST", f.base+"/watch", obj{"dirs": []string{"../.."}, "token": "x", "timeout": 1}, nil); code != 200 {
		t.Fatalf("outside dirs are skipped, not an error: %d", code)
	}
}
