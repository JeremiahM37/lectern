package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestRuleMatchToolGlob(t *testing.T) {
	o := Options{
		Deny:  []string{"shell(git push --force*)", "git reset --hard"},
		Allow: []string{"Bash(npm test*)"},
	}
	if _, ok := o.denied("git push  --force-with-lease origin main"); !ok {
		t.Error("Tool(glob) deny should match with whitespace collapsed")
	}
	if _, ok := o.denied("git push origin main"); ok {
		t.Error("a glob that does not match should not deny")
	}
	if _, ok := o.denied("echo git reset --hard"); !ok {
		t.Error("bare entries keep the substring match")
	}
	if !o.allowed("npm test -- -run Foo") {
		t.Error("Bash(glob) allow should match the command")
	}
	if o.allowed("npm test; rm -rf x") {
		t.Error("allow never passes a chained command")
	}
	o2 := Options{Deny: []string{"fetch(git push*)"}}
	if _, ok := o2.denied("git push origin"); ok {
		t.Error("a non-command tool name should not match a command")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"a*", "abc/def", true},
		{"a**c", "a/b/c", true},
		{"a.c", "abc", false},
		{"go test*", "go test ./...", true},
		{"go test", "go test ./...", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pat, c.s, got, c.want)
		}
	}
}

func TestOutsideWritesExemptTempAndProject(t *testing.T) {
	workdir := "/home/dev/proj"
	cases := map[string][]string{
		"echo hi > /var/lib/x":      {"/var/lib/x"},
		"echo hi > /tmp/x":          nil,
		"echo hi > sub/x":           nil,
		"tee ../../escape":          {"/home/escape"},
		"cp a.txt /etc/hosts.bak":   {"/etc/hosts.bak"},
		"echo hi > /dev/null":       nil,
		"echo hi > /home/dev/proj/": nil,
	}
	for cmd, want := range cases {
		got := outsideWrites(workdir, workdir, cmd)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("outsideWrites(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestShellOutsideWriteRefusedWithoutHost(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "echo hi > /var/lib/scribe-never-written"})
	if !res.IsError || !strings.Contains(res.Output, "outside the project") {
		t.Fatalf("write outside the project with no host should be refused: %+v", res)
	}
	if _, err := os.Stat("/var/lib/scribe-never-written"); err == nil {
		t.Fatal("refused command must not run")
	}
	host := &fakeHost{decision: tools.Deny}
	res = runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{"command": "echo hi > /var/lib/scribe-never-written"})
	if !res.IsError || host.approvalCount() != 1 {
		t.Fatalf("denied outside write should prompt once and not run: %+v (prompts %d)", res, host.approvalCount())
	}
}

func TestGitToolCommitRunsPreCommitGate(t *testing.T) {
	skipOnWindows(t)
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "tsconfig.json"), "{}")
	tsc := filepath.Join(dir, "node_modules", ".bin", "tsc")
	writeFile(t, tsc, "#!/bin/sh\necho 'error TS2322: bad type'\nexit 1\n")
	if err := os.Chmod(tsc, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "a.txt"), "1\n")
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	res := runTool(t, r, "git", env, map[string]any{"action": "commit", "message": "add a", "files": []string{"a.txt"}})
	if !res.IsError || !strings.Contains(res.Output, "Pre-commit check failed (tsc --noEmit)") {
		t.Fatalf("git commit should be blocked by the pre-commit gate: %+v", res)
	}
	if strings.Contains(gitIn(t, dir, "log", "--oneline", "--all"), "add a") {
		t.Fatal("a blocked commit must not be made")
	}
	res = runTool(t, r, "git", env, map[string]any{"action": "commit", "message": "add a", "files": []string{"a.txt"}, "no_verify": true})
	if res.IsError {
		t.Fatalf("no_verify should skip the gate: %+v", res)
	}
}

func TestHeadLinesCapsPreCommitOutput(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "line")
	}
	got := headLines(strings.Join(lines, "\n"), preCommitLines)
	if n := strings.Count(got, "\n"); n != preCommitLines {
		t.Fatalf("expected %d newlines after capping, got %d: %q", preCommitLines, n, got)
	}
	if !strings.Contains(got, "10 more lines omitted") {
		t.Fatalf("omitted count missing: %q", got)
	}
	short := "a\nb"
	if headLines(short, preCommitLines) != short {
		t.Fatal("short output must pass through unchanged")
	}
}

