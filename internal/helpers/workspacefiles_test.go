//go:build unix

package helpers

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// workspaceFixture is a repository workspace with the cases the file views
// guard against: links inside and out of it, a FIFO, an oversized file,
// bookkeeping names, ignored folders and awkward names.
func workspaceFixture(t *testing.T, root string) string {
	t.Helper()
	ws := filepath.Join(root, "ws")
	gitFixture(t, ws, map[string]string{
		"README.md":         "# Title\nhello world\nHello again\n",
		"src/main.go":       "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"src/util/x.txt":    "needle in text\nanother needle\n",
		".gitignore":        "node_modules/\nbuild/\n*.log\n",
		"Zeta.txt":          "zeta\n",
		"alpha.txt":         "alpha ÄÖ needle\n",
		"docs/guide.md":     "the needle word\nneedles\n",
		"bin/data.bin":      "\x00\x01needle",
		".lectern-lock":     "lock",
		".lectern-write-a1": "tmp",
	})
	os.MkdirAll(ws+"/node_modules/pkg/lib", 0o755)
	os.WriteFile(ws+"/node_modules/pkg/index.js", []byte("needle()\n"), 0o644)
	os.WriteFile(ws+"/node_modules/pkg/lib/b.js", []byte("x\n"), 0o644)
	os.MkdirAll(ws+"/build", 0o755)
	os.WriteFile(ws+"/build/out.o", []byte("needle"), 0o644)
	os.WriteFile(ws+"/debug.log", []byte("log needle\n"), 0o644)
	os.WriteFile(ws+"/untracked.txt", []byte("NEEDLE upper\n"), 0o644)
	os.WriteFile(ws+"/caf\xe9.txt", []byte("latin needle\n"), 0o644)
	os.WriteFile(ws+"/é.txt", []byte("utf needle\n"), 0o644)
	os.MkdirAll(ws+"/empty", 0o755)
	os.Symlink("README.md", ws+"/inlink")
	os.Symlink("src", ws+"/indir")
	os.WriteFile(root+"/outside.txt", []byte("secret\n"), 0o644)
	os.Symlink(root+"/outside.txt", ws+"/outlink")
	os.Symlink(root, ws+"/outdir")
	os.Symlink("missing-target", ws+"/dangling")
	syscall.Mkfifo(ws+"/fifo", 0o644)
	big, _ := os.Create(ws + "/big.bin")
	big.Truncate(26 << 20)
	big.Close()
	os.WriteFile(ws+"/src/main.go", []byte("package main\n\nfunc main() {\n\tprintln(\"hello needle\")\n}\n"), 0o644)
	os.Remove(ws + "/Zeta.txt")
	return ws
}

var mtimeField = regexp.MustCompile(`"mtime": \d+`)

// sameArchive compares two archive replies by what the zip holds.
func sameArchive(t *testing.T, py, goRes runResult) {
	t.Helper()
	contents := func(out string) string {
		var reply struct {
			Data string `json:"data"`
		}
		if json.Unmarshal([]byte(out), &reply) != nil || reply.Data == "" {
			return out
		}
		raw, _ := base64.StdEncoding.DecodeString(reply.Data)
		z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, f := range z.File {
			r, _ := f.Open()
			body, _ := io.ReadAll(r)
			r.Close()
			lines = append(lines, fmt.Sprintf("%s %v %s %q", f.Name, f.Mode(), f.Modified.Format(time.DateTime), body))
		}
		sort.Strings(lines)
		return strings.Join(lines, "\n")
	}
	if py.rc != goRes.rc || contents(py.stdout) != contents(goRes.stdout) {
		t.Fatalf("archives differ\npython (rc %d):\n%s\ngo (rc %d):\n%s", py.rc, contents(py.stdout), goRes.rc, contents(goRes.stdout))
	}
}

