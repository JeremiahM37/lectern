package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/skills"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// isolatedHome points Scribe's global directory and the user's home at fresh
// temp dirs, so the test never reads or writes the real ones.
func isolatedHome(t *testing.T) {
	t.Helper()
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// newTestBackend is a backend around a bare App rooted at root, with no model.
func newTestBackend(t *testing.T, root string) (*scribeBackend, *App) {
	t.Helper()
	set, err := skills.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Workdir: root, Tools: tools.NewRegistry(), Skills: set}
	t.Cleanup(func() {
		if a.ext.mcp != nil {
			_ = a.ext.mcp.Close()
		}
	})
	return &scribeBackend{app: a}, a
}

func writeSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	sd := filepath.Join(dir, name)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: test skill " + name + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(sd, skills.SkillFile), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestActiveSkillReachesTheNextSystemPrompt is the contract the skill toggle
// depends on: loading a skill changes the system prompt of the next request
// (main agent and subagents alike), and unloading it removes the body again.
func TestActiveSkillReachesTheNextSystemPrompt(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, ".scribe", "skills"), "release-notes", "Write notes with the MARKER-RELEASE-BODY section.")
	b, a := newTestBackend(t, root)

	if strings.Contains(a.systemPrompt("")("default"), "MARKER-RELEASE-BODY") {
		t.Fatal("an inactive skill's body is in the prompt")
	}
	if !strings.Contains(a.systemPrompt("")("default"), "release-notes") {
		t.Fatal("the skill catalog line is missing")
	}

	if err := b.SetSkillActive("release-notes", true); err != nil {
		t.Fatal(err)
	}
	// The lead's prompt stays the same bytes; the skill is latched into the
	// history by the next turn (agent.SetSkills), not put in the prompt.
	if strings.Contains(a.systemPrompt("")("default"), "MARKER-RELEASE-BODY") {
		t.Fatal("the lead's system prompt must not change when a skill loads")
	}
	block := a.loadedSkillsBlock()
	if !strings.HasPrefix(block, "<loaded_skills>") || !strings.Contains(block, "MARKER-RELEASE-BODY") {
		t.Fatalf("active skill missing from the block the next turn latches:\n%s", block)
	}
	if sub := a.systemPrompt("explore")("default"); !strings.Contains(sub, "# Loaded skills") || !strings.Contains(sub, "MARKER-RELEASE-BODY") {
		t.Fatal("active skill missing from a subagent's prompt")
	}

	if err := b.SetSkillActive("release-notes", false); err != nil {
		t.Fatal(err)
	}
	if block := a.loadedSkillsBlock(); block != "" {
		t.Fatalf("unloaded skill is still latched: %s", block)
	}
}

// TestActiveSkillsPersistPerProject checks that the choice is saved in the
// project and read back by a new session on the same project.
func TestActiveSkillsPersistPerProject(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, ".scribe", "skills"), "db-migrations", "Plan migrations. MARKER-DB")
	b, _ := newTestBackend(t, root)
	if err := b.SetSkillActive("db-migrations", true); err != nil {
		t.Fatal(err)
	}

	_, fresh := newTestBackend(t, root)
	if !strings.Contains(fresh.loadedSkillsBlock(), "MARKER-DB") {
		t.Fatal("a new session on the same project did not load the saved skill")
	}
	other, otherApp := newTestBackend(t, t.TempDir())
	if got := other.ActiveSkills(); len(got) != 0 {
		t.Fatalf("another project inherited the skill: %v", got)
	}
	if strings.Contains(otherApp.loadedSkillsBlock(), "MARKER-DB") {
		t.Fatal("another project's loaded skills have the skill")
	}
}

func TestSetSkillActiveRefusesUnknownSkill(t *testing.T) {
	isolatedHome(t)
	b, _ := newTestBackend(t, t.TempDir())
	if err := b.SetSkillActive("no-such-skill", true); err == nil {
		t.Fatal("loading a skill that is not installed should fail")
	}
}

// TestInstallSkillThenLoadIt installs from a local directory into the project,
// which rediscovers skills, and then loads it.
func TestInstallSkillThenLoadIt(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	src := t.TempDir()
	writeSkill(t, src, "review-skill", "Review carefully. MARKER-REVIEW")
	b, a := newTestBackend(t, root)

	name, err := b.InstallSkill(filepath.Join(src, "review-skill"), "project")
	if err != nil {
		t.Fatal(err)
	}
	if name != "review-skill" {
		t.Fatalf("installed name = %q", name)
	}
	if _, err := os.Stat(filepath.Join(root, ".scribe", "skills", "review-skill", skills.SkillFile)); err != nil {
		t.Fatalf("skill not copied into the project: %v", err)
	}
	if err := b.SetSkillActive(name, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.loadedSkillsBlock(), "MARKER-REVIEW") {
		t.Fatal("freshly installed and loaded skill is not in the block the next turn latches")
	}
}

