package scratch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }

func TestClassifyNeverRemovesAnythingThatLooksLikeWork(t *testing.T) {
	now := float64(1_800_000_000)
	old := now - 30*86400
	dir := Dir{Name: "shell-x", Path: "/s/shell-x", Mtime: old}
	ended := Session{ID: 7, Workdir: "/s/shell-x", LastSeen: old}
	cases := []struct {
		name     string
		dir      Dir
		sessions []Session
		history  *bool
		want     Verdict
	}{
		{"empty and old is removed", dir, []Session{ended}, no(), Empty},
		{"an orphan with no session row is removed too", dir, nil, no(), Empty},
		{"a running session wins over everything", dir, []Session{{ID: 1, Workdir: "/s/shell-x", Live: true}}, no(), Live},
		{"a session in a subdirectory still claims it", dir, []Session{{ID: 1, Workdir: "/s/shell-x/app", Live: true}}, no(), Live},
		{"a sibling with a longer name does not", dir, []Session{{ID: 1, Workdir: "/s/shell-xy", Live: true}}, no(), Empty},
		{"a project session is never scratch", dir, []Session{{ID: 2, Workdir: "/s/shell-x", Project: true, LastSeen: old}}, no(), Project},
		{"one file is work", Dir{Path: "/s/shell-x", Mtime: old, Files: 1}, nil, no(), Work},
		{"one commit is work", Dir{Path: "/s/shell-x", Mtime: old, Commits: 1}, nil, no(), Work},
		// The case that motivated this: a blank shell where someone ran an agent
		// by hand. No files, no session identity — only a conversation on disk.
		{"a conversation with no files is work", Dir{Path: "/s/shell-x", Mtime: old, ClaudeBytes: 5 << 20}, []Session{ended}, no(), Work},
		{"a recorded conversation identity is work", dir, []Session{{ID: 3, Workdir: "/s/shell-x", NativeID: true, LastSeen: old}}, no(), Work},
		{"a mention in an agent's history is work", dir, nil, yes(), Work},
		{"an unreadable directory is work", Dir{Path: "/s/shell-x", Mtime: old, Unreadable: true}, nil, no(), Work},
		{"a keep marker holds it", Dir{Path: "/s/shell-x", Mtime: old, Keep: true}, nil, no(), Kept},
		{"empty but young waits", Dir{Path: "/s/shell-x", Mtime: now - 86400}, nil, no(), Recent},
		{"a recent session keeps an old directory young", dir, []Session{{ID: 4, Workdir: "/s/shell-x", LastSeen: now - 3600}}, no(), Recent},
	}
	for _, c := range cases {
		got := Classify(c.dir, c.sessions, now, 7, c.history)
		if got.Verdict != c.want {
			t.Errorf("%s: got %s (%v), want %s", c.name, got.Verdict, got.Reasons, c.want)
		}
	}
	// Until history has been searched, a removable directory says so, and the
	// sweep must search before acting on it.
	if e := Classify(dir, nil, now, 7, nil); e.Verdict != Empty || !e.NeedsHistoryCheck {
		t.Errorf("unsearched: %+v", e)
	}
}