func TestWorkspaceFilesReadOnlyParity(t *testing.T) {
	script := pythonFile(t, "api/workspace_files.py")
	root := t.TempDir()
	ws := workspaceFixture(t, root)
	plain := filepath.Join(root, "plain")
	os.MkdirAll(plain+"/sub", 0o755)
	os.WriteFile(plain+"/sub/a.txt", []byte("plain needle\n"), 0o644)
	os.WriteFile(plain+"/b.md", []byte("Needle\n"), 0o644)
	spec := runSpec{dir: root, env: append([]string{"HOME=" + root}, gitEnv...)}
	type call struct {
		ws   string
		args []string
	}
	calls := []call{}
	add := func(dir string, args ...string) { calls = append(calls, call{dir, args}) }
	for _, mode := range []string{"", "grouped"} {
		for _, rel := range []string{".", "src", "indir", "outdir", "../x", "missing", "README.md", "empty"} {
			add(ws, rel, "list", mode)
		}
		for _, rel := range []string{"README.md", "inlink", "outlink", "fifo", "big.bin", "src", ".lectern-lock", ".lectern-write-a1", "missing", "dangling", "caf\xe9.txt"} {
			add(ws, rel, "read", mode)
			add(ws, rel, "stat", mode)
			add(ws, rel, "exists", mode)
		}
		add(ws, ".", "index", mode)
		add(ws, ".", "gitstatus", mode)
	}
	for _, rel := range []string{root + "/outside.txt", "~/outside.txt", "relative", root, root + "/nope", ws + "/fifo", ws + "/big.bin"} {
		add(ws, rel, "ext_stat", "")
		add(ws, rel, "ext_read", "")
	}
	add(ws, ".", "frobnicate", "")
	add(plain, ".", "index", "")
	add(plain, ".", "gitstatus", "")
	// Searches run after the FIFO is gone: the Python walk would block
	// opening it (the Go one skips anything but regular files).
	searchFrom := len(calls)
	search := func(dir, q, regex, cs, word, include, ignored string) {
		add(dir, ".", "search", "", q, regex, cs, word, include, ignored)
	}
	for _, dir := range []string{ws, plain} {
		search(dir, "needle", "0", "0", "0", "", "0")
		search(dir, "NEEDLE", "0", "1", "0", "", "0")
		search(dir, "needle", "0", "0", "1", "", "0")
		search(dir, "need.e[sd]?", "1", "0", "0", "", "0")
		search(dir, "needle", "0", "0", "0", "*.md, src/util/*", "0")
		search(dir, "needle", "0", "0", "0", "", "1")
		search(dir, "hello", "0", "0", "0", "*.go", "1")
		search(dir, "ÄÖ", "0", "0", "0", "", "1")
		search(dir, "", "0", "0", "0", "", "0")
	}
	add(ws, ".", "search", "", "needle")
	for i, c := range calls {
		if i == searchFrom {
			os.Remove(ws + "/fifo")
		}
		args := append([]string{c.ws}, c.args...)
		py := runPy(t, spec, script, args...)
		goRes := wRunGo(t, spec, "workspace-files", args...)
		switch c.args[1] {
		case "archive":
			sameArchive(t, py, goRes)
			continue
		case "index":
			elapsed := regexp.MustCompile(`"elapsed_ms": \d+`)
			py.stdout = elapsed.ReplaceAllString(py.stdout, `"elapsed_ms": 0`)
			goRes.stdout = elapsed.ReplaceAllString(goRes.stdout, `"elapsed_ms": 0`)
		}
		if py.stdout != goRes.stdout || py.rc != goRes.rc {
			t.Errorf("%q\npython (rc %d): %.2000s\ngo     (rc %d): %.2000s\n%s", c.args, py.rc, py.stdout, goRes.rc, goRes.stdout, goRes.stderr)
		}
	}
	for _, rel := range []string{".", "src", "README.md", "outdir", "empty"} {
		args := []string{ws, rel, "archive", "grouped"}
		sameArchive(t, runPy(t, spec, script, args...), wRunGo(t, spec, "workspace-files", args...))
	}
}

