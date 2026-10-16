package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// SummaryCompactor replaces older history with a summary plus a mechanical
// ledger of what was touched (files read and edited, commands run,
// failures), keeping the recent tail verbatim.
//
// Strategy "v2" (the default) builds the summary from the working state
// (see workingstate.go) and asks a model only to fill gaps when the state is
// thin. Strategy "v1" always asks the model for a full summary.
type SummaryCompactor struct {
	Model Model
	// KeepTokens is roughly how much recent history to keep verbatim
	// (default 20000 estimated tokens).
	KeepTokens int
	// Strategy is "v2" (default when empty) or "v1".
	Strategy string
}

// ErrNotEnoughHistory is returned when fewer than two messages are older
// than the kept tail, so there is nothing worth compacting.
var ErrNotEnoughHistory = errors.New("not enough older messages to compact yet")

// errSummaryTooShort aborts compaction so a bad summary cannot replace the
// history. The conversation is left unchanged.
var errSummaryTooShort = errors.New("Compaction produced an empty or too-short summary — aborting to preserve context")

// minSummaryChars is the shortest summary compaction accepts.
const minSummaryChars = 50

// summaryPrompt is the V1 batch summary prompt: seven sections.
const summaryPrompt = `You are compacting a coding session so it can continue in a fresh context window.
Write a summary another engineer could pick up from with no other information. Use these sections, omit empty ones:

## Environment
Platform, tools, versions and project facts that matter.
## Files Touched
Each file path changed or read, with old -> new for every edit.
## Tool Results
Literal output where it matters: test failures, build errors, command results.
## Key Decisions
Choices made and why, and constraints the user stated.
## Work Completed
What has been finished, with file paths and function names.
## Errors & Resolutions
Errors with their exact messages, and how each was resolved or left open.
## Current State
What was in progress at the cut, what is known to work or fail, the user's current request, and the concrete next steps.

Be specific: file paths, function names, commands, error text. Do not invent anything.`

// gapFillPrompt asks only for what the structured state does not hold.
const gapFillPrompt = `You are filling gaps in a structured summary of a coding session. The structured state is given. Answer only with information from the conversation that is missing from it, under these headers, omitting empty ones: "Missing Requirements", "Missing Decisions", "Missing Context". If nothing is missing, answer with the single word COMPLETE. Keep it under 300 words.`

// Caps for the summary request.
const (
	v1MessageChars    = 6000
	v1ResultChars     = 8000
	v1MaxOutput       = 8192
	gapTotalChars     = 12000
	gapMessageChars   = 2000
	gapMaxOutput      = 2048
	toolArgsChars     = 600
	defaultKeepTokens = 20000
)

// EstimateTokens is the rough size of msgs (4 chars per token).
func EstimateTokens(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Result) + len(p.Data)/3
		}
	}
	return n / 4
}

// SplitPoint returns the index of the first message to keep: the earliest
// plain user prompt such that the kept tail is at most keep tokens, never
// splitting a tool call from its result.
//
// The spec keeps the last four messages instead of a token budget. This keeps
// a token budget on purpose: it keeps more of the in-progress work. The tail
// moves forward to a user message rather than back, so it stays within the
// budget and still starts a clean exchange.
func SplitPoint(msgs []llm.Message, keep int) int {
	tokens := 0
	split := len(msgs)
	for i := len(msgs) - 1; i > 0; i-- {
		tokens += EstimateTokens(msgs[i : i+1])
		if tokens > keep {
			break
		}
		split = i
	}
	// Move forward to a user message (not a tool-result message) so the
	// kept tail starts a clean exchange.
	for split < len(msgs) && msgs[split].Role != llm.RoleUser {
		split++
	}
	if split >= len(msgs) {
		// Nothing clean to cut at: keep only the last user exchange.
		for i := len(msgs) - 1; i > 0; i-- {
			if msgs[i].Role == llm.RoleUser {
				return i
			}
		}
		return 0
	}
	return split
}

