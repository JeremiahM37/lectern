package accounts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in checks against the real CLIs (LECTERN_REAL_CLI_TEST=1): no login is
// needed. Each CLI writes a conversation into account A even when its API
// call is refused, StageCommand copies it into account B, and resuming it by
// id under B must load it — the CLI appends to B's copy — while an unknown id
// fails with the CLI's own "not found" message. They make one refused API
// call each with an invalid key.

func realCLI(t *testing.T, name string) string {
	t.Helper()
	if os.Getenv("LECTERN_REAL_CLI_TEST") != "1" {
		t.Skip("set LECTERN_REAL_CLI_TEST=1 to run against the installed CLIs")
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed", name)
	}
	return bin
}

func runCLI(t *testing.T, dir string, timeout time.Duration, env []string, bin string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func lineCount(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

func TestRealClaudeResumesATranscriptCopiedToAnotherAccount(t *testing.T) {
	bin := realCLI(t, "claude")
	root := t.TempDir()
	a, b, work := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "work.dir")
	os.MkdirAll(work, 0o700)
	key := "ANTHROPIC_API_KEY=sk-ant-invalid"
	runCLI(t, work, 20*time.Second, []string{"CLAUDE_CONFIG_DIR=" + a, key}, bin, "-p", "hi")
	found, _ := filepath.Glob(filepath.Join(a, "projects", "*", "*.jsonl"))
	if len(found) != 1 {
		t.Fatalf("claude wrote %d transcripts in A", len(found))
	}
	id := strings.TrimSuffix(filepath.Base(found[0]), ".jsonl")

	rel, err := stage(t, "claude", a, b, id, work)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(b, rel)
	before := lineCount(t, copied)
	// The refused call is retried with backoff for longer than we wait; the
	// transcript growing under B is the proof that B's claude loaded it.
	runCLI(t, work, 25*time.Second, []string{"CLAUDE_CONFIG_DIR=" + b, key}, bin, "-p", "--resume", id, "continue")
	if after := lineCount(t, copied); after <= before {
		t.Fatalf("B's copy did not grow (%d -> %d lines): the resume did not load it", before, after)
	}
	missing := runCLI(t, work, 25*time.Second, []string{"CLAUDE_CONFIG_DIR=" + b, key}, bin,
		"-p", "--resume", "00000000-0000-4000-8000-000000000000", "continue")
	if !strings.Contains(missing, "No conversation found with session ID") {
		t.Fatalf("an unknown id: %q", missing)
	}
	t.Logf("claude %s: resumed %s from B (%d -> %d lines)", strings.TrimSpace(runCLI(t, work, 10*time.Second, nil, bin, "--version")),
		id, before, lineCount(t, copied))
}

func TestRealCodexResumesARolloutCopiedToAnotherAccount(t *testing.T) {
	bin := realCLI(t, "codex")
	root := t.TempDir()
	a, b, work := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "work")
	os.MkdirAll(work, 0o700)
	os.MkdirAll(a, 0o700)
	os.MkdirAll(b, 0o700)
	runCLI(t, work, 30*time.Second, []string{"CODEX_HOME=" + a, "OPENAI_API_KEY="}, bin, "exec", "--skip-git-repo-check", "hi")
	found, _ := filepath.Glob(filepath.Join(a, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	if len(found) != 1 {
		t.Fatalf("codex wrote %d rollouts in A", len(found))
	}
	name := strings.TrimSuffix(filepath.Base(found[0]), ".jsonl")
	id := name[len(name)-36:]

	rel, err := stage(t, "codex", a, b, id, work)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(b, rel)
	before := lineCount(t, copied)
	out := runCLI(t, work, 30*time.Second, []string{"CODEX_HOME=" + b, "OPENAI_API_KEY="}, bin, "exec", "resume", id, "--skip-git-repo-check", "continue")
	if strings.Contains(out, "no rollout found") || !strings.Contains(out, id) {
		t.Fatalf("resume under B: %q", out)
	}
	if after := lineCount(t, copied); after <= before {
		t.Fatalf("B's copy did not grow (%d -> %d lines)", before, after)
	}
	missing := runCLI(t, work, 30*time.Second, []string{"CODEX_HOME=" + b, "OPENAI_API_KEY="}, bin,
		"exec", "resume", "00000000-0000-4000-8000-000000000000", "--skip-git-repo-check", "continue")
	if !strings.Contains(missing, "no rollout found for thread id") {
		t.Fatalf("an unknown id: %q", missing)
	}
}
