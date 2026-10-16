package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func hgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestExportKindsAndFallback(t *testing.T) {
	be, repo := testBackend(t)
	cases := map[string]string{
		"":         ".md",
		"md":       ".md",
		"markdown": ".md",
		"whatever": ".md", // spec: anything but json is Markdown
		"json":     ".json",
	}
	for format, ext := range cases {
		path, err := be.Export(format)
		if err != nil {
			t.Fatalf("export %q: %v", format, err)
		}
		if !strings.HasPrefix(path, filepath.Join(repo, ".scribe", "exports")) || !strings.HasSuffix(path, ext) {
			t.Errorf("export %q wrote %s, want a %s file under .scribe/exports", format, path, ext)
		}
	}
	md, err := be.(*scribeBackend).ExportMarkdown()
	if err != nil || md == "" {
		t.Fatalf("ExportMarkdown: %v, %q", err, md)
	}
}

func TestExportDiagnosticHasPromptToolsAndRedacts(t *testing.T) {
	be, repo := testBackend(t)
	const secret = "sk-testleak0123456789abcdefghij"
	// A stored provider key is a literal the redactor knows; it must not
	// reach a dump that is meant to be shared.
	if err := be.SetKey("fake", secret); err != nil {
		t.Fatal(err)
	}
	be.(*scribeBackend).current().opts.System = "debugging note: " + secret
	path, err := be.Export("all")
	if err != nil {
		t.Fatalf("diagnostic export: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(path), "diagnostic-") || filepath.Dir(path) != filepath.Join(repo, ".scribe", "exports") {
		t.Fatalf("diagnostic path = %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"system_prompt"`, `"tools"`, `"session"`, `"session_id"`} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostic dump lacks %s", want)
		}
	}
	if strings.Contains(text, secret) {
		t.Error("diagnostic dump contains an unredacted secret")
	}
}

func TestGitInitBranchPull(t *testing.T) {
	be, repo := testBackend(t)
	b := be.(*scribeBackend)
	if err := b.GitBranch(""); err == nil {
		t.Error("an empty branch name should be an error")
	}
	if err := b.GitBranch("bad..name"); err == nil {
		t.Error("an invalid branch name should be refused by git")
	}
	if err := b.GitBranch("feature/x"); err != nil {
		t.Fatalf("GitBranch: %v", err)
	}
	if got := hgit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/x" {
		t.Errorf("after GitBranch the branch is %q, want feature/x (create and switch)", got)
	}
	if _, err := b.GitInit(); err != nil {
		t.Errorf("GitInit in an existing repository: %v", err)
	}
	// No remote is configured, so pull must fail with git's message, not hang.
	if _, err := b.GitPull(); err == nil {
		t.Error("GitPull without an upstream should fail")
	}
}

func TestGitInitOutsideRepository(t *testing.T) {
	be, _ := testBackend(t)
	b := be.(*scribeBackend)
	dir := t.TempDir()
	b.current().Workdir = dir
	if _, err := b.GitInit(); err != nil {
		t.Fatalf("GitInit in a fresh folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("GitInit did not create a repository: %v", err)
	}
}

func TestGitCommitWithoutStageAllOnlyCommitsStaged(t *testing.T) {
	be, repo := testBackend(t)
	b := be.(*scribeBackend)
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "loose.txt"), []byte("l\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hgit(t, repo, "add", "staged.txt")
	if err := b.GitCommitWith("only the staged file", false); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if subject := hgit(t, repo, "log", "-1", "--format=%s"); subject != "only the staged file" {
		t.Fatalf("subject = %q", subject)
	}
	if files := hgit(t, repo, "show", "--name-only", "--format=", "HEAD"); files != "staged.txt" {
		t.Errorf("commit contains %q, want only staged.txt", files)
	}
	if status := hgit(t, repo, "status", "--porcelain"); !strings.Contains(status, "?? loose.txt") {
		t.Errorf("unstaged file should stay untracked, status: %q", status)
	}
}

func TestMoveConfigMovesScope(t *testing.T) {
	be, repo := testBackend(t)
	b := be.(*scribeBackend)
	if err := b.SetConfig("verbose", true, false); err != nil {
		t.Fatal(err)
	}
	if err := b.MoveConfig("verbose", false, true, false); err != nil {
		t.Fatalf("move global to project: %v", err)
	}
	proj, err := config.LoadFile(config.ProjectPath(repo))
	if err != nil || proj.Verbose == nil || *proj.Verbose {
		t.Fatalf("project scope should hold verbose=false: %+v, %v", proj.Verbose, err)
	}
	glob, err := config.LoadFile(config.GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	if glob.Verbose != nil {
		t.Errorf("global scope still holds verbose after the move: %v", *glob.Verbose)
	}
	if got := b.Config()["verbose"]; got != false {
		t.Errorf("merged verbose = %v, want false", got)
	}
}

func TestMoveConfigRefusesUnmovableKeyWithoutWriting(t *testing.T) {
	be, repo := testBackend(t)
	b := be.(*scribeBackend)
	err := b.MoveConfig("provider.add", map[string]any{"id": "x", "base_url": "https://x.test/v1"}, true, false)
	if err == nil {
		t.Fatal("provider.add cannot be moved; want an error")
	}
	if _, statErr := os.Stat(config.ProjectPath(repo)); statErr == nil {
		t.Error("a refused move must not write the project config")
	}
}

func TestClearContextSkillsAndMemory(t *testing.T) {
	be, repo := testBackend(t)
	b := be.(*scribeBackend)
	if err := writeActiveSkills(repo, []string{"alpha", "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := b.ClearContext("skills"); err != nil {
		t.Fatalf("clear skills: %v", err)
	}
	if got := b.ActiveSkills(); len(got) != 0 {
		t.Errorf("active skills after clear = %v", got)
	}
	if err := b.ClearContext(""); err != nil {
		t.Errorf("clear all with nothing loaded: %v", err)
	}
	// Memory clear resets the surfaced-memory set (spec 02 /context clear
	// memory). It succeeds and does not touch the skills or the transcript.
	if err := b.ClearContext("memory"); err != nil {
		t.Errorf("clear memory: %v", err)
	}
	if err := b.ClearContext("nonsense"); err == nil {
		t.Error("an unknown target should be an error")
	}
}
