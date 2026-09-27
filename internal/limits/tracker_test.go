package limits

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

type fakeDriver struct {
	mu       sync.Mutex
	nudges   []int64
	handoffs []string
	inFlight bool
	sendErr  error
}

func (f *fakeDriver) SendNudge(_ context.Context, id int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if text != NudgeText {
		panic("unexpected nudge text " + text)
	}
	f.nudges = append(f.nudges, id)
	return f.sendErr
}

func (f *fakeDriver) StartLimitHandoff(_ int64, agent, model string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handoffs = append(f.handoffs, agent+"/"+model)
	f.inFlight = true
	return nil
}

func (f *fakeDriver) InFlight(int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inFlight
}

func (f *fakeDriver) HandoffError(int64) string { return "the successor failed to start" }

func (f *fakeDriver) nudgeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.nudges)
}

type fixture struct {
	t      *testing.T
	db     *store.DB
	sess   *store.Session
	driver *fakeDriver
	now    time.Time
	pushes []string
	mu     sync.Mutex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{ProjectID: &project.ID, TargetID: target.ID,
		Name: "api-work", Agent: "claude", TmuxSession: "lec-s1", Workdir: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, db: db, sess: sess, driver: &fakeDriver{},
		now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
}

// tracker builds a tracker on the fixture's database, as a fresh process
// would: nothing in memory, everything read back from limit_holds.
func (f *fixture) tracker() *Tracker {
	db := f.db
	db.SetSetting("ntfy_server", "https://ntfy.example")
	db.SetSetting("ntfy_topic", "lec")
	n := &sinks.Notifier{DB: db, BaseURL: "http://lectern", Log: slog.Default(),
		Hook: func(p []sinks.Payload) {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, pay := range p {
				title, _ := pay.Body["title"].(string)
				f.pushes = append(f.pushes, strings.TrimPrefix(title, "lectern: "))
			}
		}}
	return &Tracker{DB: db, Notifier: n, Sessions: f.driver, Timing: DefaultTiming,
		Clock: func() time.Time { return f.now }, Rand: func() float64 { return 0 }}
}

func (f *fixture) policy(p Policy) {
	f.t.Helper()
	raw, _ := json.Marshal(p)
	if err := f.db.SetLimitPolicyJSON(ScopeProject, *f.sess.ProjectID, string(raw)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) hold() *store.LimitHold {
	f.t.Helper()
	var id int64
	f.db.QueryRow(`SELECT id FROM limit_holds WHERE session_id=? ORDER BY id DESC LIMIT 1`, f.sess.ID).Scan(&id)
	h, err := f.db.LimitHold(id)
	if err != nil {
		f.t.Fatalf("no hold: %v", err)
	}
	return h
}

func (f *fixture) pushed(title string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.pushes {
		if p == title {
			n++
		}
	}
	return n
}

const limitedPane = "✻ Working on the API…\n  ⎿  You've hit your session limit · resets 3:40pm (UTC)\n\n╭──────╮\n│ >    │\n╰──────╯\n"

func afterNudgePane(tail string) string {
	return limitedPane + "❯ " + NudgeText + "\n" + tail
}

func TestPaneLimitOpensOneHoldAndNotifiesOnce(t *testing.T) {
	f := newFixture(t)
	tr := f.tracker()
	tr.ObservePane(f.sess, limitedPane, false)
	tr.ObservePane(f.sess, limitedPane, false)
	h := f.hold()
	if h.State != StateWaiting || h.Policy != ModeNotify || h.Source != "pane" || h.DueAt != nil {
		t.Fatalf("hold: %+v", h)
	}
	if want := time.Date(2026, 9, 26, 15, 40, 0, 0, time.UTC); !fromUnix(h.ResetAt).Equal(want) {
		t.Fatalf("reset %v, want %v", fromUnix(h.ResetAt), want)
	}
	if n := f.pushed("Usage limit"); n != 1 {
		t.Fatalf("%d limit pushes, want 1", n)
	}
	// notify never types into the session, even long after the reset — it
	// tells the operator once that the window has reset.
	f.now = f.now.Add(6 * time.Hour)
	tr.Tick(context.Background())
	tr.Tick(context.Background())
	if f.driver.nudgeCount() != 0 || f.pushed("Limit reset") != 1 {
		t.Fatalf("notify policy: nudges=%d reset pushes=%d", f.driver.nudgeCount(), f.pushed("Limit reset"))
	}
}

func TestBusyPaneIsNotALimit(t *testing.T) {
	f := newFixture(t)
	f.tracker().ObservePane(f.sess, limitedPane+"✻ Thinking… (esc to interrupt)\n", true)
	if _, err := f.db.OpenLimitHoldForSession(f.sess.ID); err == nil {
		t.Fatal("a limit line above a working agent opened a hold")
	}
}

func TestWaitPolicyNudgesOnceAtResetAndVerifiesResume(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeWait})
	tr := f.tracker()
	ctx := context.Background()
	tr.ObservePane(f.sess, limitedPane, false)
	h := f.hold()
	if want := time.Date(2026, 9, 26, 15, 40, 45, 0, time.UTC); !fromUnix(h.DueAt).Equal(want) {
		t.Fatalf("due %v, want reset + grace %v", fromUnix(h.DueAt), want)
	}
	tr.Tick(ctx)
	if f.driver.nudgeCount() != 0 {
		t.Fatal("nudged before the reset")
	}
	f.now = time.Date(2026, 9, 26, 15, 41, 0, 0, time.UTC)
	tr.ObservePane(f.sess, limitedPane, false)
	tr.Tick(ctx)
	tr.Tick(ctx)
	if f.driver.nudgeCount() != 1 {
		t.Fatalf("%d nudges, want exactly 1", f.driver.nudgeCount())
	}
	if h := f.hold(); h.State != StateResuming || h.Tries != 1 {
		t.Fatalf("after nudge: %+v", h)
	}
	// The pane shows the nudge and the agent working below it.
	f.now = f.now.Add(5 * time.Second)
	tr.ObservePane(f.sess, afterNudgePane("✻ Reading handlers.go… (esc to interrupt)\n"), true)
	if h := f.hold(); h.State != StateResumed || h.ResolvedAt == nil {
		t.Fatalf("resume not verified: %+v", h)
	}
	if f.pushed("Resumed") != 1 {
		t.Fatal("no resumed push")
	}
}

