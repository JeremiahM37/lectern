package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// HandoffStatus is the observable progress of a switch. A switch is two
// separate operations — the predecessor writing its wrap, then the successor
// starting — and the operator should be able to see which one is running
// instead of watching an opaque "Switching…".
type HandoffStatus struct {
	Phase       string `json:"phase"`                 // saving | starting
	Destination string `json:"destination,omitempty"` // what the successor will be
	SuccessorID int64  `json:"successor_id,omitempty"`
}

// handoffState is the manager's handoff bookkeeping in one place. It is a
// pointer so the zero Manager stays usable in focused tests.
type handoffState struct {
	mu       sync.Mutex
	inFlight map[int64]HandoffStatus
	errors   map[int64]string
}

func newHandoffState() *handoffState {
	return &handoffState{inFlight: map[int64]HandoffStatus{}, errors: map[int64]string{}}
}

// handoffState lazily supplies the bookkeeping for a Manager built by hand in a
// test, so the zero value stays usable. Both the check and the assignment happen
// under m.mu: two goroutines reaching a zero Manager together must not race on
// the field, and every caller takes this path without holding m.mu already.
func (m *Manager) handoffState() *handoffState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handoffs == nil {
		m.handoffs = newHandoffState()
	}
	return m.handoffs
}

// Handoff comes back with the phase of a switch that is still running, and the
// successor id once one has started. Reloading the page mid-switch must not
// lose either.
func (m *Manager) Handoff(id int64) (HandoffStatus, bool) {
	s := m.handoffState()
	s.mu.Lock()
	defer s.mu.Unlock()
	status, ok := s.inFlight[id]
	return status, ok
}

// HandoffError is the reason the last switch on this session failed, kept until
// the next one starts so a reload can still explain what happened.
func (m *Manager) HandoffError(id int64) string {
	s := m.handoffState()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errors[id]
}

func (m *Manager) setHandoffPhase(id int64, phase, destination string) {
	s := m.handoffState()
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.inFlight[id]
	status.Phase, status.Destination = phase, destination
	s.inFlight[id] = status
}

func (m *Manager) setHandoffSuccessor(id, successorID int64) {
	s := m.handoffState()
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.inFlight[id]
	status.SuccessorID = successorID
	s.inFlight[id] = status
}

// finishHandoff clears in-flight state. A failure is remembered, a success is
// not: the successor session is the evidence that it worked.
func (m *Manager) finishHandoff(id int64, failure string) {
	s := m.handoffState()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inFlight, id)
	if failure != "" {
		s.errors[id] = failure
		return
	}
	delete(s.errors, id)
}

// beginHandoff claims the session and returns false when a switch is already
// running for it.
func (m *Manager) beginHandoff(id int64) bool {
	s := m.handoffState()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.inFlight[id]; busy {
		return false
	}
	delete(s.errors, id)
	s.inFlight[id] = HandoffStatus{Phase: "saving"}
	return true
}

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
	Agent     string
	Model     string
	ProfileID int64
	// QuickSwitch treats an empty model as the destination's default.
	QuickSwitch bool
	launch      *LaunchOpts
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
	if !m.beginHandoff(id) {
		return fmt.Errorf("a handoff is already in flight for this session")
	}

	sess, _, err := m.resolve(id)
	if err != nil {
		m.finishHandoff(id, err.Error())
		return err
	}
	if o.Successor {
		next, err := m.handoffLaunch(sess, o)
		if err != nil {
			m.finishHandoff(id, err.Error())
			return err
		}
		o.launch = &next
		// The picker can say exactly where the context is going while the
		// predecessor is still writing, not just "switching".
		m.setHandoffPhase(id, "saving", handoffDestination(next))
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), m.HandoffTimeout+time.Minute)
		defer cancel()
		if err := m.runHandoff(ctx, sess, o); err != nil {
			m.finishHandoff(id, err.Error())
			m.Log.Warn("handoff failed", "session", id, "err", err)
			m.Bus.Publish("board", "session_handoff", map[string]any{
				"session_id": id, "ok": false, "error": err.Error()})
			return
		}
		m.finishHandoff(id, "")
	}()
	return nil
}

// InFlight reports whether a session currently has a wrap being written.
func (m *Manager) InFlight(id int64) bool {
	_, ok := m.Handoff(id)
	return ok
}

// handoffDestination names what the successor will be, so the operator sees
// "Starting Codex · astra-test" rather than a generic switch.
func handoffDestination(o LaunchOpts) string {
	if o.Configuration != nil && o.Configuration.ProfileName != "" {
		return o.Configuration.ProfileName
	}
	name := strings.TrimSpace(o.Agent)
	if name == "" {
		name = "session"
	} else {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	if o.Model == "" {
		return name + " · default model"
	}
	return name + " · " + o.Model
}

func (m *Manager) handoffLaunch(sess *store.Session, o HandoffOpts) (LaunchOpts, error) {
	agent := firstNonEmpty(o.Agent, sess.Agent)
	if o.ProfileID != 0 && o.Agent == "" {
		agent = ""
	}
	model := o.Model
	if model == "" && agent == sess.Agent && o.ProfileID == 0 && !o.QuickSwitch {
		model = sess.Model
	}
	cfg, err := m.SessionLaunchConfiguration(sess)
	if err != nil {
		return LaunchOpts{}, err
	}
	next := LaunchOpts{GroupPath: sess.GroupPath, ProjectID: sess.ProjectID,
		TargetID: sess.TargetID, Name: sess.Name, Workdir: sess.Workdir,
		Agent: agent, Model: model, ProfileID: o.ProfileID, Yolo: cfg.Yolo}
	next, err = m.ApplyLaunchProfile(next)
	if err != nil {
		return next, err
	}
	if next.Configuration == nil {
		next.Configuration, err = m.launchConfiguration(next.Agent, next.ProjectID, nil)
		if err == nil {
			next.Configuration.Yolo = next.Yolo
		}
	}
	return next, err
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
	if o.Successor {
		prime := ResumePrompt(projectName, wrap, m.projectPrime(ctx, projectName))
		launch := o.launch
		if launch == nil {
			resolved, err := m.handoffLaunch(sess, o)
			if err != nil {
				return err
			}
			launch = &resolved
		}
		launch.Prime = prime
		m.setHandoffPhase(sess.ID, "starting", handoffDestination(*launch))
		next, err := m.Launch(ctx, *launch)
		if err != nil {
			return fmt.Errorf("wrap saved, but the successor failed to start: %w", err)
		}
		res.Session = next
		m.setHandoffSuccessor(sess.ID, next.ID)
	}
	// A failed successor must never cost the operator the original session.
	if o.KillOld {
		if err := m.Kill(ctx, sess.ID); err != nil {
			m.Log.Warn("could not retire the old session", "session", sess.ID, "err", err)
		}
	}
	if res.Session != nil {
		m.DB.Exec(`UPDATE session_wraps SET next_session_id=? WHERE id=?`, res.Session.ID, wrapID)
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
