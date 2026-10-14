package effects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/checks"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// The values of projects.effect_checks (docs/effect-checks.md).
const (
	ModeOff     = "off"
	ModeShow    = "show"
	ModeEnforce = "enforce"
)

// ModeOf reads a project's effect_checks value. Anything that is not "off" or
// "enforce" is "show", the default, so an unexpected value never silences the
// checks.
func ModeOf(v string) string {
	switch v {
	case ModeOff, ModeEnforce:
		return v
	}
	return ModeShow
}

// enforceCapPerHour bounds how many notices a session can be sent in an hour.
// An agent that keeps contradicting itself after a notice would otherwise be
// sent the same correction on every turn.
const enforceCapPerHour = 3

// checkWaitPoll is how often a claim about tests re-reads a running check.
const checkWaitPoll = 2 * time.Second

// SessionSender is how a machine-originated message reaches a session;
// *sessions.Manager satisfies it (its SendNotice).
type SessionSender interface {
	SendNotice(ctx context.Context, id int64, text string) error
}

// Notifier is the phone-push side; *sinks.Notifier satisfies it.
type Notifier interface {
	Notify(title, body, urlPath string, extra *sinks.Extra)
}

// Watcher evaluates the completion claims in each agent turn's final message.
// It is a subscriber on the hook path, so HandleHookEvent does no probing: it
// only decides whether a turn is worth checking and starts a goroutine. The
// evaluation then runs without holding the agent's HTTP hook response.
type Watcher struct {
	DB       *store.DB
	Reg      *executor.Registry
	Bus      *bus.Bus
	Notifier Notifier
	Sessions SessionSender
	Log      *slog.Logger
	// CheckTimeout bounds how long a claim about tests waits for a running
	// check. checks.DefaultTimeout is used when unset.
	CheckTimeout time.Duration

	// now is a seam for tests; time.Now in production.
	now func() time.Time

	mu       sync.Mutex
	since    map[int64]time.Time // session id -> latest UserPromptSubmit
	inflight map[string]bool     // session id + message hash being evaluated
}

// New builds a Watcher.
func New(db *store.DB, reg *executor.Registry, b *bus.Bus, notifier Notifier, sessions SessionSender, timeout time.Duration, log *slog.Logger) *Watcher {
	return &Watcher{DB: db, Reg: reg, Bus: b, Notifier: notifier, Sessions: sessions,
		CheckTimeout: timeout, Log: log, now: time.Now,
		since: map[int64]time.Time{}, inflight: map[string]bool{}}
}

// MessageHash identifies a message for the dedupe. It is a digest, not the
// text, so the table never stores a second copy of the agent's message.
func MessageHash(message string) string {
	sum := sha256.Sum256([]byte(message))
	return hex.EncodeToString(sum[:])
}

// HandleHookEvent is the events.OnHookEvent subscriber. It returns at once.
// A UserPromptSubmit starts a new turn, so its time becomes the Since fact
// that restart claims are compared against. A Stop, or codex's
// AgentTurnComplete, with a last message that makes a claim starts one
// evaluation in the background.
func (w *Watcher) HandleHookEvent(sess *store.Session, event string, body []byte, state string, changed bool) {
	if sess == nil {
		return
	}
	switch event {
	case agentevents.EventUserPromptSubmit:
		w.mu.Lock()
		w.since[sess.ID] = w.clock()
		w.mu.Unlock()
		return
	case agentevents.EventStop, agentevents.EventAgentTurnComplete:
	default:
		return
	}
	message := lastAssistantMessage(body)
	if strings.TrimSpace(message) == "" || len(Extract(message)) == 0 {
		return
	}
	hash := MessageHash(message)
	key := fmt.Sprintf("%d:%s", sess.ID, hash)
	w.mu.Lock()
	if w.inflight[key] {
		w.mu.Unlock()
		return
	}
	w.inflight[key] = true
	since, ok := w.since[sess.ID]
	w.mu.Unlock()
	if !ok {
		// No prompt seen since this process started: the session's own
		// creation time is the honest lower bound for "this turn".
		since = time.Unix(0, int64(sess.CreatedAt*1e9))
	}
	go w.evaluate(sess.ID, message, hash, key, since)
}

// lastAssistantMessage pulls last_assistant_message out of a Stop or
// AgentTurnComplete body. A body that does not parse makes no claim.
func lastAssistantMessage(body []byte) string {
	var p struct {
		LastAssistantMessage string `json:"last_assistant_message"`
	}
	if len(body) == 0 || json.Unmarshal(body, &p) != nil {
		return ""
	}
	return p.LastAssistantMessage
}

func (w *Watcher) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *Watcher) checkTimeout() time.Duration {
	if w.CheckTimeout > 0 {
		return w.CheckTimeout
	}
	return checks.DefaultTimeout
}

// storedResult is one claim's row in effect_checks.results_json.
type storedResult struct {
	Kind     Kind   `json:"kind"`
	Target   string `json:"target,omitempty"`
	Sentence string `json:"sentence"`
	Status   Status `json:"status"`
	Evidence string `json:"evidence"`
}

