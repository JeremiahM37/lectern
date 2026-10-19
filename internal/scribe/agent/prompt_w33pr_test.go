package agent

import (
	"strings"
	"testing"
)

// w33SizeInput is a fixed session input used to measure prompt size.
func w33SizeInput(model string) PromptInput {
	return PromptInput{
		Workdir:  "/work/proj",
		Date:     "2026-10-17",
		Branch:   "main",
		ModelRef: model,
		CodeMap:  "- internal/x.go\n  func Run(ctx context.Context) error  (line 12)\n",
	}
}

// Before this change the default-family prompt was 4943 bytes and claude
// 5153, openai 5192, google 5232 (measured with the same input). The added
// default block and the snapshot sentence together cost well under 1 KB.
func TestW33PromptGrowthStaysSmall(t *testing.T) {
	for _, m := range []string{"anthropic/claude-x", "openai/gpt-5", "google/gemini-2.5-pro", "ollama/qwen3.6"} {
		s := BuildSystem(w33SizeInput(m), "default")
		t.Logf("w33 size %-24s %5d bytes", m, len(s))
		if len(s) > 5600 {
			t.Errorf("%s prompt is %d bytes; growth beyond the budget", m, len(s))
		}
	}
	if n := len(familyBlocks["default"]); n > 300 {
		t.Errorf("default family block is %d bytes, want at most 300", n)
	}
	if n := len(codeMapIntro); n > 400 {
		t.Errorf("code map intro is %d bytes, want at most 400", n)
	}
}

// The code map is not in the system prompt any more: it is latched into the
// conversation (codemap_latch.go), so the system prompt is the same bytes
// whatever the map holds. The snapshot wording lives with the latch.
func TestW33CodeMapIsNotInTheSystemPrompt(t *testing.T) {
	for _, m := range []string{"anthropic/claude-x", "openai/gpt-5", "google/gemini-2.5-pro", "ollama/qwen3.6"} {
		with := BuildSystem(w33SizeInput(m), "default")
		none := w33SizeInput(m)
		none.CodeMap = ""
		if with != BuildSystem(none, "default") {
			t.Errorf("%s: the system prompt changed with the code map", m)
		}
		if strings.Contains(with, "# Code map") || strings.Contains(with, "central files") {
			t.Errorf("%s: the code map leaked into the system prompt", m)
		}
	}
}

// Reasoner models get no code map, so they get no snapshot sentence either.
func TestW33ReasonerHasNoCodeMapSnapshot(t *testing.T) {
	in := w33SizeInput("deepseek/deepseek-reasoner")
	if strings.Contains(BuildSystem(in, "default"), "central files") {
		t.Error("reasoner prompt must not carry the code map")
	}
}

// Each family gets its own tone block; the default family gets the step
// framing, and no other family does.
func TestW33FamilyBlocksAreDistinct(t *testing.T) {
	cases := map[string]struct {
		model, want string
		absent      []string
	}{
		"claude":  {"anthropic/claude-x", "Put independent tool calls in one block", []string{"Work in steps", "70 percent sure"}},
		"openai":  {"openai/gpt-5", "about 70 percent sure", []string{"Work in steps", "Put independent tool calls"}},
		"google":  {"google/gemini-2.5-pro", "Follow these mandates in order", []string{"Work in steps", "70 percent sure"}},
		"default": {"ollama/qwen3.6", "Work in steps: use tools until the task is done, then give one final answer at the end.", []string{"Put independent tool calls", "70 percent sure", "Follow these mandates"}},
	}
	for fam, c := range cases {
		s := BuildSystem(w33SizeInput(c.model), "default")
		if !strings.Contains(s, c.want) {
			t.Errorf("%s: family block %q missing", fam, c.want)
		}
		for _, bad := range c.absent {
			if strings.Contains(s, bad) {
				t.Errorf("%s: prompt wrongly carries %q", fam, bad)
			}
		}
	}
}

// Reasoner models keep their identity-only prompt: no working-style block.
func TestW33ReasonerKeepsNoFamilyBlock(t *testing.T) {
	s := BuildSystem(w33SizeInput("deepseek/deepseek-reasoner"), "default")
	if strings.Contains(s, "Work in steps") {
		t.Error("reasoner prompt must not get the default family block")
	}
}

// The system prompt is byte-identical across steps of a session: the same
// input gives the same bytes every time, for every family.
func TestW33PromptIsByteIdenticalAcrossSteps(t *testing.T) {
	for _, m := range []string{"anthropic/claude-x", "openai/gpt-5", "google/gemini-2.5-pro", "ollama/qwen3.6", "deepseek/deepseek-reasoner"} {
		in := w33SizeInput(m)
		first := BuildSystem(in, "default")
		for i := 0; i < 5; i++ {
			if got := BuildSystem(in, "default"); got != first {
				t.Fatalf("%s: prompt changed on step %d with the same input", m, i+1)
			}
		}
	}
}

// The map tools come from the code map. With the map off the guidance must not
// name them; with it on the guidance names them and says to plan from the map.
func TestToolGuidanceNamesMapToolsOnlyWithAMap(t *testing.T) {
	withMap := BuildSystem(PromptInput{Workdir: "/w", ModelRef: "ollama/qwen3.6"}, "default")
	if !strings.Contains(withMap, "find_symbol, symbol_refs, code_impact and file_outline") {
		t.Fatal("with the map running, the guidance must name the map tools")
	}
	if !strings.Contains(withMap, "Plan from the code map") {
		t.Error("with the map running, the guidance must say to plan from the map")
	}
	noMap := BuildSystem(PromptInput{Workdir: "/w", ModelRef: "ollama/qwen3.6", NoMap: true}, "default")
	for _, bad := range []string{"find_symbol", "code_impact", "code map"} {
		if strings.Contains(noMap, bad) {
			t.Errorf("with no map, the guidance still names %q", bad)
		}
	}
	if !strings.Contains(noMap, "navigate and diagnostics use language servers") {
		t.Error("the no-map guidance lost the language server line")
	}
}