// sanitizeTail makes the kept tail a valid conversation: no leading tool
// results, no leading assistant message (the acknowledgement is one), and no
// tool result that does not follow an assistant message or another result.
// If nothing survives, the last user message is kept.
func sanitizeTail(tail []llm.Message) []llm.Message {
	var out []llm.Message
	for _, m := range tail {
		switch m.Role {
		case llm.RoleTool:
			if len(out) == 0 {
				continue
			}
			if prev := out[len(out)-1].Role; prev != llm.RoleAssistant && prev != llm.RoleTool {
				continue
			}
		case llm.RoleAssistant:
			if len(out) == 0 {
				continue
			}
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		for i := len(tail) - 1; i >= 0; i-- {
			if tail[i].Role == llm.RoleUser {
				return tail[i : i+1]
			}
		}
		return nil
	}
	return out
}

// Compact implements Compactor.
func (c SummaryCompactor) Compact(ctx context.Context, msgs []llm.Message) ([]llm.Message, string, error) {
	keep := c.KeepTokens
	if keep == 0 {
		keep = defaultKeepTokens
	}
	split := SplitPoint(msgs, keep)
	if split <= 1 {
		return nil, "", ErrNotEnoughHistory
	}
	old, tail := msgs[:split], sanitizeTail(msgs[split:])
	ledger := Ledger(old)
	summary, err := c.summarise(ctx, old)
	if err != nil {
		return nil, "", err
	}
	summary = strings.TrimSpace(summary)
	if len(summary) < minSummaryChars {
		return nil, "", errSummaryTooShort
	}
	if ledger != "" {
		summary += "\n\n" + ledger
	}
	head := []llm.Message{
		llm.UserText("<conversation_summary>\nEarlier parts of this session were compacted. Summary:\n\n" + summary + "\n</conversation_summary>"),
		llm.AssistantText("Continuing."),
	}
	return append(head, tail...), summary, nil
}

// summarise produces the summary body for the older messages.
func (c SummaryCompactor) summarise(ctx context.Context, old []llm.Message) (string, error) {
	if c.Strategy == "v1" {
		return c.batchSummary(ctx, old)
	}
	ws := BuildWorkingState(old)
	if ws.Slots() == 0 {
		return c.batchSummary(ctx, old)
	}
	state := ws.Serialize()
	if ws.Slots() >= richStateSlots || c.Model.Provider == nil {
		return state, nil
	}
	extra, err := c.gapFill(ctx, old, state)
	if err != nil || !gapHasContent(extra) {
		return state, nil // a failed gap fill falls back to the state alone
	}
	return state + "\n\n## Additional Details\n" + strings.TrimSpace(extra), nil
}

// batchSummary is the V1 path: one model call over the transcript.
func (c SummaryCompactor) batchSummary(ctx context.Context, old []llm.Message) (string, error) {
	req := llm.Request{
		Model:     c.Model.ID,
		System:    summaryPrompt,
		Messages:  []llm.Message{llm.UserText("Session transcript to summarise:\n\n" + Transcript(old, v1ResultChars))},
		MaxTokens: v1MaxOutput,
	}
	return Collect(ctx, c.Model.Provider, req)
}

// gapFill asks for the information the structured state lacks. It is a
// separate, deterministic call with a smaller transcript.
func (c SummaryCompactor) gapFill(ctx context.Context, old []llm.Message, state string) (string, error) {
	var b strings.Builder
	for _, m := range old {
		line := renderMessage(m, gapMessageChars, gapMessageChars)
		if b.Len()+len(line) > gapTotalChars {
			break
		}
		b.WriteString(line)
	}
	zero := 0.0
	req := llm.Request{
		Model:       c.Model.ID,
		System:      gapFillPrompt,
		Messages:    []llm.Message{llm.UserText("Structured state so far:\n\n" + state + "\n\nConversation (oldest first):\n\n" + b.String())},
		MaxTokens:   gapMaxOutput,
		Temperature: &zero,
	}
	return Collect(ctx, c.Model.Provider, req)
}

// gapHasContent reports whether a gap-fill answer adds anything. A bare
// COMPLETE, or an answer under 20 characters, adds nothing.
func gapHasContent(s string) bool {
	s = strings.TrimSpace(s)
	return s != "COMPLETE" && len(s) >= 20
}

// Collect runs a request without tools and returns the text.
func Collect(ctx context.Context, p llm.Provider, req llm.Request) (string, error) {
	ch, err := p.Stream(ctx, req)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	var serr error
	for ev := range ch {
		switch ev.Type {
		case llm.EvText:
			b.WriteString(ev.Text)
		case llm.EvError:
			serr = ev.Err
		}
	}
	if serr != nil {
		return "", serr
	}
	return b.String(), nil
}

// Transcript renders msgs as plain text. Each text part is capped at 6000
// characters and each tool result at max.
func Transcript(msgs []llm.Message, max int) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(renderMessage(m, v1MessageChars, max))
	}
	return b.String()
}

