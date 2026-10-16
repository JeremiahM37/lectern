package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelFamily(t *testing.T) {
	cases := map[string]string{
		"anthropic/claude-opus-4":          "claude",
		"openrouter/anthropic/claude-x":    "claude",
		"moonshot/kimi-k2":                 "claude",
		"openai/gpt-5":                     "openai",
		"xai/grok-4":                       "openai",
		"deepseek/deepseek-chat":           "openai",
		"deepseek/deepseek-reasoner":       "deepseek-reasoner",
		"deepseek/deepseek-r1":             "deepseek-reasoner",
		"google/gemini-2.5-flash":          "google",
		"ollama/qwen3.6:35b-a3b":           "default",
		"llamacpp/llama-3":                 "default",
		"":                                 "default",
		"google/gemini-2.5-pro-deepseek-x": "google", // the last segment decides, gemini wins first
	}
	for ref, want := range cases {
		if got := ModelFamily(ref); got != want {
			t.Errorf("ModelFamily(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestFamilyBlockFollowsBasePrompt(t *testing.T) {
	claude := BuildSystem(PromptInput{Workdir: "/w", ModelRef: "anthropic/claude-x"}, "default")
	if !strings.Contains(claude, "Put independent tool calls in one block") {
		t.Fatal("claude family block missing")
	}
	openai := BuildSystem(PromptInput{Workdir: "/w", ModelRef: "openai/gpt-5"}, "default")
	if strings.Contains(openai, "Put independent tool calls in one block") || !strings.Contains(openai, "about 70 percent sure") {
		t.Fatal("openai family block wrong")
	}
	def := BuildSystem(PromptInput{Workdir: "/w", ModelRef: "ollama/qwen"}, "default")
	if strings.Contains(def, "# Style") {
		t.Fatal("default family should add no tone block")
	}
}

func TestSharedRulesAreInTheBasePrompt(t *testing.T) {
	sys := BuildSystem(PromptInput{Workdir: "/w"}, "default")
	for _, want := range []string{
		"Validate input only at system boundaries",
		"Delete dead code cleanly",
		"Reference code as path:line",
		"Never claim you ran or verified something you did not",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("base prompt lacks %q", want)
		}
	}
}

func TestWorkingDirectoryLineSaysNotToCd(t *testing.T) {
	sys := BuildSystem(PromptInput{Workdir: "/w/proj"}, "default")
	if !strings.Contains(sys, "- Working directory: /w/proj (shell commands already run here; do not prepend cd)") {
		t.Fatalf("working directory line wrong:\n%s", sys)
	}
}

func TestFlashTemporalNoteUsesSessionYear(t *testing.T) {
	flash := BuildSystem(PromptInput{Workdir: "/w", ModelRef: "google/gemini-2.5-flash", Date: "2026-10-16"}, "default")
	if !strings.Contains(flash, "the current year is 2026") {
		t.Fatal("flash temporal note missing")
	}
	pro := BuildSystem(PromptInput{Workdir: "/w", ModelRef: "google/gemini-2.5-pro", Date: "2026-10-16"}, "default")
	if strings.Contains(pro, "current year") {
		t.Fatal("temporal note should be Flash only")
	}
	noDate := temporalNote("")
	if strings.Contains(noDate, "current year") {
		t.Fatalf("no date must not invent a year: %q", noDate)
	}
}

func TestGitHubCLILine(t *testing.T) {
	yes, no := true, false
	if !strings.Contains(BuildSystem(PromptInput{Workdir: "/w", GitHub: &yes}, "default"), "GitHub CLI (gh) is installed") {
		t.Fatal("gh line missing when gh is available")
	}
	if strings.Contains(BuildSystem(PromptInput{Workdir: "/w", GitHub: &no}, "default"), "GitHub CLI") {
		t.Fatal("gh line present when gh is unavailable")
	}
}

func TestReasonerPromptHasNoToolGuidance(t *testing.T) {
	sys := BuildSystem(PromptInput{Workdir: "/w", ModelRef: "deepseek/deepseek-reasoner", Skills: "- lint: lint things"}, "default")
	if strings.Contains(sys, "# Tools") || strings.Contains(sys, "# Skills") || strings.Contains(sys, "# How you work") {
		t.Fatal("reasoner prompt carries tool guidance")
	}
	if !strings.Contains(sys, "You are Scribe") || !strings.Contains(sys, "# Answer voice") {
		t.Fatal("reasoner prompt lacks identity or answer voice")
	}
}

func TestSubagentRolesCoverCodeAndDesloppify(t *testing.T) {
	code := BuildSystem(PromptInput{Workdir: "/w", Subagent: "code"}, "default")
	if !strings.Contains(code, "You are a code agent. Edit only the files your task names") {
		t.Fatal("code role missing")
	}
	clean := BuildSystem(PromptInput{Workdir: "/w", Subagent: "desloppify"}, "default")
	if !strings.Contains(clean, "cleanup agent") || !strings.Contains(clean, "Keep TODO markers") {
		t.Fatal("desloppify role missing")
	}
}

func TestBuildSystemIsMemoised(t *testing.T) {
	in := PromptInput{Workdir: "/memo", Date: "2026-10-16", ModelRef: "anthropic/claude-x"}
	first := BuildSystem(in, "plan")
	if BuildSystem(in, "plan") != first {
		t.Fatal("same input gave different bytes")
	}
	in2 := in
	in2.Instructions = []Instruction{{Path: "/memo/CLAUDE.md", Text: "changed", Name: "CLAUDE.md"}}
	if BuildSystem(in2, "plan") == first {
		t.Fatal("a changed instruction did not change the prompt")
	}
	if BuildSystem(in, "plan") != first {
		t.Fatal("the original input no longer maps to its prompt")
	}
}

func TestLabelsAndGlobalPreamble(t *testing.T) {
	ins := []Instruction{
		{Path: "/home/u/instructions.md", Text: "be brief", Scope: "global", Name: "instructions.md"},
		{Path: "/p/CLAUDE.md", Text: "use tabs", Scope: "project", Name: "CLAUDE.md"},
	}
	sys := BuildSystem(PromptInput{Workdir: "/p", Instructions: ins}, "default")
	for _, want := range []string{
		"Global instructions apply everywhere. Where a project file conflicts with a global one, the project file wins.",
		"[global:instructions.md]\nbe brief",
		"[project:CLAUDE.md]\nuse tabs",
	} {
		if !strings.Contains(sys, want) {
			t.Fatalf("instruction block lacks %q:\n%s", want, sys)
		}
	}
	if strings.Index(sys, "[global:") > strings.Index(sys, "[project:") {
		t.Fatal("global files must come before project files")
	}
	noGlobal := BuildSystem(PromptInput{Workdir: "/p", Instructions: ins[1:]}, "default")
	if strings.Contains(noGlobal, "apply everywhere") {
		t.Fatal("preamble without any global file")
	}
}

func TestInstructionSourcesExpandAndDefaults(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(root, rel)
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte(text), 0o644))
	}
	write(".cursorrules", "cursor says hi")
	write(".cline/rules", "cline rules")
	write(".scribe/instructions.md", "scribe alt")
	write("CLAUDE.md", "claude rules")

	// Off-by-default sources are not read when config is nil.
	def := LoadInstructions(root, "", nil)
	for _, ins := range def {
		if strings.Contains(ins.Text, "cursor") {
			t.Fatal("cursorrules loaded by default")
		}
	}
	// The first existing non-empty candidate of a source wins.
	got := LoadInstructions(root, "", []string{"scribe", "claude"})
	var texts []string
	for _, ins := range got {
		texts = append(texts, ins.Text)
	}
	if strings.Join(texts, "|") != "scribe alt|claude rules" {
		t.Fatalf("sources = %q", texts)
	}
	// Enabling a source by id reads its files; a literal name still works.
	got = LoadInstructions(root, "", []string{"cursorrules", "cline", "CLAUDE.md"})
	texts = texts[:0]
	for _, ins := range got {
		texts = append(texts, ins.Text)
	}
	if strings.Join(texts, "|") != "cursor says hi|cline rules|claude rules" {
		t.Fatalf("enabled sources = %q", texts)
	}
	if got[0].Scope != "project" || got[0].Name != ".cursorrules" {
		t.Fatalf("project label = %+v", got[0])
	}
}

func TestGlobalFileSkippedWhenHomeIsProjectRoot(t *testing.T) {
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "instructions.md"), []byte("same dir"), 0o644))
	for _, ins := range LoadInstructions(root, root, []string{}) {
		if ins.Scope == "global" {
			t.Fatal("global scope loaded for the project root itself")
		}
	}
}
