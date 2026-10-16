package app

import (
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := osexec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestSessionOffersOnlyCoreToolsAndExcludesScribeDir(t *testing.T) {
	be, repo := testBackend(t)
	a := be.(*scribeBackend).current()
	offered := a.Agent.OfferedForTest()
	for _, n := range []string{"read", "edit_file", "write_file", "shell", "request_tools", "release_tools"} {
		if !offered[n] {
			t.Errorf("%s should be offered at session start", n)
		}
	}
	for _, n := range []string{"git", "web_search", "fetch_page", "list_dir"} {
		if offered[n] {
			t.Errorf("%s should be deferred", n)
		}
	}
	if _, ok := a.Tools.Get("git"); !ok {
		t.Fatal("deferred tools must still be registered")
	}
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil || !strings.Contains(string(exclude), ".scribe/") {
		t.Fatalf("info/exclude = %q, %v", exclude, err)
	}
}

func TestExcludeScribeDirIsIdempotentAndLeavesGitignore(t *testing.T) {
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q")
	excludeScribeDir(repo)
	excludeScribeDir(repo)
	b, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), ".scribe/"); n != 1 {
		t.Fatalf(".scribe/ appears %d times:\n%s", n, b)
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); !os.IsNotExist(err) {
		t.Fatal(".gitignore must not be created or touched")
	}
	// Outside a repository nothing happens.
	plain := t.TempDir()
	excludeScribeDir(plain)
	if _, err := os.Stat(filepath.Join(plain, ".git")); !os.IsNotExist(err) {
		t.Fatal("a plain directory must not gain a .git")
	}
}

