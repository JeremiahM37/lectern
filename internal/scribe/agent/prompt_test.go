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
		Instructions: []Instruction{{Path: "/work/proj/AGENTS.md", Text: "\nUse tabs.\n"}},
	}

	plan := BuildSystem(in, "plan")
	for _, want := range []string{
		"# Mode: plan",
		"You are in plan mode: read-only.",
		"- Working directory: /work/proj",
		"- Date: 2026-10-16",
		"# Instructions from /work/proj/AGENTS.md\nUse tabs.",
	} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan prompt lacks %q", want)
		}
	}
	if !strings.Contains(plan, "You are Scribe") {
		t.Fatal("base prompt missing")
	}

	def := BuildSystem(in, "default")
	if strings.Contains(def, "# Mode:") {
		t.Fatal("default mode should have no overlay")
	}
	if !strings.Contains(BuildSystem(in, "architect"), "# Mode: architect") {
		t.Fatal("architect overlay missing")
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
