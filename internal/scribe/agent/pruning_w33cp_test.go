package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// bigResult is a tool result long enough to be pruned, with a declaration
// line far enough down that only the cut part holds it.
func bigResult(tag string) string {
	return "file " + tag + " header\n" + strings.Repeat("filler line of prose here\n", 120) +
		"func Важная(x int) int {\n\treturn x\n}\n" + strings.Repeat("more filler text\n", 100)
}

// toolTurn is one call and its result, as the history holds them.
func toolTurn(id, tool, result string, isErr bool) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: llm.PartToolCall, ToolCallID: id, ToolName: tool, Args: json.RawMessage(`{}`)}}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: id, ToolName: tool, Result: result, IsError: isErr}}},
	}
}

// history is n big read results after one user prompt.
func history(n int) []llm.Message {
	msgs := []llm.Message{llm.UserText("read the files please")}
	for i := 0; i < n; i++ {
		msgs = append(msgs, toolTurn("c"+string(rune('a'+i)), "read", bigResult(string(rune('a'+i))), false)...)
	}
	return msgs
}

func resultAt(msgs []llm.Message, msgIdx int) string {
	return msgs[msgIdx].Parts[0].Result
}

// The newest pruneKeepResults results stay whole; older long ones are pruned.
func TestPruneLeavesTheNewestResultsWhole(t *testing.T) {
	n := pruneKeepResults + 3
	msgs := history(n)
	out := PruneOldToolResults(msgs)
	for i := 0; i < n; i++ {
		idx := 2 + 2*i // each turn is a call then its result, after the prompt
		got := resultAt(out, idx)
		fresh := i >= n-pruneKeepResults
		if fresh && got != bigResult(string(rune('a'+i))) {
			t.Errorf("result %d is recent and must stay whole", i)
		}
		if !fresh && !strings.Contains(got, "[pruned:") {
			t.Errorf("result %d is old and should be pruned:\n%s", i, got)
		}
	}
}

// A pruned result keeps its head, marks how much it removed, and keeps the
// code declaration from the removed part.
func TestPruneKeepsHeadMarkerAndDeclarations(t *testing.T) {
	out := PruneOldToolResults(history(pruneKeepResults + 1))
	got := resultAt(out, 2)
	if !strings.HasPrefix(got, "file a header") {
		t.Errorf("head not kept:\n%s", got)
	}
	if !strings.Contains(got, "[pruned: ") || !strings.Contains(got, "read the file again") {
		t.Errorf("marker missing:\n%s", got)
	}
	if !strings.Contains(got, "func Важная(x int) int {") {
		t.Errorf("declaration from the removed part not kept:\n%s", got)
	}
	if len(got) >= len(bigResult("a")) {
		t.Errorf("pruned result is not shorter: %d chars", len(got))
	}
}

// Pruning twice changes nothing more: the second pass finds no result over
// the threshold.
func TestPruneIsIdempotent(t *testing.T) {
	once := PruneOldToolResults(history(pruneKeepResults + 4))
	twice := PruneOldToolResults(once)
	for i := range once {
		for j := range once[i].Parts {
			if once[i].Parts[j].Result != twice[i].Parts[j].Result {
				t.Fatalf("message %d part %d changed on the second pass", i, j)
			}
		}
	}
}

// Results that are not old enough, or short, or errors, are left alone, and
// so are exempt tools however long their results are.
func TestPruneExemptions(t *testing.T) {
	msgs := []llm.Message{llm.UserText("go")}
	msgs = append(msgs, toolTurn("e1", "read", bigResult("err"), true)...)   // error
	msgs = append(msgs, toolTurn("p1", "plan", bigResult("plan"), false)...) // exempt tool
	msgs = append(msgs, toolTurn("s1", "read", "short result", false)...)    // under threshold
	for i := 0; i < pruneKeepResults; i++ {                                  // push the above out of the newest set
		msgs = append(msgs, toolTurn("r"+string(rune('a'+i)), "read", bigResult("new"), false)...)
	}
	out := PruneOldToolResults(msgs)
	if out[2].Parts[0].Result != bigResult("err") {
		t.Errorf("an error result was pruned")
	}
	if out[4].Parts[0].Result != bigResult("plan") {
		t.Errorf("an exempt tool's result was pruned")
	}
	if out[6].Parts[0].Result != "short result" {
		t.Errorf("a short result was pruned")
	}
}

