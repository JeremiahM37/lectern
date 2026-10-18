package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemIncludesModeOverlayAndInstructions(t *testing.T) {
	in := PromptInput{
		Workdir:      "/work/proj",
		Date:         "2026-10-16",
		ModelRef:     "fake/model-a",
		Instructions: []Instruction{{Path: "/work/proj/AGENTS.md", Text: "\nUse tabs.\n", Name: "AGENTS.md"}},
	}

	plan := BuildSystem(in, "plan")
	for _, want := range []string{
		"- Working directory: /work/proj",
		"- Working directory: /work/proj",
		"- Date: 2026-10-16",
		"# Project instructions\n\n[project:AGENTS.md]\nUse tabs.",
	} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan prompt lacks %q", want)
		}
	}
	if !strings.Contains(plan, "You are Scribe") {
		t.Fatal("base prompt missing")
	}

	// The system prompt is the same bytes in every mode; the banner for a
	// mode is ModeBanner, latched into the history by the agent.
	def := BuildSystem(in, "default")
	if strings.Contains(def, "# Mode:") {
		t.Fatal("the mode banner must not be in the system prompt")
	}
	for _, m := range Modes {
		if got := BuildSystem(in, m); got != def {
			t.Fatalf("system prompt for mode %q differs from default; the prefix must be stable across modes", m)
		}
	}
	if !strings.Contains(ModeBanner("architect"), "# Mode: architect") {
		t.Fatal("architect banner missing")
	}

	explore := BuildSystem(PromptInput{Workdir: "/w", Subagent: "explore"}, "default")
	if !strings.Contains(explore, "You are a subagent") || !strings.Contains(explore, "You are an explore agent: read-only.") {
		t.Fatal("explore subagent role missing")
	}
	if strings.Contains(BuildSystem(PromptInput{Workdir: "/w"}, "default"), "You are a subagent") {
		t.Fatal("main agent got the subagent role")
	}
}

func TestLoadInstructionsNearestLast(t *testing.T) {
	base := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(base, rel)
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte(text), 0o644))
	}
	write("home/instructions.md", "home rules")
	write(".git/HEAD", "ref: refs/heads/main\n") // base is the repository root
	write("AGENTS.md", "root rules")
	write("proj/SCRIBE.md", "   \n\t\n") // whitespace only: skipped
	write("proj/CLAUDE.md", "proj rules")
	write("proj/sub/SCRIBE.md", "scribe sub")
	write("proj/sub/AGENTS.md", "sub rules")

	got := LoadInstructions(filepath.Join(base, "proj", "sub"), filepath.Join(base, "home"), nil)

	// Parents of the temp dir are outside the test's control; keep only ours.
	var ours []string
	for _, ins := range got {
		if strings.HasPrefix(ins.Path, base+string(filepath.Separator)) {
			ours = append(ours, ins.Text)
		}
	}
	want := []string{"home rules", "root rules", "proj rules", "scribe sub", "sub rules"}
	if strings.Join(ours, " | ") != strings.Join(want, " | ") {
		t.Fatalf("instruction order = %q, want %q", ours, want)
	}
}

// Files above the repository root (a home folder's CLAUDE.md, say) are not
// project instructions for a repository inside it.
func TestLoadInstructionsStopsAtTheRepositoryRoot(t *testing.T) {
	base := t.TempDir()
	must(t, os.WriteFile(filepath.Join(base, "CLAUDE.md"), []byte("outside rules"), 0o644))
	repo := filepath.Join(base, "repo")
	must(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	must(t, os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("repo rules"), 0o644))
	var texts []string
	for _, ins := range LoadInstructions(repo, "", nil) {
		texts = append(texts, ins.Text)
	}
	if got := strings.Join(texts, "|"); got != "repo rules" {
		t.Fatalf("instructions = %q, want only the repository's", got)
	}
}

// The system prompt is the same bytes whatever the mode and wherever the
// workdir is, so a mode switch does not break the cached prefix.
func TestSystemPromptPrefixIsStableAcrossModes(t *testing.T) {
	in := PromptInput{Workdir: "/work/proj", Date: "2026-10-16", ModelRef: "p/m"}
	first := BuildSystem(in, "default")
	for _, m := range Modes {
		if got := BuildSystem(in, m); got != first {
			t.Errorf("mode %s changed the system prompt", m)
		}
	}
}

// Each mode with a banner has one, and the banners are the only mode text.
func TestModeNoticeLatchesOncePerChange(t *testing.T) {
	cases := []struct{ prev, mode, want string }{
		{"", "default", ""},
		{"default", "default", ""},
		{"", "plan", "plan"},
		{"plan", "plan", ""},
		{"plan", "architect", "architect"},
		{"architect", "default", "back in default mode"},
		{"default", "auto", "auto"},
	}
	for _, c := range cases {
		got := modeNotice(c.prev, c.mode)
		switch {
		case c.want == "" && got != "":
			t.Errorf("modeNotice(%q,%q) = %q, want none", c.prev, c.mode, got)
		case c.want != "" && !strings.Contains(got, c.want):
			t.Errorf("modeNotice(%q,%q) = %q, want it to mention %q", c.prev, c.mode, got, c.want)
		}
	}
}
