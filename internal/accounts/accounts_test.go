package accounts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func TestNextSkipsLimitedAccountsInRotationOrder(t *testing.T) {
	cands := []Candidate{
		{ID: 1, Label: "Default"},
		{ID: 2, Label: "work", LimitedUntil: now.Add(time.Hour)},
		{ID: 3, Label: "spare"},
		{ID: 4, Label: "old", LimitedUntil: now.Add(-time.Minute)}, // reset has passed
	}
	for _, tc := range []struct {
		current, want int64
	}{
		{1, 3}, // work is limited
		{3, 4}, // old's reset has passed
		{4, 1}, // wraps around
		{0, 1}, // an unregistered default starts at the top
	} {
		got, _, ok := Next(cands, tc.current, now)
		if !ok || got.ID != tc.want {
			t.Errorf("from %d: got %d (ok %v), want %d", tc.current, got.ID, ok, tc.want)
		}
	}
}

func TestNextHonoursUnknownResetsAndReportedUsage(t *testing.T) {
	cands := []Candidate{
		{ID: 1},
		// Limited with no reset named: held for UnknownResetHold.
		{ID: 2, LimitedAt: now.Add(-time.Hour)},
		// The CLI itself reports a full window resetting later.
		{ID: 3, UsagePct: 100, UsageReset: now.Add(30 * time.Minute)},
		// Full, but that window has already reset.
		{ID: 4, UsagePct: 100, UsageReset: now.Add(-time.Minute)},
	}
	got, _, ok := Next(cands, 1, now)
	if !ok || got.ID != 4 {
		t.Fatalf("got %d, want 4", got.ID)
	}
	_, soonest, ok := Next(cands[:3], 1, now)
	if ok || !soonest.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("every account limited: ok=%v soonest=%v", ok, soonest)
	}
	if u := (Candidate{LimitedAt: now.Add(-6 * time.Hour)}).BlockedUntil(now); !u.IsZero() {
		t.Fatalf("an unknown reset is held too long: %v", u)
	}
}

func TestNextWithNothingElse(t *testing.T) {
	if _, _, ok := Next([]Candidate{{ID: 1}}, 1, now); ok {
		t.Fatal("picked the current account")
	}
	if _, _, ok := Next(nil, 0, now); ok {
		t.Fatal("picked from nothing")
	}
}

