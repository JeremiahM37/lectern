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
	if n := len(codeMapPreamble); n > 300 {
		t.Errorf("code map preamble is %d bytes, want at most 300", n)
	}
}

// The code map is a snapshot from session start. The prompt must say so, and
// say the model should read a file before editing it.
func TestW33CodeMapSaysItIsASnapshot(t *testing.T) {
	for _, m := range []string{"anthropic/claude-x", "openai/gpt-5", "google/gemini-2.5-pro", "ollama/qwen3.6"} {
		s := BuildSystem(w33SizeInput(m), "default")
		want := "# Code map\nThe most central files and their key symbols, ranked by how much of the codebase depends on them. This is a snapshot taken at session start: files may have changed since, so read a file before you edit it.\n\n"
		if !strings.Contains(s, want) {
			t.Errorf("%s: code map preamble missing or changed", m)
		}
		if strings.Index(s, want) < strings.Index(s, "# Environment") {
			t.Errorf("%s: code map must come after the environment block", m)
		}
	}
	none := w33SizeInput("ollama/qwen3.6")
	none.CodeMap = ""
	if strings.Contains(BuildSystem(none, "default"), "snapshot taken at session start") {
		t.Error("the snapshot sentence must not appear without a code map")
	}
}

// Reasoner models get no code map, so they get no snapshot sentence either.
func TestW33ReasonerHasNoCodeMapSnapshot(t *testing.T) {
	in := w33SizeInput("deepseek/deepseek-reasoner")
	if strings.Contains(BuildSystem(in, "default"), "snapshot taken at session start") {
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