func TestGoProjectGetsVetInNativeChecks(t *testing.T) {
	if !programInstalled(t.TempDir(), "go") {
		t.Skip("go is not on PATH")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module x\n\ngo 1.21\n")
	got := nativeChecks(dir)
	if len(got) != 2 || got[0] != "go vet ./..." || got[1] != "go build -o "+os.DevNull+" ./..." {
		t.Fatalf("nativeChecks = %v, want go vet then go build", got)
	}
}

func TestPlainLintFailsOnWarnings(t *testing.T) {
	fg := foreground{raw: "src/a.js\n  1:1  warning  unused variable\n\n✖ 1 problem (0 errors, 1 warning)\n", body: "x", exit: 0}
	res := projectResult("lint", "eslint .", fg, time.Minute)
	if !res.IsError || !strings.Contains(res.Output, "1 warning(s)") {
		t.Fatalf("lint with warnings and exit 0 should fail: %+v", res)
	}
	clean := foreground{raw: "all good\n", body: "all good", exit: 0}
	if res := projectResult("lint", "eslint .", clean, time.Minute); res.IsError {
		t.Fatalf("clean lint should pass: %+v", res)
	}
	if res := projectResult("test", "eslint .", fg, time.Minute); res.IsError {
		t.Fatalf("only lint judges warnings: %+v", res)
	}
}

func TestProjectDefaultTimeoutIsTwoMinutes(t *testing.T) {
	if projectTimeout != 2*time.Minute {
		t.Fatalf("projectTimeout = %v, want 2m (spec 06 9.2)", projectTimeout)
	}
}

func TestProjectShapeKeepsHeadAndTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString("0123456789 line of output\n")
	}
	text := b.String()
	body, omitted := projectShape(text, "project")
	if omitted == 0 {
		t.Fatal("long output should be cut")
	}
	if len(body) > projectHeadBytes+projectTailBytes+400 {
		t.Fatalf("body is %d bytes, want about 8000 plus the marker", len(body))
	}
	if !strings.HasPrefix(body, "0123456789") || !strings.Contains(body, "lines omitted") {
		t.Fatalf("head or marker missing: %q", body[:80])
	}
	if !strings.Contains(body, "[full output:") {
		t.Fatal("full text should be kept in a tee file")
	}
	short := "ok\n"
	if got, n := projectShape(short, "x"); got != short || n != 0 {
		t.Fatalf("short output must pass through: %q %d", got, n)
	}
}

func TestProjectListShowsWorkspacesAndCwdIsChecked(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.work"), "go 1.21\n\nuse ./api\nuse (\n\t./lib\n)\n")
	writeFile(t, filepath.Join(root, "api", "go.mod"), "module api\n")
	writeFile(t, filepath.Join(root, "lib", "go.mod"), "module lib\n")
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(root, nil)

	res := runTool(t, r, "project", env, map[string]any{"action": "list"})
	if res.IsError || !strings.Contains(res.Output, "api (go)") || !strings.Contains(res.Output, "lib (go)") {
		t.Fatalf("list should show workspace packages: %+v", res)
	}
	res = runTool(t, r, "project", env, map[string]any{"action": "list", "cwd": "api"})
	if res.IsError || !strings.Contains(res.Output, "Detected: go") {
		t.Fatalf("list with cwd should detect the package: %+v", res)
	}
	res = runTool(t, r, "project", env, map[string]any{"action": "list", "cwd": "../.."})
	if !res.IsError || !strings.Contains(res.Output, "outside the project") {
		t.Fatalf("cwd outside the project should be refused: %+v", res)
	}
	res = runTool(t, r, "project", env, map[string]any{"action": "list", "cwd": "nope"})
	if !res.IsError || !strings.Contains(res.Output, "is not a directory") {
		t.Fatalf("missing cwd should say what to do: %+v", res)
	}
}

