package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fakeGuard reports a fixed dispatch state.
type fakeGuard bool

func (g fakeGuard) DispatchActive() bool { return bool(g) }

func TestSuspiciousSubshellKeywords(t *testing.T) {
	cases := map[string]string{
		"echo $(cat ~/.ssh/id_rsa)":  "ssh",
		"echo `cat secrets.json`":    "secrets",
		"echo ${API_TOKEN}":          "token",
		"echo ${HOME}":               "",
		"echo $(date +%s)":           "",
		"echo $(git rev-parse HEAD)": "",
	}
	for cmd, want := range cases {
		kw, hit := suspiciousSubshell(cmd)
		if (want != "") != hit || kw != want {
			t.Errorf("suspiciousSubshell(%q) = %q, %v; want %q", cmd, kw, hit, want)
		}
	}
}

func TestShellRefusesForbiddenPaths(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("SCRIBE_HOME", t.TempDir())
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "SECRET=1\n")
	writeFile(t, filepath.Join(dir, ".scribe", "forbidden.json"), `{"patterns": [".env", "*.pem"]}`)
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	res := runTool(t, r, "shell", env, map[string]any{"command": "cat .env"})
	if !res.IsError || !strings.Contains(res.Output, `matches forbidden pattern ".env"`) {
		t.Fatalf("reading .env should be refused: %+v", res)
	}
	res = runTool(t, r, "shell", env, map[string]any{"command": "echo hi > key.pem"})
	if !res.IsError || !strings.Contains(res.Output, "forbidden") {
		t.Fatalf("writing key.pem should be refused: %+v", res)
	}
	res = runTool(t, r, "shell", env, map[string]any{"command": "echo ok"})
	if res.IsError || !strings.Contains(res.Output, "ok") {
		t.Fatalf("an allowed command failed: %+v", res)
	}
}

func TestShellRefusesSuspiciousSubshell(t *testing.T) {
	skipOnWindows(t)
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "shell", newTestEnv(t.TempDir(), nil), map[string]any{"command": "echo $(cat ~/.ssh/config)"})
	if !res.IsError || !strings.Contains(res.Output, `suspicious subshell referencing "ssh"`) {
		t.Fatalf("expected suspicious subshell refusal: %+v", res)
	}
}

func TestBinaryReadersAreRefused(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x7f, 'E', 'L', 'F', 0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "notes.txt"), "plain text\n")
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	res := runTool(t, r, "shell", env, map[string]any{"command": "cat blob.bin"})
	if !res.IsError || !strings.Contains(res.Output, "Cannot read binary file: blob.bin") {
		t.Fatalf("cat on a binary should be refused: %+v", res)
	}
	res = runTool(t, r, "shell", env, map[string]any{"command": "head -n 1 notes.txt"})
	if res.IsError || !strings.Contains(res.Output, "plain text") {
		t.Fatalf("head on text failed: %+v", res)
	}
}

func TestWrittenFilesFromCommands(t *testing.T) {
	cases := map[string][]string{
		"sed -i 's/a/b/' f.txt":  {"f.txt"},
		"cp a.go b/c.go":         {"b/c.go"},
		"mv old.txt new.txt":     {"new.txt"},
		"echo x > out.txt":       {"out.txt"},
		"echo x >> log.txt":      {"log.txt"},
		"tee -a one.txt two.txt": {"one.txt", "two.txt"},
		"ls -la":                 nil,
		"echo x > /dev/null":     nil,
		"sed -n 1p f.txt":        nil,
	}
	for cmd, want := range cases {
		got := writtenFiles(cmd)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("writtenFiles(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestToolchainHintMapsActions(t *testing.T) {
	cases := map[string]string{
		"go test ./...":     "test",
		"npm run lint":      "lint",
		"cargo clippy":      "lint",
		"ls -la":            "",
		"echo test":         "",
		"git log --oneline": "",
	}
	for cmd, want := range cases {
		hint := toolchainHint(cmd)
		if want == "" {
			if hint != "" {
				t.Errorf("toolchainHint(%q) = %q, want none", cmd, hint)
			}
			continue
		}
		if !strings.Contains(hint, `project(action: "`+want+`")`) {
			t.Errorf("toolchainHint(%q) = %q, want action %s", cmd, hint, want)
		}
	}
}

func TestShellHintAppearsOnSuccess(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Makefile"), "test:\n\t@echo ran\n")
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "make test"})
	if res.IsError || !strings.Contains(res.Output, `Next time use project(action: "test")`) {
		t.Fatalf("expected the project hint after a successful test: %+v", res)
	}
}