// evaluate runs one turn's check. Every early return is a deliberate no-op:
// a session without a project, a project set to off, a message already
// evaluated, or a session with no workdir has nothing this package can check.
func (w *Watcher) evaluate(sessionID int64, message, hash, key string, since time.Time) {
	defer func() {
		w.mu.Lock()
		delete(w.inflight, key)
		w.mu.Unlock()
	}()
	log := w.Log
	if log == nil {
		log = slog.Default()
	}
	ctx := context.Background()

	sess, err := w.DB.Session(sessionID)
	if err != nil || sess.ProjectID == nil {
		// A session with no project has no effect_checks setting. Checking it
		// would mean a silent default, so it is left alone.
		return
	}
	project, err := w.DB.Project(*sess.ProjectID)
	if err != nil {
		return
	}
	mode := ModeOf(project.EffectChecks)
	if mode == ModeOff {
		return
	}
	if dup, err := w.DB.HasEffectCheck(sessionID, hash); err != nil || dup {
		return
	}
	target, err := w.DB.Target(sess.TargetID)
	if err != nil {
		return
	}
	ex, err := w.Reg.For(target)
	if err != nil {
		return
	}
	dir, err := checks.SessionCheckWorkdir(sess)
	if err != nil {
		return
	}

	claims := Extract(message)
	for _, c := range claims {
		if c.Kind == KindTestsPass {
			// A "tests pass" claim is judged on the session's last recorded run.
			// If that run is still going, its result is the one that counts.
			w.awaitCheck(ctx, sessionID)
			break
		}
	}
	last, _ := w.DB.LatestSessionCheck(sessionID)
	fp, _ := checks.WorktreeFingerprint(ctx, ex, dir)
	results := Evaluate(ctx, ex, dir, message, Facts{Since: since, LastCheck: last, CurrentFingerprint: fp})

	row := &store.EffectCheck{SessionID: sessionID, MessageHash: hash}
	var contradicted []Result
	stored := make([]storedResult, 0, len(results))
	for _, r := range results {
		switch r.Verdict.Status {
		case StatusVerified:
			row.Verified++
		case StatusContradicted:
			row.Contradicted++
			contradicted = append(contradicted, r)
		default:
			row.Unknown++
		}
		stored = append(stored, storedResult{Kind: r.Claim.Kind, Target: r.Claim.Target,
			Sentence: r.Claim.Sentence, Status: r.Verdict.Status, Evidence: r.Verdict.Evidence})
	}
	raw, _ := json.Marshal(stored)
	row.Results = raw

	// In enforce mode a contradicted claim goes back to the agent first, and the
	// human hears about it only if that was not possible. The cap is checked
	// against enforced rows, so a notice that could not be delivered does not
	// count toward it.
	enforced := false
	if mode == ModeEnforce && len(contradicted) > 0 && w.Sessions != nil {
		if n, err := w.DB.CountEnforcedEffectChecks(sessionID, store.Now()-time.Hour.Seconds()); err == nil && n < enforceCapPerHour {
			if err := w.Sessions.SendNotice(ctx, sessionID, enforcementNotice(contradicted)); err != nil {
				log.Warn("could not send effect notice", "session", sessionID, "err", err)
			} else {
				enforced = true
				row.Enforced = 1
			}
		}
	}
	saved, err := w.DB.InsertEffectCheck(row)
	if err != nil {
		log.Warn("could not record effect check", "session", sessionID, "err", err)
		return
	}
	w.publish(sessionID, saved)
	if len(contradicted) > 0 && !enforced {
		w.notifyHuman(sess, contradicted)
	}
}

// awaitCheck waits while the session's latest check is running, for at most
// the check timeout. It returns as soon as that check is no longer running.
func (w *Watcher) awaitCheck(ctx context.Context, sessionID int64) {
	deadline := time.NewTimer(w.checkTimeout())
	defer deadline.Stop()
	tick := time.NewTicker(checkWaitPoll)
	defer tick.Stop()
	for {
		last, err := w.DB.LatestSessionCheck(sessionID)
		if err != nil || last == nil || last.Status != checks.StatusRunning {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
	}
}

// enforcementNotice is what the agent reads when a claim did not hold. It names
// each claim and its evidence, and says what to do: make the claim true or
// correct it. It does not ask the agent to argue.
func enforcementNotice(contradicted []Result) string {
	var b strings.Builder
	b.WriteString("Lectern checked the completion claims in your last message against the machine, and these did not hold:\n")
	for _, r := range contradicted {
		fmt.Fprintf(&b, "- %q: %s\n", r.Claim.Sentence, oneLine(r.Verdict.Evidence))
	}
	b.WriteString("\nFix the problem so the claim is true, or correct your report to say what is actually the case. " +
		"Do not repeat the claim until it holds.\n")
	return b.String()
}

// notifyHuman sends one push per contradicted claim, for show mode and for
// enforce mode when the agent could not be sent the notice.
func (w *Watcher) notifyHuman(sess *store.Session, contradicted []Result) {
	if w.Notifier == nil {
		return
	}
	url := fmt.Sprintf("/#session/%d", sess.ID)
	for _, r := range contradicted {
		body := fmt.Sprintf("A claim didn't check out: %s — %s", r.Claim.Sentence, oneLine(r.Verdict.Evidence))
		w.Notifier.Notify(sess.Name, body, url, nil)
	}
}

func (w *Watcher) publish(sessionID int64, c *store.EffectCheck) {
	if w.Bus == nil {
		return
	}
	w.Bus.Publish("board", "session.effects", c)
	w.Bus.Publish(fmt.Sprintf("session:%d", sessionID), "session.effects", c)
}

// oneLine collapses whitespace so evidence that spans lines reads as one item
// in a notice or a push.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