func TestStillLimitedAfterNudgeBacksOffThenGivesUp(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeWait})
	tr := f.tracker()
	tr.Timing.MaxTries = 2
	ctx := context.Background()
	tr.ObservePane(f.sess, limitedPane, false)
	f.now = time.Date(2026, 9, 26, 15, 41, 0, 0, time.UTC)
	tr.Tick(ctx)
	if f.driver.nudgeCount() != 1 {
		t.Fatal("no first nudge")
	}
	// The same limit comes straight back, now naming a later reset.
	f.now = f.now.Add(3 * time.Second)
	tr.ObservePane(f.sess, afterNudgePane("  ⎿  You've hit your session limit · resets 8pm (UTC)\n"), false)
	h := f.hold()
	if h.State != StateWaiting || h.Tries != 1 {
		t.Fatalf("still-limited hold: %+v", h)
	}
	if want := time.Date(2026, 9, 26, 20, 0, 45, 0, time.UTC); !fromUnix(h.DueAt).Equal(want) {
		t.Fatalf("rescheduled for %v, want %v", fromUnix(h.DueAt), want)
	}
	tr.Tick(ctx)
	if f.driver.nudgeCount() != 1 {
		t.Fatal("nudged again before the new reset")
	}
	f.now = time.Date(2026, 9, 26, 20, 1, 0, 0, time.UTC)
	tr.Tick(ctx)
	if f.driver.nudgeCount() != 2 {
		t.Fatal("no second nudge at the new reset")
	}
	f.now = f.now.Add(3 * time.Second)
	tr.ObservePane(f.sess, afterNudgePane("  ⎿  You've hit your weekly limit · resets Oct 3, 9am (UTC)\n"), false)
	if h := f.hold(); h.State != StateGaveUp {
		t.Fatalf("after max tries: %+v", h)
	}
	if f.pushed("Could not resume") != 1 {
		t.Fatal("no give-up push")
	}
}

