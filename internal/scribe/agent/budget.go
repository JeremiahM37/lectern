package agent

import (
	"encoding/json"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// StepBudget shapes a subagent's run (spec 03 §3.5): wrap-up hints as the
// step cap nears, a tool-less final step, an edit-only window for code agents,
// a hint after five read-only steps in a row, and a token limit. The cap
// itself is Options.MaxSteps.
type StepBudget struct {
	// Kind is "explore", "code" or "web". Code agents get edit-only tools
	// near the end; the wording differs by kind.
	Kind string
	// NudgeAt is the step from which the wrap-up hint is given (8 for
	// explore, 12 for code). Zero means no wrap-up hint.
	NudgeAt int
	// Window is the context window the token thresholds use: the model's
	// window capped at 200 000. Zero means 200 000.
	Window int
}

// tokenWindow is the input size the token thresholds are measured against.
const (
	defaultBudgetWindow = 200_000
	tokenNudgeAt        = 0.8
	tokenStopAt         = 0.9
	readOnlyStreakHint  = 5
)

// editOnlyTools are what a code agent may still call in its last steps.
var editOnlyTools = map[string]bool{"edit_file": true, "multi_edit": true, "report_finding": true}

// stepPolicy is decided before each step from the step number: whether the
// step may use tools at all, and whether only edit tools are offered.
type stepPolicy struct {
	textOnly bool
	editOnly bool
}

// policyFor works out the policy for step (1-based) of a run capped at max.
// pendingText forces a text-only step, which is how the token nudge works.
func policyFor(b *StepBudget, max, step int, pendingText bool) stepPolicy {
	if b == nil || max <= 0 {
		return stepPolicy{textOnly: pendingText}
	}
	left := max - step + 1 // steps left, this one included
	return stepPolicy{
		textOnly: pendingText || left <= 1,
		editOnly: b.Kind == "code" && left <= 2,
	}
}

// allows reports whether the policy lets a step call the named tool.
func (p stepPolicy) allows(name string) bool {
	switch {
	case p.textOnly:
		return false
	case p.editOnly:
		return editOnlyTools[name]
	}
	return true
}

// filterTools narrows offered tool specs to the policy: none for a text-only
// step, only the edit tools for an edit-only one.
func (p stepPolicy) filterTools(specs []llm.ToolSpec) []llm.ToolSpec {
	if p.textOnly {
		return nil
	}
	if !p.editOnly {
		return specs
	}
	var out []llm.ToolSpec
	for _, s := range specs {
		if editOnlyTools[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// wrapUpHint is the hint for step next (1-based), or "" when none is due.
func wrapUpHint(b *StepBudget, max, next int) string {
	if b == nil || max <= 0 {
		return ""
	}
	left := max - next + 1
	switch {
	case left <= 1:
		return "This is your final step. No tools are available for it: reply now with a short text summary of what you found or changed."
	case b.Kind == "code" && left <= 2:
		return "Only edit_file, multi_edit and report_finding are available for the next steps. Make the remaining edits now."
	case b.Kind != "code" && left <= 2:
		return "Stop searching now. Write your answer from what you have."
	case b.NudgeAt > 0 && next >= b.NudgeAt && b.Kind == "code":
		return fmt.Sprintf("You have used %d of %d steps. Finish the edits soon.", next-1, max)
	case b.NudgeAt > 0 && next >= b.NudgeAt:
		return fmt.Sprintf("You have used %d of %d steps. Wrap up: summarize what you have found.", next-1, max)
	}
	return ""
}

// readStreakHint is the hint after readOnlyStreakHint read-only steps.
func readStreakHint(b *StepBudget) string {
	if b == nil {
		return ""
	}
	if b.Kind == "code" {
		return fmt.Sprintf("You have made %d read-only steps in a row. Apply the edits with multi_edit now.", readOnlyStreakHint)
	}
	return fmt.Sprintf("You have made %d read-only steps in a row. Summarize what you have found before reading more.", readOnlyStreakHint)
}

// tokenCheck runs after each step of a budgeted agent. At 80% of the window
// it asks for a text summary and makes the next step text-only, once. At 90%
// after that nudge it stops the run and returns the answer so far.
func (a *Agent) tokenCheck() (hint string, stop bool) {
	b := a.opts.Budget
	if b == nil {
		return "", false
	}
	window := b.Window
	if window <= 0 {
		window = defaultBudgetWindow
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	frac := float64(a.lastIn) / float64(window)
	switch {
	case frac >= tokenStopAt && a.tokenNudged:
		return "", true
	case frac >= tokenNudgeAt && !a.tokenNudged:
		a.tokenNudged = true
		a.pendingText = true
		return "Your context is nearly full. Write a concise text summary of what you have found now.", false
	}
	return "", false
}

// readPaths lists the files a successful read call touched.
func readPaths(args json.RawMessage) []string {
	var a struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	_ = json.Unmarshal(args, &a)
	var out []string
	if a.Path != "" {
		out = append(out, a.Path)
	}
	return append(out, a.Paths...)
}
