package awareness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// PeerFile is one file a peer touched recently, with its age already
// computed against `now` so the caller never has to redo that math.
type PeerFile struct {
	RelPath    string  `json:"rel_path"`
	At         float64 `json:"at"`
	AgeSeconds float64 `json:"age_seconds"`
}

// Peer is one other live session, or one other running task attempt,
// working in the same repository as the session a Peers call was made for.
type Peer struct {
	Kind        string     `json:"kind"` // "session" | "attempt"
	SessionID   int64      `json:"session_id,omitempty"`
	AttemptID   int64      `json:"attempt_id,omitempty"`
	TaskID      int64      `json:"task_id,omitempty"`
	Name        string     `json:"name"`
	Agent       string     `json:"agent"`
	Branch      string     `json:"branch,omitempty"`
	AgentState  string     `json:"agent_state,omitempty"`
	LastPrompt  string     `json:"last_prompt,omitempty"`
	SameWorkdir bool       `json:"same_workdir"`
	Workdir     string     `json:"workdir,omitempty"`
	Files       []PeerFile `json:"files"`
}

// Peers returns every other live session, and every other running task
// attempt, sharing this session's repo_key — empty (not an error) for a
// session whose repo_key is not resolved yet or is RepoKeyNone. Never
// shells out: every repo_key it compares against is whatever has already
// been cached on a session or project row (see EnsureRepoKeyAsync), which
// is what keeps this callable straight from a hook's HTTP response.
func (t *Tracker) Peers(sess *store.Session) ([]Peer, error) {
	if sess == nil || sess.RepoKey == "" || sess.RepoKey == RepoKeyNone {
		return nil, nil
	}
	return t.peersFor(func(repoKey string) bool { return repoKey == sess.RepoKey }, sess.ID, sess.Workdir)
}

// PeersForCommonDir is Peers without a calling session: it matches live
// sessions/attempts whose repo_key's git-common-dir component (the part
// after the "<target id>:" prefix) equals commonDir, regardless of target —
// what the `active_work` MCP tool falls back to for a caller with no
// LECTERN_SESSION_ID (docs/agent-events.md: "or for a given repo path").
// Matching without a target id is a deliberate simplification: two
// different targets sharing an identical absolute checkout path is
// vanishingly unlikely in this single-operator tool, and a false-positive
// match here is harmless (advisory text, not an action).
func (t *Tracker) PeersForCommonDir(commonDir string) ([]Peer, error) {
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return nil, nil
	}
	suffix := ":" + commonDir
	return t.peersFor(func(repoKey string) bool { return strings.HasSuffix(repoKey, suffix) }, 0, "")
}

func (t *Tracker) peersFor(match func(repoKey string) bool, excludeSessionID int64, selfWorkdir string) ([]Peer, error) {
	now := store.Now()
	since := now - RecentEditsWindow.Seconds()

	live, err := t.DB.LiveSessions()
	if err != nil {
		return nil, err
	}
	var peers []Peer
	for _, other := range live {
		if other.ID == excludeSessionID || other.RepoKey == "" || other.RepoKey == RepoKeyNone || !match(other.RepoKey) {
			continue
		}
		files, err := t.RecentFiles(other.ID, since, now)
		if err != nil {
			return nil, err
		}
		peers = append(peers, Peer{
			Kind: "session", SessionID: other.ID, Name: other.Name, Agent: other.Agent,
			AgentState: other.AgentState, LastPrompt: other.LastPromptExcerpt,
			SameWorkdir: selfWorkdir != "" && other.Workdir == selfWorkdir, Workdir: other.Workdir,
			Files: files,
		})
	}

	running, err := t.DB.AttemptsWhere("status='running'")
	if err != nil {
		return nil, err
	}
	for _, att := range running {
		proj, err := t.DB.ProjectForAttempt(att.ID)
		if err != nil || proj == nil || proj.RepoKey == "" || !match(proj.RepoKey) {
			continue
		}
		name := "attempt #" + fmt.Sprint(att.ID)
		if task, terr := t.DB.Task(att.TaskID); terr == nil && task != nil {
			name = task.Title
		}
		agent := att.Agent
		if agent == "" {
			agent = proj.DefaultAgent
		}
		peers = append(peers, Peer{
			Kind: "attempt", AttemptID: att.ID, TaskID: att.TaskID, Name: name, Agent: agent,
			Branch: att.Branch, SameWorkdir: selfWorkdir != "" && att.WorktreePath == selfWorkdir, Workdir: att.WorktreePath,
			Files: []PeerFile{},
		})
	}
	return peers, nil
}

