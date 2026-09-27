package api

// AI-vs-human line attribution for the review workspace (docs/review.md).
//
// Two sources, both things Lectern already sees:
//
//   - The agent's own edit hooks. A PostToolUse for Edit/MultiEdit/Write (and
//     Codex's apply_patch) says which lines the agent wrote into which file;
//     recordAgentLines keeps a hash of each one per session and path.
//   - Git history. A committed line is agent-written when the commit that last
//     touched it carries an agent co-author trailer or an agent author.
//
// A line is matched by content, so a human edit that changes an agent's line
// makes it the human's: its new text was never reported by the agent.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// maxMarkedLinesPerEdit bounds what one hook event can record, so a huge
// generated file cannot flood the table.
const maxMarkedLinesPerEdit = 5000

// lineHash identifies a line by its content, ignoring trailing whitespace. A
// blank line has no identity ("") and is never attributed.
func lineHash(line string) string {
	trimmed := strings.TrimRight(line, " \t\r")
	if strings.TrimSpace(trimmed) == "" {
		return ""
	}
	sum := sha1.Sum([]byte(trimmed))
	return hex.EncodeToString(sum[:8])
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// linesMinus is added minus removed, as multisets: the lines an edit
// introduced rather than merely kept as context.
func linesMinus(added, removed []string) []string {
	have := map[string]int{}
	for _, l := range removed {
		have[strings.TrimRight(l, " \t\r")]++
	}
	var out []string
	for _, l := range added {
		k := strings.TrimRight(l, " \t\r")
		if have[k] > 0 {
			have[k]--
			continue
		}
		out = append(out, l)
	}
	return out
}

// structuredPatchLines reads Claude Code's tool_response.structuredPatch —
// [{lines: ["+x", "-y", " z"]}, ...] — and returns the added lines, and
// whether a patch was present at all.
func structuredPatchLines(response map[string]any) ([]string, bool) {
	hunks, ok := response["structuredPatch"].([]any)
	if !ok || len(hunks) == 0 {
		return nil, false
	}
	var out []string
	for _, h := range hunks {
		hm, _ := h.(map[string]any)
		lines, _ := hm["lines"].([]any)
		for _, l := range lines {
			if s, ok := l.(string); ok && strings.HasPrefix(s, "+") {
				out = append(out, s[1:])
			}
		}
	}
	return out, true
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// agentWrittenLines extracts, from one PostToolUse hook, the lines the agent
// wrote per absolute file path. cwd resolves relative paths (Codex patches).
func agentWrittenLines(toolName string, input, response map[string]any, cwd string) map[string][]string {
	out := map[string][]string{}
	abs := func(p string) string {
		if p == "" {
			return ""
		}
		if !path.IsAbs(p) {
			if cwd == "" {
				return ""
			}
			p = path.Join(cwd, p)
		}
		return path.Clean(p)
	}
	switch toolName {
	case "Edit", "MultiEdit", "Write":
		file := abs(stringField(input, "file_path"))
		if file == "" {
			return out
		}
		if lines, ok := structuredPatchLines(response); ok {
			out[file] = lines
			return out
		}
		switch toolName {
		case "Edit":
			out[file] = linesMinus(splitLines(stringField(input, "new_string")), splitLines(stringField(input, "old_string")))
		case "MultiEdit":
			edits, _ := input["edits"].([]any)
			for _, e := range edits {
				em, _ := e.(map[string]any)
				out[file] = append(out[file], linesMinus(splitLines(stringField(em, "new_string")),
					splitLines(stringField(em, "old_string")))...)
			}
		case "Write":
			out[file] = splitLines(stringField(input, "content"))
		}
		return out
	}
	// Codex (and anything else speaking its patch format): find the patch
	// text wherever the tool put it.
	if patch := findApplyPatch(input); patch != "" {
		for file, lines := range applyPatchLines(patch) {
			if p := abs(file); p != "" {
				out[p] = append(out[p], lines...)
			}
		}
	}
	return out
}

func findApplyPatch(v any) string {
	switch t := v.(type) {
	case string:
		if strings.Contains(t, "*** Begin Patch") {
			return t
		}
	case []any:
		for _, x := range t {
			if p := findApplyPatch(x); p != "" {
				return p
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if p := findApplyPatch(t[k]); p != "" {
				return p
			}
		}
	}
	return ""
}

// applyPatchLines parses Codex's apply_patch envelope ("*** Add File: p",
// "*** Update File: p", "+line") into added lines per file.
func applyPatchLines(patch string) map[string][]string {
	out := map[string][]string{}
	current := ""
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			current = strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
		case strings.HasPrefix(line, "*** Update File: "):
			current = strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
		case strings.HasPrefix(line, "*** Delete File: "), strings.HasPrefix(line, "*** End Patch"):
			current = ""
		case strings.HasPrefix(line, "*** "):
		case current != "" && strings.HasPrefix(line, "+"):
			out[current] = append(out[current], line[1:])
		}
	}
	return out
}

// attributionHookInput is the part of a PostToolUse body attribution reads.
type attributionHookInput struct {
	ToolName     string         `json:"tool_name"`
	ToolInput    map[string]any `json:"tool_input"`
	ToolResponse any            `json:"tool_response"`
	Cwd          string         `json:"cwd"`
}

// recordAgentLines stores the lines one PostToolUse hook reports the agent
// wrote. Best effort: attribution is advisory and must never fail a hook.
func (s *Server) recordAgentLines(sess *store.Session, event string, body []byte) {
	if event != agentevents.EventPostToolUse || len(body) == 0 {
		return
	}
	var in attributionHookInput
	if json.Unmarshal(body, &in) != nil || in.ToolInput == nil {
		return
	}
	response, _ := in.ToolResponse.(map[string]any)
	for file, lines := range agentWrittenLines(in.ToolName, in.ToolInput, response, firstNonEmptyStr(in.Cwd, sess.Workdir)) {
		seen := map[string]bool{}
		var hashes []string
		for _, l := range lines {
			if h := lineHash(l); h != "" && !seen[h] {
				seen[h] = true
				hashes = append(hashes, h)
			}
			if len(hashes) >= maxMarkedLinesPerEdit {
				break
			}
		}
		if err := s.DB.AddAgentLineMarks(sess.ID, file, hashes); err != nil {
			s.Log.Warn("attribution: could not record agent lines", "session", sess.ID, "err", err)
		}
	}
}

// addedLine is one "+" line of a diff with its new-side line number.
type addedLine struct {
	n    int
	text string
}

func addedLines(patch string) []addedLine {
	var out []addedLine
	n := 0
	inHunk := false
	for _, raw := range strings.Split(patch, "\n") {
		if strings.HasPrefix(raw, "@@") {
			var a, b, c int
			if _, err := fmt.Sscanf(raw, "@@ -%d,%d +%d", &a, &b, &c); err != nil {
				if _, err := fmt.Sscanf(raw, "@@ -%d +%d", &a, &c); err != nil {
					inHunk = false
					continue
				}
			}
			n, inHunk = c, true
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(raw, "+"):
			out = append(out, addedLine{n, raw[1:]})
			n++
		case strings.HasPrefix(raw, "-"), strings.HasPrefix(raw, "\\"):
		default:
			n++
		}
	}
	return out
}

// lineRanges compresses sorted line numbers into [a, b] runs.
func lineRanges(nums []int) [][2]int {
	sort.Ints(nums)
	var out [][2]int
	for _, n := range nums {
		if len(out) > 0 && out[len(out)-1][1]+1 == n {
			out[len(out)-1][1] = n
			continue
		}
		out = append(out, [2]int{n, n})
	}
	return out
}

// classifyLine is the attribution rule for one added line. blameSHA is the
// commit that last touched it ("" or all zeroes when uncommitted). An
// uncommitted line is only called human when the session's agent is known to
// report its edits; otherwise nothing can be said.
func classifyLine(hash, blameSHA string, marks, agentCommits map[string]bool, hookEvidence bool) string {
	if hash == "" {
		return ""
	}
	if marks[hash] {
		return "agent"
	}
	committed := blameSHA != "" && strings.Trim(blameSHA, "0") != ""
	if committed {
		if agentCommits[blameSHA] {
			return "agent"
		}
		return "human"
	}
	if hookEvidence {
		return "human"
	}
	return ""
}

type fileAttribution struct {
	Agent [][2]int `json:"agent"`
	Human [][2]int `json:"human"`
}

type repoAttribution struct {
	Name  string                     `json:"name,omitempty"`
	Files map[string]fileAttribution `json:"files"`
}

type blameOut struct {
	AgentCommits []string                     `json:"agent_commits"`
	Files        map[string]map[string]string `json:"files"`
}

// attributeRepo classifies every added line of one repository's live diff.
func (s *Server) attributeRepo(ctx context.Context, ex executor.Executor, sessionID int64, d repoDiff, hookEvidence bool) (repoAttribution, error) {
	out := repoAttribution{Name: d.Name, Files: map[string]fileAttribution{}}
	perFile := map[string][]addedLine{}
	var absPaths []string
	ranges := map[string][][2]int{}
	for i, f := range d.Files {
		if i >= 300 {
			break
		}
		lines := addedLines(f.Patch)
		if len(lines) == 0 {
			continue
		}
		perFile[f.Path] = lines
		absPaths = append(absPaths, path.Join(d.Dir, f.Path))
		nums := make([]int, len(lines))
		for j, l := range lines {
			nums[j] = l.n
		}
		ranges[f.Path] = lineRanges(nums)
	}
	if len(perFile) == 0 {
		return out, nil
	}
	marks, err := s.DB.AgentLineMarks(sessionID, absPaths)
	if err != nil {
		return out, err
	}
	var blame blameOut
	if err := runReviewGit(ctx, ex, d.Dir, "blame", map[string]any{"base": d.BaseRef, "files": ranges}, &blame); err != nil {
		// No history to consult (an unborn branch, say): attribute from the
		// hook record alone.
		blame = blameOut{}
	}
	agentCommits := map[string]bool{}
	for _, sha := range blame.AgentCommits {
		agentCommits[sha] = true
	}
	for file, lines := range perFile {
		fileMarks := marks[path.Join(d.Dir, file)]
		var agent, human []int
		for _, l := range lines {
			sha := ""
			if blame.Files != nil {
				sha = blame.Files[file][fmt.Sprint(l.n)]
			}
			switch classifyLine(lineHash(l.text), sha, fileMarks, agentCommits, hookEvidence) {
			case "agent":
				agent = append(agent, l.n)
			case "human":
				human = append(human, l.n)
			}
		}
		if len(agent)+len(human) > 0 {
			out.Files[file] = fileAttribution{Agent: lineRanges(agent), Human: lineRanges(human)}
		}
	}
	return out, nil
}

// sessionAttribution is GET /api/sessions/{id}/attribution: which added lines
// of the live diff the agent wrote and which a person did.
func (s *Server) sessionAttribution(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	ex, err := s.sessionExecutor(row)
	if err != nil {
		respondErr(w, err)
		return
	}
	repos, err := s.sessionRepoDiffs(r.Context(), ex, row)
	if err != nil {
		respondDiffErr(w, err)
		return
	}
	marked, err := s.DB.CountAgentLineMarks(row.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	out := []repoAttribution{}
	for _, d := range repos {
		a, err := s.attributeRepo(r.Context(), ex, row.ID, d, marked > 0)
		if err != nil {
			respondErr(w, err)
			return
		}
		out = append(out, a)
	}
	writeJSON(w, 200, map[string]any{"repos": out, "hook_evidence": marked > 0})
}