func TestClaudeProjectDir(t *testing.T) {
	for in, want := range map[string]string{
		"/home/admin/projects/lectern": "-home-admin-projects-lectern",
		"/tmp/x/y.z_w":                 "-tmp-x-y-z-w",
		"/mnt/bulk/a b":                "-mnt-bulk-a-b",
	} {
		if got := ClaudeProjectDir(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestCreateDirIsPrivate(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command("bash", "-c", CreateDirCommand("claude", "Work Laptop!", ""))
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	dir := strings.TrimSpace(string(out))
	want, _ := filepath.EvalSymlinks(filepath.Join(home, ".lectern/accounts/claude/work-laptop"))
	if dir != want {
		t.Fatalf("dir %q, want %q", dir, want)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v, err %v", info.Mode(), err)
	}
	// Adopting an existing directory tightens it too.
	loose := filepath.Join(home, "existing")
	os.MkdirAll(loose, 0o755)
	if out, err := exec.Command("bash", "-c", CreateDirCommand("codex", "x", loose)).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if info, _ := os.Stat(loose); info.Mode().Perm() != 0o700 {
		t.Fatalf("adopted dir mode %v", info.Mode())
	}
}

func TestStatusCommandNeverReadsTheCredential(t *testing.T) {
	dir := t.TempDir()
	if !strings.HasPrefix(StatusCommand("claude", dir), "test -s ") {
		t.Fatal("status must only test for the file")
	}
	run := func() string {
		out, _ := exec.Command("bash", "-c", StatusCommand("claude", dir)).Output()
		return strings.TrimSpace(string(out))
	}
	if run() != "signed-out" {
		t.Fatal("empty dir reads as signed in")
	}
	os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte("{}"), 0o600)
	if run() != "signed-in" {
		t.Fatal("credential file not seen")
	}
}

// stage runs StageCommand for real and returns the relative path it printed.
func stage(t *testing.T, agent, from, to, id, workdir string, env ...string) (string, error) {
	t.Helper()
	cmd, err := StageCommand(agent, from, to, id, workdir)
	if err != nil {
		return "", err
	}
	c := exec.Command("bash", "-c", cmd)
	c.Env = append(os.Environ(), env...)
	out, err := c.Output()
	return strings.TrimSpace(string(out)), err
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStageClaudeTranscript(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	id := "0f3c2d1e-4b5a-4c6d-8e7f-9a0b1c2d3e4f"
	workdir := "/work/my.repo"
	src := filepath.Join(a, "projects", "-work-my-repo", id+".jsonl")
	write(t, src, `{"sessionId":"`+id+`"}`+"\n")
	write(t, filepath.Join(a, "projects", "-work-my-repo", id, "subagents", "agent-1.jsonl"), "{}\n")
	// Another conversation in the same folder is not moved.
	write(t, filepath.Join(a, "projects", "-work-my-repo", "other.jsonl"), "{}\n")

	rel, err := stage(t, "claude", a, b, id, workdir)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "projects/-work-my-repo/"+id+".jsonl" {
		t.Fatalf("rel %q", rel)
	}
	got, err := os.ReadFile(filepath.Join(b, rel))
	if err != nil || !strings.Contains(string(got), id) {
		t.Fatalf("copied transcript: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(b, "projects", "-work-my-repo", id, "subagents", "agent-1.jsonl")); err != nil {
		t.Fatal("the conversation's side directory was not copied")
	}
	if _, err := os.Stat(filepath.Join(b, "projects", "-work-my-repo", "other.jsonl")); err == nil {
		t.Fatal("an unrelated conversation was copied")
	}
	if info, _ := os.Stat(filepath.Join(b, "projects")); info.Mode().Perm() != 0o700 {
		t.Fatalf("created dirs are %v, want private", info.Mode())
	}

	// Moving back later brings the newer copy (b's) home.
	write(t, filepath.Join(b, rel), `{"sessionId":"`+id+`"}`+"\n"+`{"more":1}`+"\n")
	if _, err := stage(t, "claude", b, a, id, workdir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(src); !strings.Contains(string(got), "more") {
		t.Fatal("moving back did not bring the newer transcript")
	}
}

func TestStageClaudeFromTheDefaultDirectory(t *testing.T) {
	home := t.TempDir()
	b := filepath.Join(home, "b")
	id := "11111111-2222-4333-8444-555555555555"
	write(t, filepath.Join(home, ".claude", "projects", "-w", id+".jsonl"), "{}\n")
	rel, err := stage(t, "claude", "", b, id, "/w", "HOME="+home, "CLAUDE_CONFIG_DIR=")
	if err != nil || rel != "projects/-w/"+id+".jsonl" {
		t.Fatalf("rel %q err %v", rel, err)
	}
	if _, err := os.Stat(filepath.Join(b, rel)); err != nil {
		t.Fatal(err)
	}
}

func TestStageCodexRollout(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	id := "019a1b2c-3d4e-7f60-8a9b-0c1d2e3f4a5b"
	rel := "sessions/2026/09/27/rollout-2026-09-27T10-11-12-" + id + ".jsonl"
	write(t, filepath.Join(a, rel), `{"type":"session_meta","payload":{"id":"`+id+`"}}`+"\n")
	got, err := stage(t, "codex", a, b, id, "/anywhere")
	if err != nil || got != rel {
		t.Fatalf("rel %q err %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(b, rel)); err != nil {
		t.Fatal(err)
	}
}

func TestStageMissingConversationFails(t *testing.T) {
	root := t.TempDir()
	_, err := stage(t, "claude", filepath.Join(root, "a"), filepath.Join(root, "b"), "22222222-2222-4222-8222-222222222222", "/w")
	var exit *exec.ExitError
	if err == nil || !errorsAs(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("missing conversation: %v", err)
	}
	if _, err := StageCommand("claude", "a", "b", "../../etc/passwd", "/w"); err == nil {
		t.Fatal("a path was accepted as a conversation id")
	}
	if _, err := StageCommand("aider", "a", "b", "x", "/w"); err == nil {
		t.Fatal("an unsupported agent was accepted")
	}
}

func errorsAs(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
