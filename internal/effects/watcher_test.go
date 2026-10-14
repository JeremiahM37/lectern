package effects

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// captureSender records every notice typed into a session. failWith, when set,
// makes the send fail, as it does when the session has already ended.
type captureSender struct {
	mu       sync.Mutex
	notices  []string
	failWith error
}

func (c *captureSender) SendNotice(_ context.Context, _ int64, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failWith != nil {
		return c.failWith
	}
	c.notices = append(c.notices, text)
	return nil
}

func (c *captureSender) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.notices...)
}

// captureNotifier records every push the watcher sends.
type captureNotifier struct {
	mu     sync.Mutex
	bodies []string
}

func (c *captureNotifier) Notify(_, body, _ string, _ *sinks.Extra) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, body)
}

func (c *captureNotifier) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

// watcherFixture is a real store, a local target, and a project whose repo has
// one commit that was never pushed. Every probe therefore runs for real, and a
// "Pushed to origin/main." claim is contradicted by the repository itself.
type watcherFixture struct {
	db       *store.DB
	sess     *store.Session
	proj     *store.Project
	w        *Watcher
	sender   *captureSender
	notifier *captureNotifier
}

func newWatcherFixture(t *testing.T, mode string) *watcherFixture {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	work, _ := newRepo(t)
	commitFile(t, work, "local.txt", "not pushed")

	target, err := db.InsertTarget(&store.Target{Name: "local", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: work, EffectChecks: mode})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{ProjectID: &proj.ID, TargetID: target.ID,
		Name: "s", Agent: "claude", Model: "test-model", Workdir: work})
	if err != nil {
		t.Fatal(err)
	}
	fx := &watcherFixture{db: db, sess: sess, proj: proj, sender: &captureSender{}, notifier: &captureNotifier{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fx.w = New(db, executor.NewRegistry(false, 0), bus.New(), fx.notifier, fx.sender, 5*time.Second, log)
	return fx
}

// stopBody is the body a Stop hook carries for a final message. A string map
// cannot fail to marshal, so the error is not checked.
func stopBody(message string) []byte {
	b, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "last_assistant_message": message})
	return b
}