func TestShellCwdInsideProjectAndOutsideNeedsApproval(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := newTestRegistry(t, Options{})

	res := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "pwd", "cwd": "sub"})
	if res.IsError || !strings.Contains(res.Output, "sub") {
		t.Fatalf("cwd inside the project should run there: %+v", res)
	}
	res = runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "pwd", "cwd": "missing"})
	if !res.IsError || !strings.Contains(res.Output, "is not a directory") {
		t.Fatalf("missing cwd should be an error: %+v", res)
	}
	outside := "/" // not under the temp dir, which is an exempt place
	res = runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "pwd", "cwd": outside})
	if !res.IsError || !strings.Contains(res.Output, "outside the project") {
		t.Fatalf("outside cwd with no host should be refused: %+v", res)
	}
	host := &fakeHost{decision: tools.AllowOnce}
	res = runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{"command": "pwd", "cwd": outside})
	if res.IsError || host.approvalCount() != 1 {
		t.Fatalf("approved outside cwd should run once with one prompt: %+v (prompts %d)", res, host.approvalCount())
	}
	host = &fakeHost{decision: tools.Deny}
	res = runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{"command": "pwd", "cwd": outside})
	if !res.IsError {
		t.Fatalf("denied outside cwd should not run: %+v", res)
	}
}

func TestShellWrittenFilesAreClaimed(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "echo hi > made.txt"})
	if res.IsError || len(res.Files) != 1 || res.Files[0] != "made.txt" {
		t.Fatalf("written file should be reported in Files: %+v", res)
	}
}

func TestShellOutputCollectionIsCapped(t *testing.T) {
	skipOnWindows(t)
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "shell", newTestEnv(t.TempDir(), nil), map[string]any{"command": "yes x | head -c 300000"})
	if !strings.Contains(res.Output, "showing the first 256 KB") {
		t.Fatalf("collection cap marker missing: %q", res.Output[len(res.Output)-120:])
	}
}

func TestNativeChecksDetectAndSkipMissingPrograms(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tsconfig.json"), "{}")
	writeFile(t, filepath.Join(dir, "node_modules", ".bin", "tsc"), "x")
	got := nativeChecks(dir)
	if len(got) != 1 || got[0] != "tsc --noEmit" {
		t.Fatalf("nativeChecks = %v, want [tsc --noEmit]", got)
	}
	t.Setenv("PATH", t.TempDir())
	if got := nativeChecks(t.TempDir()); len(got) != 0 {
		t.Fatalf("no config should give no checks: %v", got)
	}
}

func TestPreCommitGateBlocksAndPasses(t *testing.T) {
	skipOnWindows(t)
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "tsconfig.json"), "{}")
	tsc := filepath.Join(dir, "node_modules", ".bin", "tsc")
	writeFile(t, tsc, "#!/bin/sh\necho 'error TS2322: bad type'\nexit 1\n")
	if err := os.Chmod(tsc, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "a.txt"), "1\n")
	gitIn(t, dir, "add", "a.txt")
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	res := runTool(t, r, "shell", env, map[string]any{"command": `git commit -m "add a"`})
	if !res.IsError || !strings.Contains(res.Output, "Pre-commit check failed (tsc --noEmit)") {
		t.Fatalf("failing check should block the commit: %+v", res)
	}
	if strings.Contains(gitIn(t, dir, "log", "--oneline", "--all"), "add a") {
		t.Fatalf("a blocked commit must not be made")
	}

	writeFile(t, tsc, "#!/bin/sh\nexit 0\n")
	res = runTool(t, r, "shell", env, map[string]any{"command": `git commit -m "add a"`})
	if res.IsError {
		t.Fatalf("passing check should let the commit through: %+v", res)
	}
	if !strings.Contains(gitIn(t, dir, "log", "--oneline"), "add a") {
		t.Fatalf("commit was not made")
	}
}