// The core restart guarantee: a nudge is recorded before it is sent, so a
// second process — or the same one after a restart — verifies the first
// nudge instead of sending another.
func TestNoDoubleResumeAfterRestart(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeWait})
	ctx := context.Background()
	before := f.tracker()
	before.ObservePane(f.sess, limitedPane, false)
	f.now = time.Date(2026, 9, 26, 15, 41, 0, 0, time.UTC)
	before.Tick(ctx)
	if f.driver.nudgeCount() != 1 {
		t.Fatal("no nudge before the restart")
	}

	after := f.tracker() // a restarted Lectern: nothing in memory
	after.Tick(ctx)      // no pane seen yet
	after.ObservePane(f.sess, afterNudgePane("❯ \n"), false)
	f.now = f.now.Add(30 * time.Second)
	after.Tick(ctx)
	f.now = f.now.Add(2 * time.Minute) // past the settle window: no limit followed the nudge
	after.ObservePane(f.sess, afterNudgePane("I'll pick up the handler refactor.\n❯ \n"), false)
	after.Tick(ctx)
	if n := f.driver.nudgeCount(); n != 1 {
		t.Fatalf("%d nudges across the restart, want 1", n)
	}
	if h := f.hold(); h.State != StateResumed {
		t.Fatalf("restarted tracker did not verify the resume: %+v", h)
	}
}

// Two processes reaching the same due hold at once: the compare-and-swap lets
// exactly one of them type.
func TestConcurrentTicksNudgeOnce(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeWait})
	f.tracker().ObservePane(f.sess, limitedPane, false)
	f.now = time.Date(2026, 9, 26, 15, 41, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		tr := f.tracker()
		tr.ObservePane(f.sess, limitedPane, false)
		wg.Add(1)
		go func() { defer wg.Done(); tr.Tick(context.Background()) }()
	}
	wg.Wait()
	if n := f.driver.nudgeCount(); n != 1 {
		t.Fatalf("%d nudges from concurrent ticks, want 1", n)
	}
}

// A nudge recorded but never delivered (a crash between the two) is retried —
// that is not a double resume, because nothing reached the session.
func TestUndeliveredNudgeIsRetried(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeWait})
	tr := f.tracker()
	tr.ObservePane(f.sess, limitedPane, false)
	h := f.hold()
	nudged := unix(time.Date(2026, 9, 26, 15, 41, 0, 0, time.UTC))
	f.db.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"state": StateResuming, "nudged_at": nudged, "tries": 1})
	f.now = time.Date(2026, 9, 26, 15, 44, 0, 0, time.UTC)
	restarted := f.tracker()
	restarted.ObservePane(f.sess, limitedPane, false) // no nudge in the pane
	restarted.Tick(context.Background())
	if h := f.hold(); h.State != StateWaiting {
		t.Fatalf("undelivered nudge not rescheduled: %+v", h)
	}
	restarted.Tick(context.Background())
	if f.driver.nudgeCount() != 1 {
		t.Fatalf("%d nudges, want the one retry", f.driver.nudgeCount())
	}
}