// TestSaveAndRemoveMCPServerWritesItsScope exercises the config writes behind
// the MCP form: each save lands in the scope's file, keeps the other keys,
// moves between scopes, and removal takes the entry back out.
func TestSaveAndRemoveMCPServerWritesItsScope(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	if err := os.WriteFile(config.GlobalPath(), []byte(`{"model":"anthropic/claude-sonnet-5"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := newTestBackend(t, root)

	// Add a global stdio server. A failing command keeps the test fast: the
	// point is what is written and what the manager reports.
	demo := tui.MCPServerSpec{Name: "demo", Scope: "global", Transport: "stdio", Command: "/bin/false", Args: []string{"x"}, Env: map[string]string{"K": "V"}}
	if err := b.SaveMCPServer(demo, nil); err != nil {
		t.Fatal(err)
	}
	global, _ := os.ReadFile(config.GlobalPath())
	if !strings.Contains(string(global), `"demo"`) || !strings.Contains(string(global), `"model": "anthropic/claude-sonnet-5"`) {
		t.Fatalf("global config after add:\n%s", global)
	}
	got, ok := findMCPSpec(b.MCPServers(), "demo")
	if !ok || got.Command != "/bin/false" || got.Env["K"] != "V" || got.Scope != "global" {
		t.Fatalf("listed server = %+v ok=%v", got, ok)
	}

	// Move it to the project scope, disabled, and rename it.
	moved := demo
	moved.Name, moved.Scope, moved.Disabled = "demo2", "project", true
	if err := b.SaveMCPServer(moved, &demo); err != nil {
		t.Fatal(err)
	}
	global, _ = os.ReadFile(config.GlobalPath())
	if strings.Contains(string(global), `"demo"`) {
		t.Fatalf("server left in the global config after moving scope:\n%s", global)
	}
	project, err := os.ReadFile(config.ProjectPath(root))
	if err != nil || !strings.Contains(string(project), `"demo2"`) || !strings.Contains(string(project), `"disabled": true`) {
		t.Fatalf("project config after move: %s err=%v", project, err)
	}
	got, ok = findMCPSpec(b.MCPServers(), "demo2")
	if !ok || !got.Disabled || got.Scope != "project" {
		t.Fatalf("after move, listed = %+v ok=%v", got, ok)
	}

	if err := b.RemoveMCPServer("demo2", "project"); err != nil {
		t.Fatal(err)
	}
	project, _ = os.ReadFile(config.ProjectPath(root))
	if strings.Contains(string(project), "demo2") {
		t.Fatalf("project config after remove:\n%s", project)
	}
	if _, ok := findMCPSpec(b.MCPServers(), "demo2"); ok {
		t.Fatal("removed server still listed")
	}
	if err := b.RemoveMCPServer("demo2", "project"); err == nil {
		t.Fatal("removing a server that is not there should fail")
	}
}

func TestSaveMCPServerValidatesBeforeWriting(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	b, _ := newTestBackend(t, root)
	bad := []tui.MCPServerSpec{
		{Name: "bad name", Scope: "global", Transport: "stdio", Command: "x"},
		{Name: "a__b", Scope: "global", Transport: "stdio", Command: "x"},
		{Name: "ok", Scope: "global", Transport: "stdio"},
		{Name: "ok", Scope: "global", Transport: "http", URL: "ftp://nope"},
		{Name: "ok", Scope: "elsewhere", Transport: "stdio", Command: "x"},
	}
	for _, spec := range bad {
		if err := b.SaveMCPServer(spec, nil); err == nil {
			t.Errorf("accepted %+v", spec)
		}
	}
	if _, err := os.Stat(config.GlobalPath()); !os.IsNotExist(err) {
		t.Fatal("a rejected server still wrote the config")
	}
}

func findMCPSpec(list []tui.MCPServerSpec, name string) (tui.MCPServerSpec, bool) {
	i := slices.IndexFunc(list, func(s tui.MCPServerSpec) bool { return s.Name == name })
	if i < 0 {
		return tui.MCPServerSpec{}, false
	}
	return list[i], true
}
