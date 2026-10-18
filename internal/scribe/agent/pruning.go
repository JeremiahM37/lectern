package agent

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// Tool-result pruning (spec 03 section 8.7). Old tool results in the live
// history are shortened so a long turn does not keep every file it read in
// full. The spec fixes the on/off target (contextManagement.pruningTarget,
// default none) but not the cut-off or the exempt tools, so those are set
// here: the newest pruneKeepResults results stay whole, and only results
// longer than pruneMinChars are touched.
const (
	pruneKeepResults = 8
	pruneMinChars    = 2000
	pruneHeadChars   = 400
	pruneSymbolLines = 20
	pruneSymbolChars = 800
	pruneLineChars   = 160
)

// pruneExempt are tools whose results carry state the model must keep in
// full: the plan, the task list, the bus, and the user's answers. Their
// results are short, and cutting them would lose the thread of the work.
var pruneExempt = map[string]bool{
	"plan":               true,
	"update_plan_step":   true,
	"task_list":          true,
	"ask_user":           true,
	"report_finding":     true,
	"check_findings":     true,
	"check_peers":        true,
	"check_agent_result": true,
	"cancel_dispatch":    true,
}

// declarationLine matches a line that declares a code symbol in most
// languages. Pruning keeps these lines from the cut part of a result, so a
// read of a large file still shows its shape.
var declarationLine = regexp.MustCompile(`^\s*(func|type|class|def|interface|struct|const|var|export)\b`)

// PruningApplies reports whether the pruning target covers this agent. The
// target is config.ContextManagement.Pruning(): main covers the main agent,
// subagents covers dispatched agents, and both covers each.
func PruningApplies(target string, subagent bool) bool {
	switch target {
	case config.PruningMain:
		return !subagent
	case config.PruningSubagents:
		return subagent
	case config.PruningBoth:
		return true
	default:
		return false
	}
}

// PruneOldToolResults returns msgs with old tool results shortened. It only
// rewrites the Result text of tool-result parts, so user and assistant
// messages and the system prompt (which is not in msgs) are unchanged. The
// input is not modified: a changed message is copied, so the caller's history
// keeps its own slices. When nothing is pruned the input slice is returned
// as it is.
//
// A result is pruned only when it is not an error, its tool is not exempt,
// it is longer than pruneMinChars, and it is older than the newest
// pruneKeepResults tool results. Pruning is idempotent: a pruned result is
// shorter than pruneMinChars, so a second pass leaves it alone.
func PruneOldToolResults(msgs []llm.Message) []llm.Message {
	names := map[string]string{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == llm.PartToolCall {
				names[p.ToolCallID] = p.ToolName
			}
		}
	}
	// Walk from the newest result back, so the first pruneKeepResults
	// results seen are the ones that stay whole.
	type target struct{ msg, part int }
	var cut []target
	seen := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		for j := len(msgs[i].Parts) - 1; j >= 0; j-- {
			p := msgs[i].Parts[j]
			if p.Type != llm.PartToolResult {
				continue
			}
			seen++
			if seen <= pruneKeepResults || p.IsError || len(p.Result) <= pruneMinChars {
				continue
			}
			tool := p.ToolName
			if tool == "" {
				tool = names[p.ToolCallID]
			}
			if pruneExempt[tool] {
				continue
			}
			cut = append(cut, target{i, j})
		}
	}
	if len(cut) == 0 {
		return msgs
	}

	out := append([]llm.Message(nil), msgs...)
	copied := map[int]bool{}
	for _, t := range cut {
		if !copied[t.msg] {
			out[t.msg].Parts = append([]llm.Part(nil), msgs[t.msg].Parts...)
			copied[t.msg] = true
		}
		p := &out[t.msg].Parts[t.part]
		p.Result = pruneResult(p.Result)
	}
	return out
}

// pruneResult shortens one tool result to its head, then adds the code
// declaration lines from the rest, within a fixed budget. The marker says
// how much was removed, so the model knows it can read the file again.
func pruneResult(result string) string {
	head := result[:cutAtRune(result, pruneHeadChars)]
	rest := result[len(head):]

	var decls []string
	chars := 0
	for _, line := range strings.Split(rest, "\n") {
		if len(decls) >= pruneSymbolLines || chars >= pruneSymbolChars {
			break
		}
		if !declarationLine.MatchString(line) {
			continue
		}
		line = strings.TrimRight(clip(line, pruneLineChars), " \t\r")
		decls = append(decls, line)
		chars += len(line)
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(head, "\n"))
	fmt.Fprintf(&b, "\n… [pruned: %d of %d chars removed; read the file again for the rest]", len(rest), len(result))
	if len(decls) > 0 {
		b.WriteString("\nDeclarations from the removed part:\n")
		b.WriteString(strings.Join(decls, "\n"))
	}
	return b.String()
}

// cutAtRune returns the largest index at most n that does not split a UTF-8
// sequence, so the head stays valid text.
func cutAtRune(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}