func TestGitCommitLeavesSecretFilesOut(t *testing.T) {
	be, repo := testBackend(t)
	must := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("ok.txt", "fine\n")
	must(".env", "API_TOKEN=hunter2\n")
	must("server.pem", "-----BEGIN-----\n")
	err := be.GitCommit("add notes")
	if err == nil || !strings.Contains(err.Error(), ".env") || !strings.Contains(err.Error(), "server.pem") {
		t.Fatalf("GitCommit error = %v, want the two secret files reported", err)
	}
	files := gitIn(t, repo, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(files, "ok.txt") || strings.Contains(files, ".env") || strings.Contains(files, "server.pem") {
		t.Fatalf("committed files = %q", files)
	}
	status := gitIn(t, repo, "status", "--porcelain")
	if !strings.Contains(status, ".env") || !strings.Contains(status, "server.pem") {
		t.Fatalf("secret files should stay in the working tree, status = %q", status)
	}
}

func TestBangShellScrubsCredentials(t *testing.T) {
	be, _ := testBackend(t)
	t.Setenv("GITHUB_TOKEN", "leak-me-1")
	t.Setenv("MY_SERVICE_TOKEN", "leak-me-2")
	out, code, err := be.(*scribeBackend).Shell(context.Background(), `printf '%s|%s' "${GITHUB_TOKEN:-none}" "${MY_SERVICE_TOKEN:-none}"`)
	if err != nil || code != 0 {
		t.Fatalf("shell err=%v code=%d", err, code)
	}
	if out != "none|none" {
		t.Fatalf("credentials reached the ! shell: %q", out)
	}
}

func TestFileClaimsBlockOtherSessionsOnly(t *testing.T) {
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	reg := newClaimRegistry(func() time.Time { return now }, claimTTL)
	dir := "/work"
	reg.claim(filepath.Join(dir, "a.go"), "tabA", "Scribe session aaaa")

	mine := claimHooks{reg: reg, owner: "tabA", workdir: dir}
	theirs := claimHooks{reg: reg, owner: "tabB", label: "Scribe session b", workdir: dir}
	args := json.RawMessage(`{"path":"a.go","content":"x"}`)

	if block := theirs.PreTool(context.Background(), "write_file", args); block == "" || !strings.Contains(block, "Scribe session aaaa") {
		t.Fatalf("other tab's edit not blocked: %q", block)
	}
	if block := mine.PreTool(context.Background(), "write_file", args); block != "" {
		t.Fatalf("owner must not block itself: %q", block)
	}
	if block := theirs.PreTool(context.Background(), "read", args); block != "" {
		t.Fatalf("reads are never blocked: %q", block)
	}
	if extra, _ := theirs.Prompt(context.Background(), "hi"); !strings.Contains(extra, "a.go") {
		t.Fatalf("turn-start reminder missing the claim: %q", extra)
	}
	if extra, _ := mine.Prompt(context.Background(), "hi"); extra != "" {
		t.Fatalf("own claims must not be reported back: %q", extra)
	}

	now = now.Add(claimTTL + time.Second)
	if block := theirs.PreTool(context.Background(), "write_file", args); block != "" {
		t.Fatalf("expired claim still blocks: %q", block)
	}
	reg.claim(filepath.Join(dir, "b.go"), "tabA", "x")
	reg.release("tabA")
	if got := reg.otherClaims("tabB"); len(got) != 0 {
		t.Fatalf("released claims still listed: %v", got)
	}
}

// inner hooks keep working underneath the claim check.
type vetoHooks struct{ agent.Hooks }

func (vetoHooks) PreTool(context.Context, string, json.RawMessage) string { return "policy says no" }

func TestClaimHooksKeepUserHooks(t *testing.T) {
	reg := newClaimRegistry(time.Now, claimTTL)
	h := claimHooks{inner: vetoHooks{}, reg: reg, owner: "x", workdir: "/w"}
	if block := h.PreTool(context.Background(), "read", nil); block != "policy says no" {
		t.Fatalf("user hook veto lost: %q", block)
	}
}

func TestOnFileClaimsClaimsEditedFiles(t *testing.T) {
	dir := t.TempDir()
	a := &App{Workdir: dir, claimOwner: "tabOnFiles", claimLabel: "tab test"}
	defer fileClaims.release("tabOnFiles")
	a.onFileClaims(agent.Event{Kind: agent.EvToolResult, Result: &tools.Result{Files: []string{"pkg/x.go"}}})
	a.onFileClaims(agent.Event{Kind: agent.EvToolResult, Result: &tools.Result{IsError: true, Files: []string{"never.go"}}})
	if _, ok := fileClaims.heldByOther(filepath.Join(dir, "pkg", "x.go"), "someone-else"); !ok {
		t.Fatal("edited file was not claimed")
	}
	if _, ok := fileClaims.heldByOther(filepath.Join(dir, "never.go"), "someone-else"); ok {
		t.Fatal("a failed edit must not claim")
	}
}

func TestSubagentToolSets(t *testing.T) {
	be, _ := testBackend(t)
	a := be.(*scribeBackend).current()
	web := a.subagentTools("web")
	if got := strings.Join(web.Names(), ","); got != "web_search,fetch_page" {
		t.Fatalf("web subagent tools = %s", got)
	}
	explore := a.subagentTools("explore")
	if _, ok := explore.Get("edit_file"); ok {
		t.Fatal("explore agents must not get edit tools")
	}
	code := a.subagentTools("code")
	if _, ok := code.Get("edit_file"); !ok {
		t.Fatal("code agents need edit tools")
	}
	if _, ok := code.Get("dispatch"); ok {
		t.Fatal("subagents must not dispatch")
	}
}

func TestAutoVerifyReportsBrokenGoPackage(t *testing.T) {
	if _, err := osexec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	must := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.test/broken\n\ngo 1.21\n")
	must("main.go", "package main\n\nfunc main() { undefinedThing() }\n")
	label, ok, report := autoVerify(dir)(context.Background())
	if label == "" || ok || !strings.Contains(report, "undefinedThing") {
		t.Fatalf("label=%q ok=%v report=%q", label, ok, report)
	}
	must("main.go", "package main\n\nfunc main() {}\n")
	if label, ok, report = autoVerify(dir)(context.Background()); !ok {
		t.Fatalf("fixed package still fails (%s): %s", label, report)
	}
	if label, _, _ := autoVerify(t.TempDir())(context.Background()); label != "" {
		t.Fatalf("a directory with no toolchain should have nothing to check, got %q", label)
	}
}