// RecentFiles is one session's own edited files at or after `since`, with
// ages computed against `now` — used both for a peer's file list (Peers
// above) and for the calling session's OWN recent files (the API handler's
// self_files, which the frontend intersects against each peer's files to
// render the "overlaps" chip without a second round trip).
func (t *Tracker) RecentFiles(sessionID int64, since, now float64) ([]PeerFile, error) {
	edits, err := t.DB.SessionFileEditsFor(sessionID, since)
	if err != nil {
		return nil, err
	}
	out := make([]PeerFile, 0, len(edits))
	for _, e := range edits {
		out = append(out, PeerFile{RelPath: e.RelPath, At: e.At, AgeSeconds: now - e.At})
	}
	return out, nil
}

// ---- briefing (SessionStart / UserPromptSubmit additionalContext) --------

// Briefing renders the "other agents are working in this repository right
// now" text for a session, or ok=false when there is nothing to say (no
// peers) or nothing NEW to say (same peer summary as last time, sent under
// BriefingTTL ago — docs/agent-events.md's dedup rule). Sending updates the
// session's dedup bookkeeping; a caller that decides not to use the text
// for some other reason (e.g. the setting is off) must not call this, since
// the side effect is unconditional.
func (t *Tracker) Briefing(sess *store.Session) (text string, ok bool) {
	peers, err := t.Peers(sess)
	if err != nil || len(peers) == 0 {
		return "", false
	}
	text = renderBriefing(peers)
	hash := hashString(text)
	now := store.Now()
	fresh := sess.AwarenessBriefingAt == nil || now-*sess.AwarenessBriefingAt >= BriefingTTL.Seconds()
	if hash == sess.AwarenessBriefingHash && !fresh {
		return "", false
	}
	if err := t.DB.Update("sessions", sess.ID, map[string]any{
		"awareness_briefing_hash": hash, "awareness_briefing_at": now,
	}); err != nil {
		t.Log.Warn("awareness: could not record briefing dedup state", "session", sess.ID, "err", err)
	}
	sess.AwarenessBriefingHash = hash
	sess.AwarenessBriefingAt = &now
	return text, true
}

func renderBriefing(peers []Peer) string {
	var b strings.Builder
	b.WriteString("Lectern: other agents are working in this repository right now —\n")
	for _, p := range peers {
		b.WriteString(fmt.Sprintf("• %s %s \"%s\"", agentLabel(p.Kind), agentIdent(p), p.Name))
		var bits []string
		if p.Branch != "" {
			bits = append(bits, "branch "+p.Branch)
		}
		if p.AgentState != "" {
			bits = append(bits, p.AgentState)
		}
		if len(bits) > 0 {
			b.WriteString(" (" + strings.Join(bits, ", ") + ")")
		}
		b.WriteString(":")
		if p.LastPrompt != "" {
			b.WriteString(" last asked \"" + p.LastPrompt + "\"")
		}
		if len(p.Files) > 0 {
			b.WriteString("; edited ")
			b.WriteString(fileList(p.Files, 3))
		}
		b.WriteString("\n")
	}
	b.WriteString("Coordinate before duplicating their work: check their branch or ask the operator.")
	return clip(b.String(), BriefingMaxChars)
}

func agentLabel(kind string) string {
	if kind == "attempt" {
		return "Task"
	}
	return "Session"
}

func agentIdent(p Peer) string {
	if p.Kind == "attempt" {
		return "#" + fmt.Sprint(p.AttemptID)
	}
	return "#" + fmt.Sprint(p.SessionID)
}

func fileList(files []PeerFile, max int) string {
	sort.Slice(files, func(i, j int) bool { return files[i].At > files[j].At })
	if max > len(files) {
		max = len(files)
	}
	parts := make([]string, 0, max)
	for _, f := range files[:max] {
		parts = append(parts, fmt.Sprintf("%s %s ago", f.RelPath, roundAge(f.AgeSeconds)))
	}
	rest := len(files) - max
	s := strings.Join(parts, ", ")
	if rest > 0 {
		s += fmt.Sprintf(" (+%d more)", rest)
	}
	return s
}