func TestCLIAutoContinueIsLeftToTheCLI(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeWait})
	tr := f.tracker()
	banner := "  ⎿  You've hit your session limit · resets 3pm (UTC)\nUsage limit reached · continuing automatically at 3pm · esc to cancel\n"
	tr.ObservePane(f.sess, banner, false)
	f.now = time.Date(2026, 9, 26, 15, 1, 0, 0, time.UTC)
	tr.ObservePane(f.sess, banner, false)
	tr.Tick(context.Background())
	if f.driver.nudgeCount() != 0 {
		t.Fatal("typed into a CLI that is continuing by itself")
	}
	f.now = f.now.Add(20 * time.Second)
	tr.ObservePane(f.sess, "✻ Continuing… (esc to interrupt)\n", true)
	if h := f.hold(); h.State != StateCleared {
		t.Fatalf("CLI's own resume not recognised: %+v", h)
	}
	if f.driver.nudgeCount() != 0 {
		t.Fatal("nudged anyway")
	}
}

func TestHandoffPolicyStartsOneHandoffAndReportsFailure(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeHandoff, FallbackAgent: "codex", FallbackModel: "gpt-5"})
	tr := f.tracker()
	ctx := context.Background()
	tr.ObservePane(f.sess, limitedPane, false)
	tr.Tick(ctx)
	tr.Tick(ctx)
	if len(f.driver.handoffs) != 1 || f.driver.handoffs[0] != "codex/gpt-5" {
		t.Fatalf("handoffs: %v", f.driver.handoffs)
	}
	if h := f.hold(); h.State != StateHandingOff {
		t.Fatalf("hold: %+v", h)
	}
	// The handoff ended without a successor (its launch failed).
	f.driver.inFlight = false
	f.now = f.now.Add(time.Minute)
	f.db.Exec(`UPDATE limit_holds SET updated_at=updated_at-60`)
	tr.Tick(ctx)
	h := f.hold()
	if h.State != StateWaiting || h.Policy != ModeNotify || !strings.Contains(h.Note, "successor failed") {
		t.Fatalf("failed handoff: %+v", h)
	}
	if f.pushed("Handoff failed") != 1 || len(f.driver.handoffs) != 1 {
		t.Fatalf("pushes=%v handoffs=%v", f.pushes, f.driver.handoffs)
	}
}

func TestStopFailureHookAndResumeEvidence(t *testing.T) {
	f := newFixture(t)
	tr := f.tracker()
	transient := `{"hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"API Error: Rate limit reached"}`
	tr.HandleHookEvent(f.sess, agentevents.EventStopFailure, []byte(transient), "error", true)
	if _, err := f.db.OpenLimitHoldForSession(f.sess.ID); err == nil {
		t.Fatal("a transient 429 opened a usage-limit hold")
	}
	real := `{"hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your weekly limit · resets Oct 3, 9am (UTC)"}`
	tr.HandleHookEvent(f.sess, agentevents.EventStopFailure, []byte(real), "error", true)
	h := f.hold()
	if h.Source != "hook" || !fromUnix(h.ResetAt).Equal(time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("hook hold: %+v", h)
	}
	// Resuming: a tool call after the nudge is proof the agent is back.
	f.db.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"state": StateResuming, "nudged_at": unix(f.now)})
	f.now = f.now.Add(10 * time.Second)
	tr.HandleHookEvent(f.sess, agentevents.EventPreToolUse, []byte(`{}`), "working", true)
	if h := f.hold(); h.State != StateResumed {
		t.Fatalf("tool hook did not confirm the resume: %+v", h)
	}
}

func TestStatuslineSuppliesAMissingReset(t *testing.T) {
	f := newFixture(t)
	reset := float64(time.Date(2026, 9, 26, 17, 0, 0, 0, time.UTC).Unix())
	pct := 100
	f.sess.Rate5hPct, f.sess.Rate5hReset = &pct, &reset
	f.tracker().ObservePane(f.sess, "  ⎿  Claude AI usage limit reached\n", false) // no reset in the text, no match
	if _, err := f.db.OpenLimitHoldForSession(f.sess.ID); err == nil {
		t.Fatal("unrecognised text opened a hold")
	}
	f.tracker().HandleHookEvent(f.sess, agentevents.EventStopFailure,
		[]byte(`{"error":"rate_limit","last_assistant_message":"Request rejected (429)"}`), "error", true)
	if h := f.hold(); fromUnix(h.ResetAt).Unix() != int64(reset) {
		t.Fatalf("statusline reset not used: %+v", h)
	}
}

