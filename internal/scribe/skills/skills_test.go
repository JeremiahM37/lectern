package skills

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// env isolates HOME and SCRIBE_HOME in temp dirs and returns the project root.
func env(t *testing.T) (root, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SCRIBE_HOME", filepath.Join(t.TempDir(), "scribe"))
	return t.TempDir(), home
}

func writeSkill(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skill(name, desc, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" + body + "\n"
}

func find(t *testing.T, set *Set, name string) Skill {
	t.Helper()
	sk, ok := set.Find(name)
	if !ok {
		t.Fatalf("skill %q not discovered; have %+v", name, set.Skills)
	}
	return sk
}

func TestDiscoverPrecedence(t *testing.T) {
	root, home := env(t)
	// Same name in project .scribe and global ~/.claude: project wins.
	writeSkill(t, filepath.Join(root, ".scribe", "skills", "dup"), skill("dup", "project copy", "p"))
	writeSkill(t, filepath.Join(home, ".claude", "skills", "dup"), skill("dup", "global copy", "g"))
	// Same name in project .agents and .claude: .claude wins (scan order).
	writeSkill(t, filepath.Join(root, ".agents", "skills", "shared"), skill("shared", "from agents", "a"))
	writeSkill(t, filepath.Join(root, ".claude", "skills", "shared"), skill("shared", "from claude", "c"))
	// Global only.
	writeSkill(t, filepath.Join(home, ".agents", "skills", "only-agents"), skill("only-agents", "global agents", "x"))
	writeSkill(t, filepath.Join(home, ".claude", "skills", "other"), skill("other", "global claude", "y"))
	// Ignored: dot directory, and a directory without SKILL.md.
	writeSkill(t, filepath.Join(root, ".scribe", "skills", ".hidden"), skill("hidden", "no", "h"))
	if err := os.MkdirAll(filepath.Join(root, ".scribe", "skills", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	set, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, set, "dup"); got.Description != "project copy" || got.Scope != ScopeProject {
		t.Fatalf("project should beat global: %+v", got)
	}
	if got := find(t, set, "shared"); got.Description != "from claude" || got.Source != ".claude/skills" {
		t.Fatalf(".claude should beat .agents within project: %+v", got)
	}
	if got := find(t, set, "only-agents"); got.Scope != ScopeGlobal {
		t.Fatalf("global agents skill: %+v", got)
	}
	find(t, set, "other")
	if _, ok := set.Find("hidden"); ok {
		t.Fatal("dot directories must be skipped")
	}
	if _, ok := set.Find("empty"); ok {
		t.Fatal("directories without SKILL.md must be skipped")
	}
	if len(set.Skills) != 4 {
		t.Fatalf("want 4 skills, got %d: %+v", len(set.Skills), set.Skills)
	}
	// Without a root only global skills are visible.
	global, _ := Discover("")
	if _, ok := global.Find("dup"); !ok {
		t.Fatal("global dup should be visible with no root")
	}
	if _, ok := global.Find("shared"); ok {
		t.Fatal("project skills must be hidden with no root")
	}
}

func TestSymlinkCopiesCountedOnce(t *testing.T) {
	root, home := env(t)
	real := filepath.Join(home, ".agents", "skills", "real")
	writeSkill(t, real, skill("real", "the one", "r"))
	// The claude dir links to the same directory under a different name.
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home, ".claude", "skills", "alias")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	_ = root
	set, err := Discover("")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, sk := range set.Skills {
		if sk.Name == "real" || sk.Name == "alias" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("symlinked duplicate should count once, got %d: %+v", count, set.Skills)
	}
}

func TestFrontmatterParsing(t *testing.T) {
	root, home := env(t)
	writeSkill(t, filepath.Join(root, ".scribe", "skills", "tools-list"),
		"---\nname: tools-list\ndescription: \"Multi\n  line   text\"\nallowed-tools: Read, Grep\n---\nBody here\n")
	writeSkill(t, filepath.Join(root, ".scribe", "skills", "tools-yaml"),
		"---\nname: tools-yaml\ndescription: yaml list\nallowed-tools:\n  - Bash\n  - Edit\n---\nBody\n")
	writeSkill(t, filepath.Join(root, ".scribe", "skills", "no-front"), "# Plain\n\nJust markdown.\n")
	writeSkill(t, filepath.Join(root, ".scribe", "skills", "dir-name"), "---\ndescription: name comes from the directory\n---\nB\n")
	writeSkill(t, filepath.Join(root, ".scribe", "skills", "broken"), "---\nname: [unclosed\n---\nB\n")
	writeSkill(t, filepath.Join(home, ".claude", "skills", "bad-name"), "---\nname: ../escape\ndescription: traversal\n---\nB\n")

	set, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if sk := find(t, set, "tools-list"); sk.Description != "Multi line text" || strings.Join(sk.AllowedTools, "|") != "Read|Grep" {
		t.Fatalf("string form: %+v", sk)
	}
	if sk := find(t, set, "tools-yaml"); strings.Join(sk.AllowedTools, "|") != "Bash|Edit" {
		t.Fatalf("list form: %+v", sk)
	}
	if sk := find(t, set, "no-front"); sk.Description != "" {
		t.Fatalf("no frontmatter should give empty description: %+v", sk)
	}
	if sk := find(t, set, "dir-name"); sk.Description == "" {
		t.Fatalf("directory-named skill lost its description: %+v", sk)
	}
	if sk := find(t, set, "broken"); sk.Problem == "" {
		t.Fatalf("broken frontmatter should record a problem: %+v", sk)
	}
	// A frontmatter name with a path separator is ignored; the dir name is used.
	if sk := find(t, set, "bad-name"); sk.Problem != "" || sk.Name != "bad-name" {
		t.Fatalf("unsafe name should fall back to dir name: %+v", sk)
	}
}

func TestCatalogAndRender(t *testing.T) {
	root, _ := env(t)
	if c := mustDiscover(t, root).Catalog(); c != "" {
		t.Fatalf("empty catalog should be empty, got %q", c)
	}
	dir := filepath.Join(root, ".scribe", "skills", "go-style")
	writeSkill(t, dir, skill("go-style", "Use when writing Go code.", "Run gofmt.\n\nPrefer small functions."))
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "errors.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(root, ".scribe", "skills", "zeta"), skill("zeta", "Last one.", "z"))

	set := mustDiscover(t, root)
	cat := set.Catalog()
	want := "- go-style: Use when writing Go code.\n- zeta: Last one."
	if !strings.Contains(cat, want) {
		t.Fatalf("catalog missing lines:\n%s", cat)
	}

	out, err := set.Render("go-style")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "description:") || strings.Contains(out, "---") {
		t.Fatalf("frontmatter leaked into rendered skill:\n%s", out)
	}
	if !strings.Contains(out, "Run gofmt.") || !strings.Contains(out, "Directory: "+dir) {
		t.Fatalf("rendered skill missing body or dir:\n%s", out)
	}
	if !strings.Contains(out, "- references/errors.md") || strings.Contains(out, "- SKILL.md") {
		t.Fatalf("supporting file listing wrong:\n%s", out)
	}
	if _, err := set.Render("nope"); err == nil {
		t.Fatal("unknown skill should error")
	}
}

