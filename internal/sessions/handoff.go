package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/memory"
	"github.com/JeremiahM37/lectern/internal/store"
)

// HandoffPrompt is what we ask a session to write before it is retired.
//
// The point of a handoff is that the PROJECT survives the context window. A
// conversation is not a durable representation of a piece of software: keeping
// one alive for months just means everything important eventually lives in
// whatever survived compaction. A wrap makes the durable unit explicit — the
// agent states, while it still has the context, what the next one needs.
func HandoffPrompt(path string) string {
	return fmt.Sprintf(`Before this session ends, write a handoff for the agent that
picks this project up next. Write it to %s (create the file; do not print it here).

Cover, briefly and concretely:
- WHERE WE ARE: what is done, what is half-done, what is verified vs assumed
- NEXT: the specific next steps, in order
- DECISIONS: choices made and why, so they are not re-litigated
- GOTCHAS: anything that cost time and would cost it again
- STATE: branches, worktrees, running processes, uncommitted work

Write it for someone with no memory of this conversation but full access to the
repo. Facts over narrative.
Write to %s.partial first. After all sections are complete, append this exact
completion marker on its own final line:
%s
Then atomically rename the completed file to %s. Do not publish the final path
until writing is finished. Reply with exactly: WRAPPED`, path, path, handoffMarker(path), path)
}

// ResumePrompt primes a fresh session with its predecessor's wrap.
func ResumePrompt(projectName, wrap, priming string) string {
	var b strings.Builder
	b.WriteString("You are continuing work on ")
	if projectName != "" {
		b.WriteString(projectName)
	} else {
		b.WriteString("this project")
	}
	b.WriteString(". A previous session wrote this handoff:\n\n---\n")
	b.WriteString(strings.TrimSpace(wrap))
	b.WriteString("\n---\n")
	if strings.TrimSpace(priming) != "" {
		b.WriteString("\n" + strings.TrimSpace(priming) + "\n")
	}
	b.WriteString("\nRead what you need from the repo to confirm the state above, " +
		"then tell me where things stand and what you plan to do next. " +
		"Do not start changing things until I say so.")
	return b.String()
}

// HandoffOpts controls what happens around a wrap.
type HandoffOpts struct {
	// Successor starts a fresh session on the same project, primed with the wrap.
	Successor bool
	// KillOld retires the old session once the wrap is captured.
	KillOld bool
	// Agent/Model for the successor; empty reuses the old session's.
	Agent string
	Model string
}

// HandoffResult reports what a wrap produced.
type HandoffResult struct {
	WrapID     int64          `json:"wrap_id"`
	Summary    string         `json:"summary"`
	Session    *store.Session `json:"session,omitempty"`
	Remembered bool           `json:"remembered"`
}

// StartHandoff asks a session to write its wrap and finishes the job in the
// background: an agent mid-turn can take minutes to answer, and the operator
// should not sit on a hanging request to find that out.
func (m *Manager) StartHandoff(id int64, o HandoffOpts) error {
	m.mu.Lock()
	if m.handoffs[id] {
		m.mu.Unlock()
		return fmt.Errorf("a handoff is already in flight for this session")
	}
	m.handoffs[id] = true
	m.mu.Unlock()

	sess, _, err := m.resolve(id)
	if err != nil {
		m.clearHandoff(id)
		return err
	}
	go func() {
		defer m.clearHandoff(id)
		ctx, cancel := context.WithTimeout(context.Background(), m.HandoffTimeout+time.Minute)
		defer cancel()
		if err := m.runHandoff(ctx, sess, o); err != nil {
			m.Log.Warn("handoff failed", "session", id, "err", err)
			m.Bus.Publish("board", "session_handoff", map[string]any{
				"session_id": id, "ok": false, "error": err.Error()})
		}
	}()
	return nil
}

// InFlight reports whether a session currently has a wrap being written.
func (m *Manager) InFlight(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handoffs[id]
}

func (m *Manager) clearHandoff(id int64) {
	m.mu.Lock()
	delete(m.handoffs, id)
	m.mu.Unlock()
}