func roundAge(seconds float64) string {
	m := int(seconds / 60)
	if m < 1 {
		return "<1m"
	}
	return fmt.Sprintf("%dm", m)
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---- edit warning (PreToolUse additionalContext) --------------------------

// EditWarning renders a PreToolUse advisory for an Edit/Write/MultiEdit/
// NotebookEdit call whose target file was edited by a PEER within
// EditWarnWindow — never a denial, purely informational. ok is false when
// the tool/path is not tracked, the session has no resolved repo, or no
// peer touched this exact rel_path recently.
func (t *Tracker) EditWarning(sess *store.Session, toolName string, input map[string]any) (text string, ok bool) {
	if sess == nil || sess.RepoKey == "" || sess.RepoKey == RepoKeyNone {
		return "", false
	}
	absPath, fileOK := FilePathFromToolInput(toolName, input)
	if !fileOK {
		return "", false
	}
	rel, relOK := RelPath(sess.RepoToplevel, absPath)
	if !relOK {
		return "", false
	}
	now := store.Now()
	since := now - EditWarnWindow.Seconds()
	edits, err := t.DB.SessionFileEditsForRepo(sess.RepoKey, since)
	if err != nil {
		return "", false
	}
	var latest *store.SessionFileEdit
	for _, e := range edits {
		if e.RelPath != rel || e.SessionID == sess.ID {
			continue
		}
		if latest == nil || e.At > latest.At {
			latest = e
		}
	}
	if latest == nil {
		return "", false
	}
	peer, err := t.DB.Session(latest.SessionID)
	if err != nil {
		return "", false
	}
	age := roundAge(now - latest.At)
	if peer.Workdir == sess.Workdir {
		return fmt.Sprintf("Lectern: session #%d %q edited %s %s ago in this SAME working directory — "+
			"your changes may collide or be overwritten. Check what they did before proceeding.",
			peer.ID, peer.Name, rel, age), true
	}
	return fmt.Sprintf("Lectern: session #%d %q edited %s %s ago in a separate worktree of this repository — "+
		"a merge conflict is likely later, not an immediate collision. Consider coordinating on the change.",
		peer.ID, peer.Name, rel, age), true
}

// ---- duplicate-prompt detection (Needs-you notice) -------------------------

// DuplicatePair is two live sessions in the same repo whose recent prompts
// look like the same task.
type DuplicatePair struct {
	SessionAID int64   `json:"session_a_id"`
	SessionA   string  `json:"session_a"`
	SessionBID int64   `json:"session_b_id"`
	SessionB   string  `json:"session_b"`
	Score      float64 `json:"score"`
}

// DuplicatePrompts scans every live session with a resolved repo_key for
// pairs, in the SAME repo, whose last prompts (within the last hour) look
// like duplicate work — Jaccard similarity over normalised words >= 0.5.
// O(n^2) over live sessions, which is fine at the session counts this tool
// ever runs at (a handful, not thousands).
func (t *Tracker) DuplicatePrompts() ([]DuplicatePair, error) {
	live, err := t.DB.LiveSessions()
	if err != nil {
		return nil, err
	}
	now := store.Now()
	cutoff := now - time.Hour.Seconds()
	type cand struct {
		id    int64
		name  string
		words map[string]bool
	}
	byRepo := map[string][]cand{}
	for _, s := range live {
		if s.RepoKey == "" || s.RepoKey == RepoKeyNone || s.LastPromptExcerpt == "" || s.LastPromptAt == nil || *s.LastPromptAt < cutoff {
			continue
		}
		byRepo[s.RepoKey] = append(byRepo[s.RepoKey], cand{id: s.ID, name: s.Name, words: wordSet(s.LastPromptExcerpt)})
	}
	var out []DuplicatePair
	for _, group := range byRepo {
		for i := 0; i < len(group); i++ {
			for j := i + 1; j < len(group); j++ {
				score := jaccard(group[i].words, group[j].words)
				if score >= 0.5 {
					out = append(out, DuplicatePair{
						SessionAID: group[i].id, SessionA: group[i].name,
						SessionBID: group[j].id, SessionB: group[j].name, Score: score,
					})
				}
			}
		}
	}
	return out, nil
}

func wordSet(s string) map[string]bool {
	fields := strings.Fields(strings.ToLower(s))
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, ".,!?;:\"'()[]{}")
		if f == "" {
			continue
		}
		out[f] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
