package decisionlog

import (
	"encoding/json"
	"log/slog"
	"sync"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Recorder turns hook events into decisions. It is an events.OnHookEvent
// subscriber: the state machine runs under a short lock on the hook path, and
// each database write runs in its own goroutine, so a hook never waits on
// SQLite. The state is per session and lives in memory; a restart forgets a
// pending wait, which costs one decision, never a wrong one.
type Recorder struct {
	DB *store.DB

	now func() float64

	mu       sync.Mutex
	sessions map[int64]*sessionState
	writes   sync.WaitGroup
}

// sessionState is what one session's hooks have said so far.
type sessionState struct {
	agent string

	// working is true from a UserPromptSubmit until the next Stop.
	working bool
	// waiting is true from a Stop until the next UserPromptSubmit; the gap is
	// the time the agent's reply waited for a person.
	waiting      bool
	waitingSince float64

	// lastStop is the most recent Stop, for Review latency.
	lastStop float64
	hasStop  bool

	// pending is a PermissionRequest not yet answered by a PostToolUse for the
	// same tool, nor cut short by a Stop or a new prompt.
	pending     bool
	pendingTool string
	pendingAt   float64
}

// New builds a Recorder over db.
func New(db *store.DB) *Recorder {
	return &Recorder{DB: db, now: store.Now, sessions: map[int64]*sessionState{}}
}

// Wait blocks until every queued write has finished. Tests use it; the server
// never needs to.
func (r *Recorder) Wait() {
	if r == nil {
		return
	}
	r.writes.Wait()
}

// hookProbe pulls the only fields the recorder reads from a hook body. The
// prompt is decoded so its length can be taken; the text itself is dropped.
type hookProbe struct {
	ToolName string `json:"tool_name"`
	Prompt   string `json:"prompt"`
}

// HandleHookEvent is the events.OnHookEvent subscriber. It returns at once.
// Events the recorder does not read are ignored.
func (r *Recorder) HandleHookEvent(sess *store.Session, event string, body []byte, state string, changed bool) {
	if r == nil || sess == nil || !Enabled() {
		return
	}
	switch event {
	case agentevents.EventStop, agentevents.EventAgentTurnComplete,
		agentevents.EventUserPromptSubmit, agentevents.EventPermissionRequest, agentevents.EventPostToolUse:
	default:
		return
	}
	var probe hookProbe
	if event == agentevents.EventUserPromptSubmit || event == agentevents.EventPermissionRequest ||
		event == agentevents.EventPostToolUse {
		_ = json.Unmarshal(body, &probe)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	st := r.stateFor(sess)
	switch event {
	case agentevents.EventStop, agentevents.EventAgentTurnComplete:
		r.resolvePermission(sess.ID, st, now, ViaInferred, KindPermissionPrompt, "denied_or_interrupted")
		st.waiting = true
		st.waitingSince = now
		st.working = false
		st.lastStop = now
		st.hasStop = true

	case agentevents.EventUserPromptSubmit:
		r.resolvePermission(sess.ID, st, now, ViaInferred, KindPermissionPrompt, "denied_or_interrupted")
		chars := utf8.RuneCountInString(probe.Prompt)
		switch {
		case st.waiting:
			waited := st.waitingSince
			latency := now - waited
			r.emit(store.Decision{
				At: now, Kind: KindTurnResponse, Decision: "reply", Via: ViaTerminal,
				SessionID: idPtr(sess.ID), Agent: st.agent,
				WaitedSince: &waited, LatencyS: &latency,
				Detail: detailJSON(map[string]any{"prompt_chars": chars}),
			})
			st.waiting = false
		case st.working:
			r.emit(store.Decision{
				At: now, Kind: KindInterrupt, Decision: "interrupt", Via: ViaTerminal,
				SessionID: idPtr(sess.ID), Agent: st.agent,
				Detail: detailJSON(map[string]any{"prompt_chars": chars}),
			})
		}
		st.working = true

	case agentevents.EventPermissionRequest:
		// Claude asks one permission at a time, so a new request replaces any
		// unresolved one rather than queueing behind it.
		st.pending = true
		st.pendingTool = probe.ToolName
		st.pendingAt = now

	case agentevents.EventPostToolUse:
		if st.pending && probe.ToolName == st.pendingTool {
			waited := st.pendingAt
			latency := now - waited
			r.emit(store.Decision{
				At: now, Kind: KindPermissionPrompt, Decision: "allowed", Via: ViaInferred,
				SessionID: idPtr(sess.ID), Agent: st.agent,
				WaitedSince: &waited, LatencyS: &latency,
				Detail: detailJSON(map[string]any{"tool": st.pendingTool}),
			})
			st.pending = false
		}
	}
}

// resolvePermission closes an unanswered permission request with the given
// decision. The caller holds r.mu.
func (r *Recorder) resolvePermission(sessionID int64, st *sessionState, now float64, via, kind, decision string) {
	if !st.pending {
		return
	}
	waited := st.pendingAt
	latency := now - waited
	r.emit(store.Decision{
		At: now, Kind: kind, Decision: decision, Via: via,
		SessionID: idPtr(sessionID), Agent: st.agent,
		WaitedSince: &waited, LatencyS: &latency,
		Detail: detailJSON(map[string]any{"tool": st.pendingTool}),
	})
	st.pending = false
}

// stateFor returns the session's state, creating it on first sight. The agent
// is refreshed each time so a session whose row changed is not stuck on an old
// name. The caller holds r.mu.
func (r *Recorder) stateFor(sess *store.Session) *sessionState {
	st, ok := r.sessions[sess.ID]
	if !ok {
		st = &sessionState{}
		r.sessions[sess.ID] = st
	}
	st.agent = sess.Agent
	return st
}

// RecordReview records a Review action (commit, push, comments sent, discard,
// or a task review). Latency is measured from the session's last Stop when
// one is known: that is when the agent stopped and the person started looking.
// A task review has no session, so its latency is null.
func (r *Recorder) RecordReview(sessionID int64, agent, kind, decision string, detail map[string]any) {
	if r == nil || !Enabled() {
		return
	}
	now := r.now()
	var waited, latency *float64
	if sessionID != 0 {
		r.mu.Lock()
		if st, ok := r.sessions[sessionID]; ok && st.hasStop {
			w := st.lastStop
			l := now - w
			waited, latency = &w, &l
		}
		r.mu.Unlock()
	}
	d := store.Decision{
		At: now, Kind: kind, Decision: decision, Via: ViaWeb, Agent: agent,
		WaitedSince: waited, LatencyS: latency, Detail: detailJSON(detail),
	}
	if sessionID != 0 {
		d.SessionID = idPtr(sessionID)
	}
	r.emit(d)
}

// emit writes a decision in the background. A failed write is logged and
// dropped: a decision log must never fail the hook or the Review action.
func (r *Recorder) emit(d store.Decision) {
	if r.DB == nil {
		return
	}
	r.writes.Add(1)
	go func() {
		defer r.writes.Done()
		if err := Record(r.DB, d); err != nil {
			slog.Default().Warn("decision log write failed", "kind", d.Kind, "err", err)
		}
	}()
}

func idPtr(v int64) *int64 { return &v }