func TestChooseFromCardOrPush(t *testing.T) {
	f := newFixture(t)
	tr := f.tracker()
	ctx := context.Background()
	tr.ObservePane(f.sess, limitedPane, false)
	h := f.hold()
	if _, err := tr.Choose(ctx, h.ID, ActionHandoff, nil); err == nil {
		t.Fatal("handoff with no fallback anywhere was accepted")
	}
	got, err := tr.Choose(ctx, h.ID, ActionWait, nil)
	if err != nil || got.Policy != ModeWait || got.DueAt == nil {
		t.Fatalf("wait: %+v %v", got, err)
	}
	got, err = tr.Choose(ctx, h.ID, ActionHandoff, &Policy{FallbackAgent: "gemini"})
	if err != nil || got.State != StateHandingOff || f.driver.handoffs[0] != "gemini/" {
		t.Fatalf("handoff: %+v %v %v", got, err, f.driver.handoffs)
	}
	if _, err := tr.Choose(ctx, h.ID, ActionWait, nil); err != ErrConflict {
		t.Fatalf("a second choice during the handoff: %v", err)
	}
	got, err = tr.Choose(ctx, h.ID, ActionDismiss, nil)
	if err != nil || got.State != StateDismissed {
		t.Fatalf("dismiss: %+v %v", got, err)
	}
}

func TestDueAtBacksOffAndCaps(t *testing.T) {
	tm := DefaultTiming
	now := time.Unix(1_000_000, 0)
	zero := func() float64 { return 0 }
	if got := tm.DueAt(time.Time{}, 0, now, zero); got.Sub(now) != 10*time.Minute {
		t.Fatalf("first backoff %v", got.Sub(now))
	}
	if got := tm.DueAt(time.Time{}, 2, now, zero); got.Sub(now) != 40*time.Minute {
		t.Fatalf("third backoff %v", got.Sub(now))
	}
	if got := tm.DueAt(time.Time{}, 10, now, zero); got.Sub(now) != 2*time.Hour {
		t.Fatalf("capped backoff %v", got.Sub(now))
	}
	if got := tm.DueAt(now.Add(time.Hour), 0, now, func() float64 { return 1 }); got.Sub(now) != time.Hour+45*time.Second+90*time.Second {
		t.Fatalf("reset + grace + full jitter %v", got.Sub(now))
	}
}

// After a hold is resolved its message is often still on screen. That is
// the same stop, not a new one — reopening it would nudge a working agent.
func TestResolvedLimitStillOnScreenIsNotReopened(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeWait})
	tr := f.tracker()
	tr.ObservePane(f.sess, limitedPane, false)
	h := f.hold()
	if _, err := tr.Choose(context.Background(), h.ID, ActionDismiss, nil); err != nil {
		t.Fatal(err)
	}
	tr.ObservePane(f.sess, limitedPane, false)
	if _, err := f.db.OpenLimitHoldForSession(f.sess.ID); err == nil {
		t.Fatal("a dismissed limit came back from the same screen")
	}
	// Resumed, with the old message still in the tail above the nudge.
	tr.ObservePane(f.sess, afterNudgePane("Done with the handler.\n❯ \n"), false)
	if _, err := f.db.OpenLimitHoldForSession(f.sess.ID); err == nil {
		t.Fatal("the message above the nudge opened a hold")
	}
	// A genuinely new limit (a new reset) does open one.
	tr.ObservePane(f.sess, afterNudgePane("  ⎿  You've hit your weekly limit · resets Oct 3, 9am (UTC)\n"), false)
	if h2 := f.hold(); h2.ID == h.ID || !h2.Open() {
		t.Fatalf("new limit not detected: %+v", h2)
	}
}