func TestRenderTruncates(t *testing.T) {
	root, _ := env(t)
	big := strings.Repeat("word ", MaxBodyChars)
	writeSkill(t, filepath.Join(root, ".scribe", "skills", "big"), skill("big", "huge", big))
	out, err := mustDiscover(t, root).Render("big")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[... truncated]") {
		t.Fatal("oversized skill should be truncated with a marker")
	}
	if len(out) > MaxBodyChars+400 {
		t.Fatalf("rendered length %d exceeds cap", len(out))
	}
}

func TestInstallFromDirectoryAndRemove(t *testing.T) {
	root, _ := env(t)
	src := t.TempDir()
	writeSkill(t, src, skill("installed-one", "Installed from a dir.", "body"))
	if err := os.MkdirAll(filepath.Join(src, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "templates", "t.txt"), []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}

	sk, err := Install(root, src, ScopeProject)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if sk.Name != "installed-one" || sk.Dir != filepath.Join(root, ".scribe", "skills", "installed-one") {
		t.Fatalf("installed skill: %+v", sk)
	}
	if _, err := os.Stat(filepath.Join(sk.Dir, "templates", "t.txt")); err != nil {
		t.Fatalf("supporting file not copied: %v", err)
	}
	// No staging directories left behind.
	entries, _ := os.ReadDir(filepath.Join(root, ".scribe", "skills"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".install-") {
			t.Fatalf("staging dir left behind: %s", e.Name())
		}
	}
	find(t, mustDiscover(t, root), "installed-one")

	if _, err := Install(root, src, ScopeProject); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second install should refuse to overwrite, got %v", err)
	}

	if err := Remove(root, "installed-one"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := mustDiscover(t, root).Find("installed-one"); ok {
		t.Fatal("skill still present after Remove")
	}
	if err := Remove(root, "installed-one"); err == nil {
		t.Fatal("removing a missing skill should error")
	}
}