func TestDispatchBlocksMutatingGit(t *testing.T) {
	skipOnWindows(t)
	dir := initRepo(t)
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)
	env.Services = map[string]any{"dispatch": fakeGuard(true)}

	res := runTool(t, r, "shell", env, map[string]any{"command": `git commit -m "x"`})
	if !res.IsError || !strings.Contains(res.Output, "BLOCKED") {
		t.Fatalf("shell commit during dispatch should be blocked: %+v", res)
	}
	res = runTool(t, r, "shell", env, map[string]any{"command": "git status"})
	if strings.Contains(res.Output, "BLOCKED") {
		t.Fatalf("read-only git should not be blocked during dispatch")
	}
	res = runTool(t, r, "git", env, map[string]any{"action": "commit", "message": "x"})
	if !res.IsError || !strings.Contains(res.Output, "BLOCKED") {
		t.Fatalf("git commit during dispatch should be blocked: %+v", res)
	}
	res = runTool(t, r, "git", env, map[string]any{"action": "status"})
	if strings.Contains(res.Output, "BLOCKED") {
		t.Fatalf("git status should not be blocked during dispatch")
	}
}

func TestEnvPairsRejectCredentialsAndBadNames(t *testing.T) {
	if got, err := envPairs(map[string]string{"FOO": "bar"}); err != nil || len(got) != 1 || got[0] != "FOO=bar" {
		t.Fatalf("plain env rejected: %v %v", got, err)
	}
	if _, err := envPairs(map[string]string{"GITHUB_TOKEN": "x"}); err == nil {
		t.Fatalf("credential env should be refused")
	}
	if _, err := envPairs(map[string]string{"A=B": "x"}); err == nil {
		t.Fatalf("name with = should be refused")
	}
}

func TestGitCommitStagesFilesAndAmends(t *testing.T) {
	skipOnWindows(t)
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "1\n", "first")
	writeFile(t, filepath.Join(dir, "b.txt"), "2\n")
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	res := gitRun(t, r, env, map[string]any{"action": "commit", "message": "second", "files": []string{"b.txt"}})
	if res.IsError {
		t.Fatalf("commit with files should stage them first: %+v", res)
	}
	if n := len(strings.Split(strings.TrimSpace(gitIn(t, dir, "log", "--oneline")), "\n")); n != 2 {
		t.Fatalf("want 2 commits, got %d", n)
	}

	res = gitRun(t, r, env, map[string]any{"action": "commit", "amend": true, "message": "second, reworded"})
	if res.IsError {
		t.Fatalf("amend failed: %+v", res)
	}
	log := gitIn(t, dir, "log", "--format=%s")
	if !strings.Contains(log, "second, reworded") || strings.Count(log, "\n") != 2 {
		t.Fatalf("amend should rewrite the last commit in place:\n%s", log)
	}

	res = gitRun(t, r, env, map[string]any{"action": "commit", "amend": true})
	if res.IsError {
		t.Fatalf("amend without a message should keep the old one: %+v", res)
	}

	writeFile(t, filepath.Join(dir, ".env"), "X=1\n")
	res = gitRun(t, r, env, map[string]any{"action": "commit", "message": "secret", "files": []string{".env"}})
	if !res.IsError || !strings.Contains(res.Output, "secret file") {
		t.Fatalf("secret file should be refused on commit: %+v", res)
	}
}

func TestProjectEnvIsPassedThrough(t *testing.T) {
	// The project tool sends env into the child process; check the wiring
	// through envPairs ordering rather than running a real toolchain.
	got, err := envPairs(map[string]string{"B": "2", "A": "1"})
	if err != nil || strings.Join(got, ",") != "A=1,B=2" {
		t.Fatalf("env pairs not sorted: %v %v", got, err)
	}
}
