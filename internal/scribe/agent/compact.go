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

// SummaryCompactor replaces older history with a model-written summary
// plus a mechanical ledger of what was touched (files read and edited,
// commands run, failures), keeping the recent tail verbatim.
type SummaryCompactor struct {
	Model Model
	// KeepTokens is roughly how much recent history to keep verbatim
	// (default 20000 estimated tokens).
	KeepTokens int
}

const summaryPrompt = `You are compacting a coding session so it can continue in a fresh context window.
Write a summary another engineer could pick up from with no other information. Use these sections, omit empty ones:

## Goal
What the user asked for, in their words where it matters. Include later changes of direction.
## Decisions
Choices made and why, constraints the user stated.
## Done
What has been completed, with file paths.
## Current state
What was in progress at the cut, what is known to work or fail (tests, builds, errors with exact messages).
## Next
The concrete next steps.

Be specific: file paths, function names, commands, error text. Do not invent anything. Keep it under 900 words.`

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

// Compact implements Compactor.
func (c SummaryCompactor) Compact(ctx context.Context, msgs []llm.Message) ([]llm.Message, string, error) {
	keep := c.KeepTokens
	if keep == 0 {
		keep = 20000
	}
	split := SplitPoint(msgs, keep)
	if split <= 1 {
		return nil, "", errors.New("nothing to compact yet")
	}
	old, tail := msgs[:split], msgs[split:]
	ledger := Ledger(old)
	req := llm.Request{
		Model:     c.Model.ID,
		System:    summaryPrompt,
		Messages:  []llm.Message{llm.UserText("Session transcript to summarise:\n\n" + Transcript(old, 2000))},
		MaxTokens: 4096,
	}
	summary, err := Collect(ctx, c.Model.Provider, req)
	if err != nil {
		return nil, "", err
	}
	summary = strings.TrimSpace(summary)
	if ledger != "" {
		summary += "\n\n" + ledger
	}
	head := []llm.Message{
		llm.UserText("<conversation_summary>\nEarlier parts of this session were compacted. Summary:\n\n" + summary + "\n</conversation_summary>"),
		llm.AssistantText("Understood. I have the summary of the earlier work and will continue from it."),
	}
	return append(head, tail...), summary, nil
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

// Transcript renders msgs as plain text, truncating each tool result to
// max characters.
func Transcript(msgs []llm.Message, max int) string {
	var b strings.Builder
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch p.Type {
			case llm.PartText:
				fmt.Fprintf(&b, "[%s] %s\n\n", m.Role, p.Text)
			case llm.PartToolCall:
				fmt.Fprintf(&b, "[tool call] %s %s\n", p.ToolName, clip(string(p.Args), 600))
			case llm.PartToolResult:
				tag := "result"
				if p.IsError {
					tag = "error"
				}
				fmt.Fprintf(&b, "[tool %s] %s\n\n", tag, clip(p.Result, max))
			}
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