// The tool name is recovered from the call when the result carries none.
func TestPruneUsesCallNameWhenResultHasNone(t *testing.T) {
	msgs := []llm.Message{llm.UserText("go")}
	msgs = append(msgs, toolTurn("x1", "plan", bigResult("plan"), false)...)
	msgs[2].Parts[0].ToolName = "" // the result carries no name
	for i := 0; i < pruneKeepResults; i++ {
		msgs = append(msgs, toolTurn("r"+string(rune('a'+i)), "read", bigResult("new"), false)...)
	}
	out := PruneOldToolResults(msgs)
	if out[2].Parts[0].Result != bigResult("plan") {
		t.Errorf("exempt plan result pruned when its name came from the call")
	}
}

// Only tool-result text changes. Messages without tool results, the parts of
// the other messages and the roles are the same, and the input is not
// modified, so the caller's history keeps its own text.
func TestPruneRewritesOnlyToolResults(t *testing.T) {
	msgs := history(pruneKeepResults + 2)
	before := make([]string, len(msgs))
	for i, m := range msgs {
		for _, p := range m.Parts {
			before[i] += p.Result + "|" + p.Text + "|" + p.ToolName
		}
	}
	out := PruneOldToolResults(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("message count changed: %d -> %d", len(msgs), len(out))
	}
	for i := range msgs {
		if out[i].Role != msgs[i].Role || len(out[i].Parts) != len(msgs[i].Parts) {
			t.Fatalf("message %d role or part count changed", i)
		}
		for j := range msgs[i].Parts {
			a, b := msgs[i].Parts[j], out[i].Parts[j]
			if a.Type != b.Type || a.ToolName != b.ToolName || a.ToolCallID != b.ToolCallID || a.Text != b.Text || string(a.Args) != string(b.Args) {
				t.Fatalf("message %d part %d changed beyond its result", i, j)
			}
		}
	}
	for i, m := range msgs {
		var got string
		for _, p := range m.Parts {
			got += p.Result + "|" + p.Text + "|" + p.ToolName
		}
		if got != before[i] {
			t.Fatalf("input message %d was modified", i)
		}
	}
	if msgs[0].Parts[0].Text != "read the files please" {
		t.Fatalf("the user prompt changed")
	}
}

// When nothing is over the threshold, the very same slice comes back.
func TestPruneReturnsInputWhenNothingToDo(t *testing.T) {
	msgs := history(2)
	out := PruneOldToolResults(msgs)
	if &out[0] != &msgs[0] {
		t.Fatalf("nothing to prune should return the input slice")
	}
}

// The target decides who is pruned: main, subagents, both, or none.
func TestPruningAppliesByTarget(t *testing.T) {
	cases := []struct {
		target   string
		subagent bool
		want     bool
	}{
		{"", false, false},
		{config.PruningNone, false, false},
		{config.PruningMain, false, true},
		{config.PruningMain, true, false},
		{config.PruningSubagents, false, false},
		{config.PruningSubagents, true, true},
		{config.PruningBoth, false, true},
		{config.PruningBoth, true, true},
		{"bogus", false, false},
	}
	for _, c := range cases {
		if got := PruningApplies(c.target, c.subagent); got != c.want {
			t.Errorf("PruningApplies(%q, subagent=%v) = %v, want %v", c.target, c.subagent, got, c.want)
		}
	}
}

// A multi-byte character at the cut is not split, so the head stays valid
// UTF-8.
func TestPruneDoesNotSplitARune(t *testing.T) {
	s := strings.Repeat("é", 300) // 2 bytes each
	n := cutAtRune(s, 401)
	if n%2 != 0 || n > 401 {
		t.Fatalf("cut %d splits a rune or passes the limit", n)
	}
}