// waitUntil polls cond for up to 15 seconds. The watcher evaluates on a
// goroutine, so every assertion about its effects waits for them.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (fx *watcherFixture) rows(t *testing.T) []*store.EffectCheck {
	t.Helper()
	rows, err := fx.db.EffectChecks(fx.sess.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (fx *watcherFixture) stop(message string) {
	fx.w.HandleHookEvent(fx.sess, agentevents.EventStop, stopBody(message), "", false)
}

func TestWatcherEnforceSendsContradictedClaimToAgent(t *testing.T) {
	fx := newWatcherFixture(t, ModeEnforce)
	fx.stop("Pushed to origin/main.")

	waitUntil(t, "an effect row", func() bool { return len(fx.rows(t)) == 1 })
	row := fx.rows(t)[0]
	if row.Contradicted != 1 || row.Verified != 0 || row.Enforced != 1 {
		t.Fatalf("row = verified %d contradicted %d enforced %d; want 0/1/1", row.Verified, row.Contradicted, row.Enforced)
	}
	var results []storedResult
	if err := json.Unmarshal(row.Results, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Kind != KindPushed || results[0].Status != StatusContradicted {
		t.Fatalf("results = %+v; want one contradicted pushed claim", results)
	}
	notices := fx.sender.all()
	if len(notices) != 1 {
		t.Fatalf("agent got %d notices; want 1", len(notices))
	}
	if !strings.Contains(notices[0], "Pushed to origin/main.") || !strings.Contains(notices[0], "Fix the problem") {
		t.Fatalf("notice does not quote the claim and ask for a fix:\n%s", notices[0])
	}
	if got := fx.notifier.all(); len(got) != 0 {
		t.Fatalf("enforce sent the human %d pushes while the agent had the notice; want 0", len(got))
	}
}

func TestWatcherShowNotifiesHumanOnly(t *testing.T) {
	fx := newWatcherFixture(t, ModeShow)
	fx.stop("Pushed to origin/main.")

	waitUntil(t, "a push to the human", func() bool { return len(fx.notifier.all()) == 1 })
	if got := fx.notifier.all()[0]; !strings.HasPrefix(got, "A claim didn't check out: Pushed to origin/main.") {
		t.Fatalf("push body = %q", got)
	}
	if n := len(fx.sender.all()); n != 0 {
		t.Fatalf("show mode sent the agent %d notices; want 0", n)
	}
	row := fx.rows(t)[0]
	if row.Enforced != 0 || row.Contradicted != 1 {
		t.Fatalf("show row enforced=%d contradicted=%d; want 0/1", row.Enforced, row.Contradicted)
	}
}

func TestWatcherFallsBackToHumanWhenNoticeFails(t *testing.T) {
	fx := newWatcherFixture(t, ModeEnforce)
	fx.sender.failWith = errors.New("session has ended")
	fx.stop("Pushed to origin/main.")

	waitUntil(t, "a push to the human", func() bool { return len(fx.notifier.all()) == 1 })
	waitUntil(t, "an effect row", func() bool { return len(fx.rows(t)) == 1 })
	if row := fx.rows(t)[0]; row.Enforced != 0 {
		t.Fatalf("a notice that failed to send was counted as enforced")
	}
}

func TestWatcherDedupesByMessageHash(t *testing.T) {
	fx := newWatcherFixture(t, ModeEnforce)
	fx.stop("Pushed to origin/main.")
	waitUntil(t, "an effect row", func() bool { return len(fx.rows(t)) == 1 })

	// The same message again, as a repeated Stop with nothing new would send.
	fx.stop("Pushed to origin/main.")
	time.Sleep(300 * time.Millisecond)
	if n := len(fx.rows(t)); n != 1 {
		t.Fatalf("repeat of the same message made %d rows; want 1", n)
	}
	if n := len(fx.sender.all()); n != 1 {
		t.Fatalf("repeat of the same message sent %d notices; want 1", n)
	}
}

func TestWatcherCapsEnforcementPerHour(t *testing.T) {
	fx := newWatcherFixture(t, ModeEnforce)
	messages := []string{
		"Pushed to origin/main.",
		"Pushed to origin/main again.",
		"Pushed to origin/main a third time.",
		"Pushed to origin/main a fourth time.",
	}
	for i, m := range messages {
		fx.stop(m)
		want := i + 1
		waitUntil(t, "effect row "+m, func() bool { return len(fx.rows(t)) == want })
	}
	if n := len(fx.sender.all()); n != enforceCapPerHour {
		t.Fatalf("agent got %d notices; want the cap of %d", n, enforceCapPerHour)
	}
	// Past the cap the human hears about the claim instead, so it is not lost.
	waitUntil(t, "a push to the human", func() bool { return len(fx.notifier.all()) == 1 })
	rows := fx.rows(t)
	enforced := 0
	for _, r := range rows {
		enforced += r.Enforced
	}
	if enforced != enforceCapPerHour {
		t.Fatalf("%d rows marked enforced; want %d", enforced, enforceCapPerHour)
	}
}

func TestWatcherOffAndClaimlessMessagesAreSkipped(t *testing.T) {
	fx := newWatcherFixture(t, ModeOff)
	fx.stop("Pushed to origin/main.")
	fx.stop("I looked at the code and have no changes to report.")
	time.Sleep(300 * time.Millisecond)
	if n := len(fx.rows(t)); n != 0 {
		t.Fatalf("off mode stored %d rows; want 0", n)
	}
	if n := len(fx.sender.all()) + len(fx.notifier.all()); n != 0 {
		t.Fatalf("off mode sent %d messages; want 0", n)
	}
}

func TestWatcherSkipsSessionWithoutProject(t *testing.T) {
	fx := newWatcherFixture(t, ModeEnforce)
	sess, err := fx.db.InsertSession(&store.Session{TargetID: fx.sess.TargetID, Name: "adhoc",
		Agent: "claude", Workdir: fx.sess.Workdir})
	if err != nil {
		t.Fatal(err)
	}
	fx.w.HandleHookEvent(sess, agentevents.EventStop, stopBody("Pushed to origin/main."), "", false)
	time.Sleep(300 * time.Millisecond)
	if rows, _ := fx.db.EffectChecks(sess.ID, 10); len(rows) != 0 {
		t.Fatalf("a session with no project stored %d rows; want 0", len(rows))
	}
}

func TestModeOfDefaultsToShow(t *testing.T) {
	cases := map[string]string{"": ModeShow, "show": ModeShow, "off": ModeOff, "enforce": ModeEnforce, "bogus": ModeShow}
	for in, want := range cases {
		if got := ModeOf(in); got != want {
			t.Errorf("ModeOf(%q) = %q; want %q", in, got, want)
		}
	}
}
