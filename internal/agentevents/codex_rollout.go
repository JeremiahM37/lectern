package agentevents

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// codexThreadIDRe matches a codex thread/session id (a UUID, observed as
// e.g. "01a0925a-4173-7b43-addd-68891f230769"). Ingest validates against
// this before ever writing sessions.codex_thread_id: the value arrives in an
// hook body the target process controls, and it is later interpolated next
// to unquoted shell glob characters in CodexRolloutTailCommand — rejecting
// anything that is not plausibly a codex id here means that command only
// ever has to defend against a merely-inert value, not an unvalidated one.
var codexThreadIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{1,64}$`)

// ValidCodexThreadID reports whether s is safe to store and later use to
// locate a rollout file.
func ValidCodexThreadID(s string) bool { return codexThreadIDRe.MatchString(s) }

// CodexUsage is what the codex rollout reader extracts from a session's
// rollout JSONL — the closest codex has to Claude's statusline. Cost has no
// equivalent field here: docs/agent-events.md calls for showing tokens
// instead of a dollar figure for codex, so there is deliberately no CostUSD.
type CodexUsage struct {
	Model         string
	ContextTokens int // cumulative tokens counted against the context window
	ContextSize   int // model_context_window
	ContextPct    int // ContextTokens/ContextSize, capped at 100
	InputTokens   int // cumulative, for usage_daily delta booking
	OutputTokens  int // cumulative, for usage_daily delta booking
}

// The types below mirror the real rollout JSONL shape captured from a codex
// 0.155.1 session on this machine (see the workstream report for the
// sanitised excerpt): an outer envelope with a "type" tag, and — for the
// record this reader cares about — a nested event_msg payload whose own
// "type" is "token_count", carrying a running token_count.info.total_token_usage
// against info.model_context_window. Only the fields used are declared;
// everything else in the real file (session_meta, world_state, compacted,
// response_item, ...) is ignored rather than rejected, since a newer codex is
// free to add record types.
type codexRolloutLine struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type codexEventMsg struct {
	Type string               `json:"type"`
	Info *codexTokenCountInfo `json:"info"`
}

type codexTokenCountInfo struct {
	TotalTokenUsage    codexTokenUsage `json:"total_token_usage"`
	ModelContextWindow int             `json:"model_context_window"`
}

type codexTokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// turn_context carries the model name (rollout's session_meta does not).
type codexTurnContext struct {
	Model string `json:"model"`
}

// ParseCodexRolloutUsage scans a (possibly partial — a tailed read may start
// mid-line) chunk of a rollout JSONL file for the LAST token_count record,
// codex's running context gauge, and the most recent turn_context's model.
// A malformed or truncated leading line is simply skipped like any other
// unrecognised line rather than failing the whole read — the same tolerance
// IngestStatusline has for a bad statusline body.
func ParseCodexRolloutUsage(data []byte) (*CodexUsage, bool) {
	var info *codexTokenCountInfo
	model := ""
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var outer codexRolloutLine
		if err := json.Unmarshal(line, &outer); err != nil {
			continue
		}
		switch outer.Type {
		case "event_msg":
			var msg codexEventMsg
			if err := json.Unmarshal(outer.Payload, &msg); err != nil {
				continue
			}
			if msg.Type == "token_count" && msg.Info != nil && msg.Info.ModelContextWindow > 0 {
				info = msg.Info
			}
		case "turn_context":
			var tc codexTurnContext
			if err := json.Unmarshal(outer.Payload, &tc); err == nil && tc.Model != "" {
				model = tc.Model
			}
		}
	}
	if info == nil {
		return nil, false
	}
	usage := &CodexUsage{
		Model:         model,
		ContextTokens: info.TotalTokenUsage.TotalTokens,
		ContextSize:   info.ModelContextWindow,
		InputTokens:   info.TotalTokenUsage.InputTokens,
		OutputTokens:  info.TotalTokenUsage.OutputTokens,
	}
	if usage.ContextSize > 0 {
		pct := usage.ContextTokens * 100 / usage.ContextSize
		if pct > 100 {
			pct = 100
		}
		usage.ContextPct = pct
	}
	return usage, true
}

// codexRolloutTailBytes is how much of a rollout file's tail is read: a
// token_count event_msg record is small and codex writes one roughly every
// exchange, so recent history comfortably fits without reading the whole
// (potentially long-running) session file on every poll tick.
const codexRolloutTailBytes = 200_000

// CodexRolloutTailCommand builds one shell command that, for every given
// thread id, locates that session's exact rollout file under
// ~/.codex/sessions/*/*/*/rollout-*-<id>.jsonl (the year/month/day glob
// segments are needed because the reader has no other record of which day a
// session started — only the id, from the AgentTurnComplete notify payload,
// is exact) and prints its tail, framed the same base64/marker way
// poll_wire.go's PollCommand frames tmux panes, so one target round trip
// covers every codex session on it.
func CodexRolloutTailCommand(threadIDs []string) string {
	var cmd strings.Builder
	seen := map[string]bool{}
	for _, id := range threadIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		// The id is validated (see agentevents.ValidCodexThreadID, checked
		// before this ever reaches a session row) but is still rendered
		// through shellq rather than trusted bare: it sits next to unquoted
		// glob characters ("rollout-*-<id>.jsonl"), and shellq.Quote's safe
		// charset overlaps a UUID exactly, so a valid id renders unquoted
		// (an ordinary glob-adjacent word) while anything else is neutralised
		// as an inert literal instead of being interpreted by the shell.
		fmt.Fprintf(&cmd,
			`lec_codex_file=$(ls -t $HOME/.codex/sessions/*/*/*/rollout-*-%s.jsonl 2>/dev/null | head -1); `+
				`if [ -n "$lec_codex_file" ]; then lec_codex_payload=$(tail -c %d "$lec_codex_file" | base64 | tr -d '\r\n'); lec_codex_state=ok; else lec_codex_payload=''; lec_codex_state=missing; fi; `+
				`printf '%%s\t%%s\t%%s\n' %s "$lec_codex_state" "$lec_codex_payload"; `,
			shellq.Quote(id), codexRolloutTailBytes, shellq.Quote(base64.StdEncoding.EncodeToString([]byte(id))))
	}
	fmt.Fprintf(&cmd, "printf '%%s\\n' %s", shellq.Quote(CodexRolloutEnd))
	return cmd.String()
}

// CodexRolloutEnd terminates a CodexRolloutTailCommand snapshot, mirroring
// poll_wire.go's PollEnd.
const CodexRolloutEnd = "ADK-CODEX-ROLLOUT-END-v1"

// ParseCodexRolloutSnapshot decodes CodexRolloutTailCommand's output into a
// per-thread-id tail (raw bytes, possibly nil when no matching file was
// found). Like ParsePollSnapshot, a short, malformed, or incomplete snapshot
// is rejected outright rather than partially trusted.
func ParseCodexRolloutSnapshot(output string, threadIDs []string) (map[string][]byte, bool) {
	wanted := map[string]bool{}
	for _, id := range threadIDs {
		if id != "" {
			wanted[id] = true
		}
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != CodexRolloutEnd || len(lines) != len(wanted)+1 {
		return nil, false
	}
	out := map[string][]byte{}
	for _, line := range lines[:len(lines)-1] {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return nil, false
		}
		idBytes, err := base64.StdEncoding.DecodeString(parts[0])
		if err != nil || !wanted[string(idBytes)] {
			return nil, false
		}
		if _, exists := out[string(idBytes)]; exists {
			return nil, false
		}
		if parts[1] == "missing" {
			out[string(idBytes)] = nil
			continue
		}
		body, err := base64.StdEncoding.DecodeString(parts[2])
		if err != nil {
			return nil, false
		}
		out[string(idBytes)] = body
	}
	return out, true
}
