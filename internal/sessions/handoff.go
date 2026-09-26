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
	"github.com/JeremiahM37/lectern/v2/internal/limits"
	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
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
	// Continue primes the successor to carry on with the work rather than
	// report and wait — used when a usage limit, not the operator, is the
	// reason for the switch (docs/rate-limits.md).
	Continue bool
	launch   *LaunchOpts
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

// StartLimitHandoff moves a session stopped by its usage limit to another
// agent or model in the same workspace. The limited agent cannot write a
// handoff, so runHandoff builds one from what Lectern captured (limitWrap).
// The original session is kept; its hold is resolved once the successor runs.
func (m *Manager) StartLimitHandoff(id int64, agent, model string, profileID int64) error {
	return m.StartHandoff(id, HandoffOpts{Successor: true, Agent: agent, Model: model,
		ProfileID: profileID, QuickSwitch: true, Continue: true})
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

// HandoffPollInterval is the default for Manager.HandoffPoll: how often a
// handoff checks whether the agent has finished writing its wrap. Agents take
// seconds to minutes to write one, so checking more often buys nothing in
// production; tests lower it so each handoff does not cost a fixed three
// seconds of waiting.
var HandoffPollInterval = 3 * time.Second

func (m *Manager) handoffPoll() time.Duration {
	if m.HandoffPoll > 0 {
		return m.HandoffPoll
	}
	return HandoffPollInterval
}

func (m *Manager) runHandoff(ctx context.Context, sess *store.Session, o HandoffOpts) error {
	_, ex, err := m.resolve(sess.ID)
	if err != nil {
		return err
	}
	// A session stopped by its usage limit cannot answer the wrap request;
	// asking would only time out. Hand over what Lectern can see instead.
	var wrap string
	hold, _ := m.DB.OpenLimitHoldForSession(sess.ID)
	if hold.Open() {
		wrap = m.limitWrap(ctx, sess, ex, hold)
	} else if wrap, err = m.requestWrap(ctx, sess, ex); err != nil {
		return err
	}

	projectName := sess.ProjectName
	wrapID, err := m.DB.InsertWrap(&store.Wrap{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Summary: wrap,
		Transcript: sess.PaneTail})
	if err != nil {
		return err
	}
	res := HandoffResult{WrapID: wrapID, Summary: wrap}

	// the wrap goes into project memory too, so a DISPATCHED task on this
	// project starts from the same state an interactive session would. A
	// limit capture is mostly a screen dump, not knowledge, so it stays out.
	if sess.ProjectID != nil && !hold.Open() {
		m.DB.InsertNote(*sess.ProjectID, "Session handoff: "+clipRunes(wrap, 900), nil)
	}
	if m.Memory != nil && !hold.Open() && m.Memory.Available(ctx) {
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
		if o.Continue {
			prime = LimitResumePrompt(projectName, wrap, m.projectPrime(ctx, projectName))
		}
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
		if hold.Open() {
			m.resolveLimitHandoff(hold.ID, next.ID)
		}
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

// limitWrap is the handoff for a session its usage limit has stopped: the
// limit itself, what the agent was last asked, and the end of its screen.
// Together with the unchanged workspace that is what the successor needs to
// pick the work up. If the CLI is counting down to continue by itself, that is
// cancelled, so two agents never end up working in one workspace.
func (m *Manager) limitWrap(ctx context.Context, sess *store.Session, ex executor.Executor, hold *store.LimitHold) string {
	pane := sess.PaneTail
	cmd := fmt.Sprintf("printf '%%s' %s; tmux capture-pane -p -t %s -S -200 -J",
		shellq.Quote(PollDelimiter+sess.TmuxSession+"\n"), shellq.Quote("="+sess.TmuxSession+":"))
	if r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 15}); err == nil && r.OK() {
		if text := ParsePoll(r.Stdout)[sess.TmuxSession]; strings.TrimSpace(text) != "" {
			pane = text
		}
	}
	if hit, ok := limits.DetectTail(pane, limits.TailLines, time.Now()); ok && hit.SelfResume {
		if err := m.SendKey(ctx, sess.ID, "Escape"); err != nil {
			m.Log.Warn("could not cancel the CLI's own limit wait", "session", sess.ID, "err", err)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "WHERE WE ARE: the previous %s session", sess.Agent)
	if sess.Model != "" {
		fmt.Fprintf(&b, " (%s)", sess.Model)
	}
	fmt.Fprintf(&b, " was stopped by its usage limit: %q. It could not write its own handoff; "+
		"this was captured by Lectern instead.\n", hold.Message)
	if prompt := strings.TrimSpace(sess.LastPromptExcerpt); prompt != "" {
		fmt.Fprintf(&b, "\nLAST REQUEST it was working on: %s\n", prompt)
	}
	fmt.Fprintf(&b, "\nSTATE: the workspace %s is exactly as it left it, including uncommitted work. "+
		"Check git status and the recent diff before changing anything.\n", sess.Workdir)
	tail := strings.TrimSpace(pane)
	if r := []rune(tail); len(r) > 6000 {
		tail = string(r[len(r)-6000:])
	}
	fmt.Fprintf(&b, "\nEND OF ITS SCREEN:\n```\n%s\n```", tail)
	return b.String()
}

// requestWrap asks the agent to write its handoff and waits for it.
func (m *Manager) requestWrap(ctx context.Context, sess *store.Session, ex executor.Executor) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	path := fmt.Sprintf("/tmp/lectern-handoff-%d-%s.md", sess.ID, hex.EncodeToString(nonce))
	// clear any wrap left by an earlier handoff on this session, or we would
	// happily "capture" the previous one and call it current
	if _, err := ex.Run(ctx, "rm -f "+path, executor.RunOpts{Timeout: 20}); err != nil {
		return "", err
	}
	if err := m.SendText(ctx, sess.ID, HandoffPrompt(path)); err != nil {
		return "", err
	}

	deadline := time.Now().Add(m.HandoffTimeout)
	var wrap string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(m.handoffPoll()):
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
		return "", fmt.Errorf("the agent did not write a handoff within %s", m.HandoffTimeout)
	}
	ex.Run(ctx, "rm -f "+path, executor.RunOpts{Timeout: 20})
	return wrap, nil
}