func (m *Manager) runHandoff(ctx context.Context, sess *store.Session, o HandoffOpts) error {
	_, ex, err := m.resolve(sess.ID)
	if err != nil {
		return err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	path := fmt.Sprintf("/tmp/lectern-handoff-%d-%s.md", sess.ID, hex.EncodeToString(nonce))
	// clear any wrap left by an earlier handoff on this session, or we would
	// happily "capture" the previous one and call it current
	if _, err := ex.Run(ctx, "rm -f "+path, executor.RunOpts{Timeout: 20}); err != nil {
		return err
	}
	if err := m.SendText(ctx, sess.ID, HandoffPrompt(path)); err != nil {
		return err
	}

	deadline := time.Now().Add(m.HandoffTimeout)
	var wrap string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		raw, err := ex.ReadFile(ctx, path, 0)
		if err == nil {
			if completed, ok := completedHandoff(raw, path); ok {
				wrap = completed
				break
			}
		}
	}
	if wrap == "" {
		return fmt.Errorf("the agent did not write a handoff within %s", m.HandoffTimeout)
	}
	ex.Run(ctx, "rm -f "+path, executor.RunOpts{Timeout: 20})

	projectName := sess.ProjectName
	wrapID, err := m.DB.InsertWrap(&store.Wrap{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Summary: wrap,
		Transcript: sess.PaneTail})
	if err != nil {
		return err
	}
	res := HandoffResult{WrapID: wrapID, Summary: wrap}

	// the wrap goes into project memory too, so a DISPATCHED task on this
	// project starts from the same state an interactive session would
	if sess.ProjectID != nil {
		m.DB.InsertNote(*sess.ProjectID, "Session handoff: "+clipRunes(wrap, 900), nil)
	}
	if m.Memory != nil && m.Memory.Available(ctx) {
		topic := ""
		if sess.ProjectID != nil {
			if project, err := m.DB.Project(*sess.ProjectID); err == nil {
				topic = project.MemoryTopic
			}
		}
		if err := m.Memory.Remember(ctx, memory.Entry{
			// The same key the agent's own writes carry, not the display name:
			// names repeat and can be edited, and a key that does either is a label.
			Project: projectName, Topic: topic, Session: MemorySessionKey(sess.ID), Agent: sess.Agent,
			Category: "handoff", Text: wrap,
		}); err != nil {
			m.Log.Warn("memory provider rejected the wrap", "err", err)
		} else {
			res.Remembered = true
		}
	}

	// KillOld is honoured on its own terms. It used to be implied by Successor,
	// so a handoff that started a new agent always killed the old one even when
	// the operator had explicitly said not to — and comparing two agents on the
	// same work, which is the obvious reason to keep both, was impossible.
	if o.KillOld {
		if err := m.Kill(ctx, sess.ID); err != nil {
			m.Log.Warn("could not retire the old session", "session", sess.ID, "err", err)
		}
	}
	if o.Successor {
		prime := ResumePrompt(projectName, wrap, m.projectPrime(ctx, projectName))
		next, err := m.Launch(ctx, LaunchOpts{
			GroupPath: sess.GroupPath,
			ProjectID: sess.ProjectID, TargetID: sess.TargetID,
			Name:    sess.Name,
			Agent:   firstNonEmpty(o.Agent, sess.Agent),
			Model:   firstNonEmpty(o.Model, sess.Model),
			Workdir: sess.Workdir, Prime: prime,
		})
		if err != nil {
			return fmt.Errorf("wrap saved, but the successor failed to start: %w", err)
		}
		m.DB.Exec(`UPDATE session_wraps SET next_session_id=? WHERE id=?`, next.ID, wrapID)
		res.Session = next
	}
	m.Bus.Publish("board", "session_handoff", map[string]any{
		"session_id": sess.ID, "ok": true, "wrap_id": wrapID,
		"successor": res.Session, "remembered": res.Remembered})
	m.Log.Info("session wrapped", "session", sess.ID, "wrap", wrapID,
		"successor", res.Session != nil, "remembered", res.Remembered)
	return nil
}

// projectPrime pulls what the memory provider knows about a project, so a fresh
// session starts with the project's knowledge and not just its predecessor's.
func (m *Manager) projectPrime(ctx context.Context, projectName string) string {
	if _, automatic := m.Memory.(memory.AutomaticProvider); automatic {
		return ""
	}
	if projectName == "" {
		return ""
	}
	return memory.LoadBrief(ctx, m.Memory, projectName).Prompt()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// The final marker is bound to a unique request path, so neither a partial file
// nor a late writer from a previous attempt can complete this handoff.
func handoffMarker(path string) string { return "<!-- lectern:complete " + path + " -->" }

func completedHandoff(raw []byte, path string) (string, bool) {
	text := strings.TrimSpace(string(raw))
	marker := "\n" + handoffMarker(path)
	if !strings.HasSuffix(text, marker) {
		return "", false
	}
	body := strings.TrimSpace(strings.TrimSuffix(text, marker))
	return body, body != ""
}