func TestWorkspacePackagesAllSources(t *testing.T) {
	pnpm := t.TempDir()
	writeFile(t, filepath.Join(pnpm, "pnpm-workspace.yaml"), "packages:\n  - 'packages/*'\n  - apps/web\n")
	os.MkdirAll(filepath.Join(pnpm, "packages", "ui"), 0o755)
	os.MkdirAll(filepath.Join(pnpm, "apps", "web"), 0o755)
	if got := workspacePackages(pnpm); strings.Join(got, ",") != "apps/web,packages/ui" {
		t.Errorf("pnpm packages = %v", got)
	}

	cargo := t.TempDir()
	writeFile(t, filepath.Join(cargo, "Cargo.toml"), "[workspace]\nmembers = [\"crates/*\", \"tool\"]\n\n[package]\nname = \"root\"\n")
	os.MkdirAll(filepath.Join(cargo, "crates", "core"), 0o755)
	os.MkdirAll(filepath.Join(cargo, "tool"), 0o755)
	if got := workspacePackages(cargo); strings.Join(got, ",") != "crates/core,tool" {
		t.Errorf("cargo members = %v", got)
	}

	npm := t.TempDir()
	writeFile(t, filepath.Join(npm, "package.json"), `{"workspaces": {"packages": ["services/*"]}}`)
	os.MkdirAll(filepath.Join(npm, "services", "api"), 0o755)
	if got := workspacePackages(npm); strings.Join(got, ",") != "services/api" {
		t.Errorf("npm packages = %v", got)
	}

	single := t.TempDir()
	writeFile(t, filepath.Join(single, "go.mod"), "module one\n")
	if got := workspacePackages(single); got != nil {
		t.Errorf("a single package has no workspace: %v", got)
	}
}

func TestBranchSummary(t *testing.T) {
	cases := map[string]string{
		"main...origin/main [ahead 2]": "main [ahead 2]",
		"main":                         "main",
		"No commits yet on trunk":      "no commits yet on trunk",
		"HEAD (no branch)":             "detached HEAD",
	}
	for in, want := range cases {
		if got := branchSummary(in); got != want {
			t.Errorf("branchSummary(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGitContextBlockDescribesRepo(t *testing.T) {
	skipOnWindows(t)
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "1\n")
	gitIn(t, dir, "add", "a.txt")
	gitIn(t, dir, "commit", "-m", "first commit")
	writeFile(t, filepath.Join(dir, "b.txt"), "new\n")
	got := GitContextBlock(dir)
	for _, want := range []string{"Branch: ", "Working tree: 0 staged, 0 unstaged, 1 untracked", "Last commits:", "first commit"} {
		if !strings.Contains(got, want) {
			t.Errorf("git block missing %q:\n%s", want, got)
		}
	}
	if GitContextBlock(t.TempDir()) != "" {
		t.Error("a directory outside a repository gives no block")
	}
}

func TestProjectCommandsLineListsCommands(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module x\n")
	got := ProjectCommandsLine(dir)
	if !strings.Contains(got, "test: go test ./...") || !strings.Contains(got, "build: go build -o "+os.DevNull+" ./...") {
		t.Fatalf("commands line = %q", got)
	}
	if ProjectCommandsLine(t.TempDir()) != "" {
		t.Error("no toolchain gives an empty line")
	}
}

func TestFormatFileRewritesOnlyThatFile(t *testing.T) {
	if !programInstalled(t.TempDir(), "gofmt") {
		t.Skip("gofmt is not on PATH")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module x\n\ngo 1.21\n")
	file := filepath.Join(dir, "main.go")
	writeFile(t, file, "package main\nfunc main(){}\n")
	if err := FormatFile(context.Background(), dir, file, 20*time.Second); err != nil {
		t.Fatalf("FormatFile: %v", err)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), "func main() {") {
		t.Fatalf("file was not formatted: %q", b)
	}
}

func TestFormatFileWithoutFormatterSaysSo(t *testing.T) {
	if err := FormatFile(context.Background(), t.TempDir(), "x.txt", time.Second); err != ErrNoFormatter {
		t.Fatalf("err = %v, want ErrNoFormatter", err)
	}
}