func TestInstallGlobalAndSubpathSelection(t *testing.T) {
	root, _ := env(t)
	src := t.TempDir()
	writeSkill(t, filepath.Join(src, "alpha"), skill("alpha", "a", "a"))
	writeSkill(t, filepath.Join(src, "beta"), skill("beta", "b", "b"))

	if _, err := Install(root, src, ScopeGlobal); err == nil || !strings.Contains(err.Error(), "pick one") {
		t.Fatalf("collection without subpath should ask for one, got %v", err)
	}
	sk, err := Install(root, src+"#beta", ScopeGlobal)
	if err != nil {
		t.Fatalf("Install subpath: %v", err)
	}
	if sk.Scope != ScopeGlobal || !strings.HasPrefix(sk.Dir, filepath.Join(os.Getenv("SCRIBE_HOME"), "skills")) {
		t.Fatalf("global install landed at %s", sk.Dir)
	}
	find(t, mustDiscover(t, root), "beta")
	if _, err := Install(root, t.TempDir(), ScopeGlobal); err == nil {
		t.Fatal("directory without SKILL.md should be rejected")
	}
	if _, err := Install(root, src, "planet"); err == nil {
		t.Fatal("unknown scope should be rejected")
	}
	if _, err := Install("", src, ScopeProject); err == nil {
		t.Fatal("project install with no root should be rejected")
	}
}

func TestInstallFromGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root, _ := env(t)
	repo := t.TempDir()
	writeSkill(t, filepath.Join(repo, "skills", "from-git"), skill("from-git", "Came from git.", "g"))
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sk, err := Install(root, "file://"+repo+"#skills/from-git", ScopeProject)
	if err != nil {
		t.Fatalf("git install: %v", err)
	}
	if sk.Name != "from-git" {
		t.Fatalf("git install name: %+v", sk)
	}
	if _, err := os.Stat(filepath.Join(sk.Dir, ".git")); err == nil {
		t.Fatal(".git must not be copied")
	}
	if _, err := Install(root, "file:///nonexistent/repo.git", ScopeProject); err == nil {
		t.Fatal("bad git URL should fail")
	}
}

// ---- tool ----

func runSkills(t *testing.T, root, args string) tools.Result {
	t.Helper()
	r := tools.NewRegistry()
	Register(r)
	tool, ok := r.Get("skills")
	if !ok {
		t.Fatal("skills tool not registered")
	}
	if !tool.ReadOnly() {
		t.Fatal("skills tool should report ReadOnly")
	}
	return tool.Run(context.Background(), &tools.Env{Workdir: root}, json.RawMessage(args))
}

func TestSkillsToolActions(t *testing.T) {
	root, _ := env(t)
	writeSkill(t, filepath.Join(root, ".scribe", "skills", "release"), skill("release", "Cut a release.", "Tag, then push."))

	res := runSkills(t, root, `{"action":"list"}`)
	if res.IsError || !strings.Contains(res.Output, "release (project): Cut a release.") {
		t.Fatalf("list: %+v", res)
	}
	res = runSkills(t, root, `{"action":"load","name":"release"}`)
	if res.IsError || !strings.Contains(res.Output, "Tag, then push.") {
		t.Fatalf("load: %+v", res)
	}
	res = runSkills(t, root, `{"action":"load","name":"releas"}`)
	if !res.IsError || !strings.Contains(res.Output, "release") {
		t.Fatalf("near-miss load should suggest the real name: %+v", res)
	}
	if res := runSkills(t, root, `{"action":"install"}`); !res.IsError {
		t.Fatal("unknown action should error")
	}
	if res := runSkills(t, root, `{"action":"load"}`); !res.IsError {
		t.Fatal("load without name should error")
	}
	if res := runSkills(t, t.TempDir(), `{"action":"list"}`); res.IsError || !strings.Contains(res.Output, "No skills") {
		t.Fatalf("empty list message: %+v", res)
	}
}

func mustDiscover(t *testing.T, root string) *Set {
	t.Helper()
	set, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestUnknownActionSuggestsNearMiss(t *testing.T) {
	root, _ := env(t)
	res := runSkills(t, root, `{"action":"lsit"}`)
	if !res.IsError || !strings.Contains(res.Output, `Did you mean "list"`) {
		t.Fatalf("typo action: %+v", res)
	}
	res = runSkills(t, root, `{"action":"install"}`)
	if !res.IsError || strings.Contains(res.Output, "Did you mean") {
		t.Fatalf("far-off action should not get a suggestion: %+v", res)
	}
}

func TestNameSuggestionsRankByEditDistance(t *testing.T) {
	set := &Set{Skills: []Skill{{Name: "deploy-prod"}, {Name: "review"}, {Name: "reviewer-notes"}}}
	if got := namesOrNone(set, "reveiw"); !strings.HasPrefix(got, "review") {
		t.Fatalf("closest name should lead: %q", got)
	}
	if got := closest("lsit", []string{"list", "load"}); got != "list" {
		t.Fatalf("closest(lsit) = %q", got)
	}
	if got := closest("zzzz", []string{"list", "load"}); got != "" {
		t.Fatalf("closest(zzzz) = %q, want none", got)
	}
	if d := editDistance("kitten", "sitting"); d != 3 {
		t.Fatalf("editDistance = %d, want 3", d)
	}
}