func TestSafeNameRejectsAnythingMktempWouldNotMake(t *testing.T) {
	for _, ok := range []string{"shell-20260918-qfEK6Y", "codex-1", "a.b_c"} {
		if !SafeName(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", ".trash", "a/b", "a b", "-rf", "x;rm", "$(x)", "a..b", "né"} {
		if SafeName(bad) {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

// world is a scratch root on the real filesystem, reached through the real
// scripts, with every agent home pointed somewhere disposable.
type world struct {
	t                        *testing.T
	root, claude, codex, tmp string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	tmp := t.TempDir()
	w := &world{t: t, tmp: tmp, root: filepath.Join(tmp, "scratch"),
		claude: filepath.Join(tmp, "claude"), codex: filepath.Join(tmp, "codex")}
	for _, d := range []string{w.root, w.claude, w.codex} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func (w *world) run(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + w.tmp, "LECTERN_SCRATCH_ROOT=" + w.root,
		"CLAUDE_CONFIG_DIR=" + w.claude, "CODEX_HOME=" + w.codex,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"}
	out, err := cmd.Output()
	return string(out), err
}

func (w *world) dir(name string) string {
	w.t.Helper()
	p := filepath.Join(w.root, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.run(context.Background(), "git init -q "+shellQuote(p)); err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) write(path, body string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func TestInspectReadsEverySignalFromARealTree(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.dir("empty-1")
	w.write(filepath.Join(w.dir("files-1"), "notes.md"), "hi")
	committed := w.dir("commit-1")
	w.write(filepath.Join(committed, "a.txt"), "a")
	if _, err := w.run(ctx, "cd "+shellQuote(committed)+" && git add -A && git -c user.name=t -c user.email=t@t commit -qm x"); err != nil {
		t.Fatal(err)
	}
	talked := w.dir("talked-1")
	slug := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(talked, "-")
	w.write(filepath.Join(w.claude, "projects", slug, "c.jsonl"), strings.Repeat("x", 4096))
	w.write(filepath.Join(w.dir("kept-1"), KeepMarker), "")
	// None of these is a scratch directory, and none may be reported as one.
	w.dir("has space")
	w.dir(".trash")
	if err := os.Symlink(w.tmp, filepath.Join(w.root, "link-1")); err != nil {
		t.Fatal(err)
	}
	w.write(filepath.Join(w.root, "stray-file"), "x")

	root, dirs, err := Inspect(ctx, w.run)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Dir{}
	for _, d := range dirs {
		got[d.Name] = d
	}
	if len(got) != 5 {
		t.Fatalf("reported %d directories: %v", len(got), got)
	}
	resolved, _ := filepath.EvalSymlinks(w.root)
	if root != resolved {
		t.Errorf("root %q, want %q", root, resolved)
	}
	if d := got["empty-1"]; d.Files != 0 || d.Commits != 0 || d.ClaudeBytes != 0 || d.Keep || d.Unreadable || d.Mtime == 0 {
		t.Errorf("empty: %+v", d)
	}
	if got["files-1"].Files != 1 {
		t.Errorf("files: %+v", got["files-1"])
	}
	if d := got["commit-1"]; d.Commits != 1 || d.Files != 1 {
		t.Errorf("commit: %+v", d)
	}
	if got["talked-1"].ClaudeBytes != 4096 || got["talked-1"].Files != 0 {
		t.Errorf("talked: %+v", got["talked-1"])
	}
	// The marker exempts a directory; it must not also count as a file in it.
	if d := got["kept-1"]; !d.Keep || d.Files != 0 {
		t.Errorf("kept: %+v", d)
	}
}

func TestHistorySearchFindsACodexConversationAndFailsClosed(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	used, unused := w.dir("codex-used"), w.dir("codex-unused")
	w.write(filepath.Join(w.codex, "sessions", "2026", "09", "rollout-1.jsonl"),
		`{"type":"session_meta","payload":{"cwd":"`+used+`"}}`+"\n")
	hits, err := HistoryMentions(ctx, w.run, []string{used, unused})
	if err != nil {
		t.Fatal(err)
	}
	if !hits[used] || hits[unused] {
		t.Errorf("hits: %v", hits)
	}
	// A search that cannot run must not read as "nothing there".
	broken := func(context.Context, string) (string, error) { return "", os.ErrPermission }
	hits, err = HistoryMentions(ctx, broken, []string{unused})
	if err == nil || !hits[unused] {
		t.Errorf("a failed search must report a hit: %v %v", hits, err)
	}
}

func TestTrashIsRecoverableAndPurgeOnlyTakesWhatItStamped(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.write(filepath.Join(w.dir("gone-1"), "proof.txt"), "still here")
	w.dir("stays-1")
	now := time.Now().Unix()
	moved, err := Trash(ctx, w.run, []string{"gone-1", "../escape", "missing-1"}, now-20*86400)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0] != "gone-1" {
		t.Fatalf("moved: %v", moved)
	}
	trash := filepath.Join(w.root, ".trash")
	entries, _ := os.ReadDir(trash)
	if len(entries) != 1 {
		t.Fatalf("trash: %v", entries)
	}
	// Trashed, not deleted: the contents are intact until the purge.
	if body, _ := os.ReadFile(filepath.Join(trash, entries[0].Name(), "proof.txt")); string(body) != "still here" {
		t.Errorf("trashed contents: %q", body)
	}
	if _, err := os.Stat(filepath.Join(w.root, "stays-1")); err != nil {
		t.Errorf("an unnamed directory was touched: %v", err)
	}
	// Something a person dropped in the trash carries no stamp and is not ours.
	w.write(filepath.Join(trash, "mine", "keep.txt"), "x")
	w.write(filepath.Join(trash, "notes.2026", "keep.txt"), "x") // a numeric suffix is not our stamp
	fresh, _ := Trash(ctx, w.run, []string{"stays-1"}, now)
	if len(fresh) != 1 {
		t.Fatalf("fresh: %v", fresh)
	}
	purged, err := Purge(ctx, w.run, 14, now)
	if err != nil || purged != 1 {
		t.Fatalf("purged %d, %v", purged, err)
	}
	left, _ := os.ReadDir(trash)
	names := []string{}
	for _, e := range left {
		names = append(names, e.Name())
	}
	if len(names) != 3 {
		t.Errorf("after purge: %v — only the 20-day-old entry should be gone", names)
	}
}

func TestMarkKeepExemptsADirectory(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.dir("precious-1")
	if err := MarkKeep(ctx, w.run, "precious-1"); err != nil {
		t.Fatal(err)
	}
	_, dirs, _ := Inspect(ctx, w.run)
	if len(dirs) != 1 || !dirs[0].Keep {
		t.Fatalf("dirs: %+v", dirs)
	}
	if err := MarkKeep(ctx, w.run, "nope-1"); err == nil {
		t.Error("marking a missing directory must fail")
	}
	if err := MarkKeep(ctx, w.run, "../x"); err == nil {
		t.Error("an unsafe name must be refused")
	}
}
