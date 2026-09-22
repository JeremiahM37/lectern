package api_test

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/store"
)

func TestLiveReviewReadsGitIndexAndWorktreeWithoutChangingEither(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	root := t.TempDir()
	mustRun(t, root, "tmux", "new-session", "-d", "-s", "review", "bash --norc")
	mustRun(t, root, "git", "init", "-q")
	mustRun(t, root, "git", "config", "user.name", "Test")
	mustRun(t, root, "git", "config", "user.email", "test@example.com")
	write := func(name, text string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("tracked.txt", "original\n")
	write("rename.txt", "renamed contents\n")
	mustRun(t, root, "git", "add", ".")
	mustRun(t, root, "git", "commit", "-qm", "base")
	write("tracked.txt", "staged version\n")
	mustRun(t, root, "git", "add", "tracked.txt")
	write("tracked.txt", "working version\n")
	mustRun(t, root, "git", "mv", "rename.txt", "renamed file.txt")
	weird := "Résumé's $(touch owned).txt"
	write(weird, "new content\n")
	write("binary.dat", "\x00binary\xff")
	outside := filepath.Join(t.TempDir(), "private")
	os.WriteFile(outside, []byte("DO NOT READ OUTSIDE"), 0600)
	os.Symlink(outside, filepath.Join(root, "link"))
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "review", Kind: "local"})
	session, _ := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: "review", Workdir: root, TmuxSession: "review", Status: "idle"})
	base := fmt.Sprintf("/api/term/session/%d/changes", session.ID)
	read := func(q string) obj { t.Helper(); var out obj; h.decode("GET", base+q, nil, 200, &out); return out }
	indexBefore, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	working := read("?path=tracked.txt")
	if !strings.Contains(working.str("patch"), "+working version") || !strings.Contains(working.str("patch"), "-staged version") {
		t.Fatalf("working diff: %v", working)
	}
	staged := read("?scope=staged&path=tracked.txt")
	if !strings.Contains(staged.str("patch"), "+staged version") || strings.Contains(staged.str("patch"), "working version") {
		t.Fatalf("staged diff: %v", staged)
	}
	if !strings.Contains(read("?scope=staged&path=renamed%20file.txt").str("patch"), "rename from") {
		t.Fatal("rename not represented")
	}
	if !strings.Contains(read("?path="+url.QueryEscape(weird)).str("patch"), "+new content") {
		t.Fatal("untracked content missing")
	}
	if !strings.Contains(read("?path=binary.dat").str("patch"), "Binary files") {
		t.Fatal("binary not represented")
	}
	if strings.Contains(read("?path=link").str("patch"), "DO NOT READ OUTSIDE") {
		t.Fatal("followed untracked symlink")
	}
	for _, q := range []string{"?path=../private", "?path=" + url.QueryEscape(outside), "?path=:(glob)*"} {
		code, _ := h.request("GET", base+q, nil, nil)
		if code != 409 {
			t.Fatalf("unsafe path %s: %d", q, code)
		}
	}
	code, _ := h.request("GET", base+"?scope=anything", nil, nil)
	if code != 400 {
		t.Fatal(code)
	}
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("review modified index")
	}
	if _, e := os.Stat(filepath.Join(root, "owned")); e == nil {
		t.Fatal("filename executed as shell")
	}
	write("large.txt", strings.Repeat("long added line\n", 60000))
	if read("?path=large.txt")["truncated"] != true {
		t.Fatal("oversized diff not labelled")
	}
}

func TestReviewUnbornRepositoryAndWorkspaceBoundary(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	root := t.TempDir()
	mustRun(t, root, "tmux", "new-session", "-d", "-s", "review", "bash --norc")
	mustRun(t, root, "git", "init", "-q")
	os.Mkdir(filepath.Join(root, "sub"), 0700)
	os.WriteFile(filepath.Join(root, "outside.txt"), []byte("not this workspace"), 0600)
	os.WriteFile(filepath.Join(root, "sub", "new.txt"), []byte("first file\n"), 0600)
	mustRun(t, root, "git", "add", "sub/new.txt")
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "review", Kind: "local"})
	session, _ := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: "review", Workdir: filepath.Join(root, "sub"), TmuxSession: "review", Status: "idle"})
	var out obj
	h.decode("GET", fmt.Sprintf("/api/term/session/%d/changes?scope=staged", session.ID), nil, 200, &out)
	if out.str("path") != "new.txt" || !strings.Contains(out.str("patch"), "+first file") || strings.Contains(fmt.Sprint(out["files"]), "outside.txt") {
		t.Fatalf("unborn/subdir: %v", out)
	}
}