// renderMessage renders one message. textMax caps text parts and resultMax
// caps tool results; tool call arguments are capped at 600 characters.
func renderMessage(m llm.Message, textMax, resultMax int) string {
	var b strings.Builder
	for _, p := range m.Parts {
		switch p.Type {
		case llm.PartText:
			fmt.Fprintf(&b, "[%s] %s\n\n", m.Role, clip(p.Text, textMax))
		case llm.PartToolCall:
			fmt.Fprintf(&b, "[tool call] %s %s\n", p.ToolName, clip(string(p.Args), toolArgsChars))
		case llm.PartToolResult:
			tag := "result"
			if p.IsError {
				tag = "error"
			}
			fmt.Fprintf(&b, "[tool %s] %s\n\n", tag, clip(p.Result, resultMax))
		}
	}
	return b.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("… (%d more chars)", len(s)-n)
}

// Ledger lists files read and edited, commands run and failed calls.
func Ledger(msgs []llm.Message) string {
	read, edited, cmds := map[string]bool{}, map[string]bool{}, []string{}
	failures := []string{}
	// A call whose result is an error did not read or edit anything.
	failed := map[string]bool{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == llm.PartToolResult && p.IsError {
				failed[p.ToolCallID] = true
			}
		}
	}
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch p.Type {
			case llm.PartToolCall:
				var a struct {
					Path    string   `json:"path"`
					Paths   []string `json:"paths"`
					Command string   `json:"command"`
				}
				_ = json.Unmarshal(p.Args, &a)
				ok := !failed[p.ToolCallID]
				switch p.ToolName {
				case "read":
					if !ok {
						break
					}
					if a.Path != "" {
						read[a.Path] = true
					}
					for _, x := range a.Paths {
						read[x] = true
					}
				case "edit_file", "multi_edit", "write_file", "ast_edit", "rename_symbol":
					if ok && a.Path != "" {
						edited[a.Path] = true
					}
				case "shell":
					if a.Command != "" && len(cmds) < 25 {
						cmds = append(cmds, clip(a.Command, 160))
					}
				}
			case llm.PartToolResult:
				if p.IsError && len(failures) < 10 {
					failures = append(failures, fmt.Sprintf("%s: %s", p.ToolName, clip(firstLine(p.Result), 200)))
				}
			}
		}
	}
	var b strings.Builder
	list := func(title string, m map[string]bool) {
		if len(m) == 0 {
			return
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "%s: %s\n", title, strings.Join(keys, ", "))
	}
	list("Files edited", edited)
	list("Files read", read)
	if len(cmds) > 0 {
		fmt.Fprintf(&b, "Commands run:\n- %s\n", strings.Join(cmds, "\n- "))
	}
	if len(failures) > 0 {
		fmt.Fprintf(&b, "Failed calls:\n- %s\n", strings.Join(failures, "\n- "))
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Ledger\n" + b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