// An invalid regular expression is refused by both, though the two regular
// expression engines word the reason differently.
func TestWorkspaceFilesRefusesInvalidRegex(t *testing.T) {
	script := pythonFile(t, "api/workspace_files.py")
	root := t.TempDir()
	ws := workspaceFixture(t, root)
	args := []string{ws, ".", "search", "", "needle(", "1", "0", "0", "", "0"}
	py := runPy(t, runSpec{dir: root, env: gitEnv}, script, args...)
	goRes := wRunGo(t, runSpec{dir: root, env: gitEnv}, "workspace-files", args...)
	if py.rc != 1 || goRes.rc != 1 || !strings.HasPrefix(goRes.stdout, `{"error": "invalid regular expression: `) {
		t.Fatalf("python %d %s\ngo %d %s", py.rc, py.stdout, goRes.rc, goRes.stdout)
	}
}

func TestWorkspaceFilesChangesParity(t *testing.T) {
	script := pythonFile(t, "api/workspace_files.py")
	hash := func(s string) string { return sha256Hex([]byte(s)) }
	type step struct {
		rel, action, mode string
		extra             []string
		upload            string // staged as extra[0] before the step
	}
	write := func(rel, base, body string) step {
		return step{rel: rel, action: "write", extra: []string{"$UPLOAD", base}, upload: body}
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{name: "writes", steps: []step{
			write("new.txt", "absent", "fresh\n"),
			write("new.txt", "absent", "again\n"),
			write("new.txt", hash("fresh\n"), "second\n"),
			write("new.txt", hash("stale"), "third\n"),
			write("new.txt", "any", "forced\n"),
			write("inlink", hash("# Title\nhello world\nHello again\n"), "through link\n"),
			write("outlink", "any", "escape\n"),
			write("src/deep/new.txt", "absent", "no parent\n"),
			write("src", "any", "a folder\n"),
			write("fifo", "any", "pipe\n"),
			write(".lectern-lock", "any", "x"),
			{rel: ".lectern-lock", action: "write", mode: "grouped", extra: []string{"$UPLOAD", "any"}, upload: "x"},
			write("dangling", "absent", "made through a dangling link\n"),
			{rel: "x.txt", action: "write", extra: []string{"$UPLOAD"}},
			{rel: "exec.sh", action: "write", extra: []string{"$UPLOAD", "any"}, upload: "#!/bin/sh\n"},
		}},
		{name: "operations", steps: []step{
			{rel: "newdir", action: "mkdir"},
			{rel: "newdir", action: "mkdir"},
			{rel: "newdir/f.txt", action: "create"},
			{rel: "newdir/f.txt", action: "create"},
			{rel: "/", action: "create"},
			{rel: "..", action: "mkdir"},
			{rel: "a/../../x", action: "mkdir"},
			{rel: "outdir/x", action: "create"},
			{rel: "newdir", action: "rename", extra: []string{"renamed"}},
			{rel: "renamed", action: "rename", extra: []string{"renamed/inner"}},
			{rel: "README.md", action: "rename", extra: []string{"alpha.txt"}},
			{rel: "gone", action: "rename", extra: []string{"x"}},
			{rel: ".git", action: "rename", extra: []string{"git2"}},
			{rel: "README.md", action: "rename"},
			{rel: "renamed", action: "delete"},
			{rel: "inlink", action: "delete"},
			{rel: "src", action: "delete"},
			{rel: ".git", action: "delete"},
			{rel: "gone", action: "delete"},
			{rel: ".", action: "delete"},
			{rel: ".lectern-write-a1", action: "delete", mode: "grouped"},
			{rel: ".lectern-write-a1", action: "delete"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wRequirePython(t)
			pyRoot, goRoot := t.TempDir(), t.TempDir()
			workspaceFixture(t, pyRoot)
			workspaceFixture(t, goRoot)
			os.Chmod(pyRoot+"/ws/src/main.go", 0o600)
			os.Chmod(goRoot+"/ws/src/main.go", 0o600)
			for i, s := range c.steps {
				run := func(root string, fn func(runSpec, ...string) runResult) runResult {
					extra := append([]string(nil), s.extra...)
					for j := range extra {
						if extra[j] == "$UPLOAD" {
							extra[j] = root + "/upload"
							os.WriteFile(extra[j], []byte(s.upload), 0o600)
						}
					}
					res := fn(runSpec{dir: root, env: gitEnv}, append([]string{root + "/ws", s.rel, s.action, s.mode}, extra...)...)
					res.stdout = mtimeField.ReplaceAllString(strings.ReplaceAll(res.stdout, root, "$ROOT"), `"mtime": 0`)
					return res
				}
				py := run(pyRoot, func(sp runSpec, a ...string) runResult { return runPy(t, sp, script, a...) })
				goRes := run(goRoot, func(sp runSpec, a ...string) runResult { return wRunGo(t, sp, "workspace-files", a...) })
				sameResult(t, py, goRes)
				t.Logf("step %d %s %s: rc %d %s", i, s.action, s.rel, goRes.rc, strings.TrimSpace(goRes.stdout))
				if a, b := strings.ReplaceAll(treeOf(t, pyRoot), pyRoot, "$ROOT"), strings.ReplaceAll(treeOf(t, goRoot), goRoot, "$ROOT"); a != b {
					t.Fatalf("after step %d trees differ\npython:\n%s\ngo:\n%s", i, a, b)
				}
			}
		})
	}
}

// A watch answers at once for a new client, times out unchanged, and wakes
// on a change; both versions agree on the token.
func TestWorkspaceFilesWatchParity(t *testing.T) {
	script := pythonFile(t, "api/workspace_files.py")
	root := t.TempDir()
	ws := workspaceFixture(t, root)
	spec := runSpec{dir: root, env: gitEnv}
	dirs := `[".", "src", 5, "outdir", "missing"]`
	first := assertParity(t, spec, script, "workspace-files", ws, ".", "watch", "", dirs, "", "1")
	var reply struct{ Token string }
	json.Unmarshal([]byte(first.stdout), &reply)
	start := time.Now()
	assertParity(t, spec, script, "workspace-files", ws, ".", "watch", "", dirs, reply.Token, "1")
	if time.Since(start) < 2*time.Second {
		t.Fatal("the watches did not wait out their time")
	}
	assertParity(t, spec, script, "workspace-files", ws, ".", "watch", "", dirs, "stale-token", "1")
	for _, bad := range [][]string{{"{"}, {"[]", "tok"}, {"[]", "tok", "soon"}} {
		assertParity(t, spec, script, "workspace-files", append([]string{ws, ".", "watch", ""}, bad...)...)
	}
	// A change while both wait: each wakes with the same new token.
	for _, runner := range []func() runResult{
		func() runResult {
			return runPy(t, spec, script, ws, ".", "watch", "", `["src"]`, currentToken(t, script, ws), "10")
		},
		func() runResult {
			return wRunGo(t, spec, "workspace-files", ws, ".", "watch", "", `["src"]`, currentToken(t, script, ws), "10")
		},
	} {
		go func() {
			time.Sleep(400 * time.Millisecond)
			os.WriteFile(ws+"/src/changed-"+fmt.Sprint(time.Now().UnixNano()), nil, 0o644)
		}()
		start := time.Now()
		res := runner()
		if !strings.Contains(res.stdout, `"changed": true`) || time.Since(start) > 5*time.Second {
			t.Fatalf("no wake on change: %s", res.stdout)
		}
		if !strings.Contains(res.stdout, `"mode": "inotify"`) {
			t.Logf("watch mode: %s", res.stdout)
		}
	}
}

func currentToken(t *testing.T, script, ws string) string {
	res := runPy(t, runSpec{env: gitEnv}, script, ws, ".", "watch", "", `["src"]`, "", "1")
	var reply struct{ Token string }
	json.Unmarshal([]byte(res.stdout), &reply)
	return reply.Token
}

func TestWorkspaceSearchSkipsSpecialFiles(t *testing.T) {
	root := t.TempDir()
	ws := workspaceFixture(t, root)
	done := make(chan runResult, 1)
	go func() {
		done <- wRunGo(t, runSpec{dir: root, env: gitEnv}, "workspace-files", ws, ".", "search", "", "needle", "0", "0", "0", "", "1")
	}()
	select {
	case res := <-done:
		if res.rc != 0 || !strings.Contains(res.stdout, `"source": "python"`) {
			t.Fatalf("search: %d %s %s", res.rc, res.stdout, res.stderr)
		}
	case <-time.After(30 * time.Second):
		os.Remove(ws + "/fifo")
		t.Fatal("the search blocked on a FIFO")
	}
}