// resolveLimitHandoff closes the predecessor's usage-limit hold once its
// successor is running, whether the limit policy or the operator's own switch
// started the handoff. The hold's own compare-and-swap keeps a racing resume
// from also acting on it.
func (m *Manager) resolveLimitHandoff(holdID, successorID int64) {
	fields := func() map[string]any {
		return map[string]any{"state": limits.StateHandedOff, "resolved_at": store.Now(), "successor_id": successorID}
	}
	for _, from := range []string{limits.StateHandingOff, limits.StateWaiting} {
		if ok, _ := m.DB.TransitionLimitHold(holdID, from, fields()); ok {
			return
		}
	}
}

// LimitResumePrompt primes a successor that takes over because its
// predecessor hit a usage limit: unlike ResumePrompt, it should carry on.
func LimitResumePrompt(projectName, wrap, priming string) string {
	var b strings.Builder
	b.WriteString("You are taking over work on ")
	if projectName != "" {
		b.WriteString(projectName)
	} else {
		b.WriteString("this project")
	}
	b.WriteString(" from another agent that was stopped by its usage limit. Lectern captured this:\n\n---\n")
	b.WriteString(strings.TrimSpace(wrap))
	b.WriteString("\n---\n")
	if strings.TrimSpace(priming) != "" {
		b.WriteString("\n" + strings.TrimSpace(priming) + "\n")
	}
	b.WriteString("\nConfirm the state of the workspace (git status, the recent diff), then continue " +
		"the task it was working on. If what it was doing is unclear, say what you found and stop.")
	return b.String()
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
