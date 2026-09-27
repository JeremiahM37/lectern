package limits

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// accounts registers the default login plus the named extra ones for the
// fixture's claude session, in rotation order.
func (f *fixture) accounts(labels ...string) []*store.Account {
	f.t.Helper()
	out := []*store.Account{}
	for i, label := range append([]string{"Default"}, labels...) {
		dir := ""
		if i > 0 {
			dir = "/accounts/" + label
		}
		a, err := f.db.InsertAccount(&store.Account{TargetID: f.sess.TargetID, Agent: "claude", Label: label, Dir: dir})
		if err != nil {
			f.t.Fatal(err)
		}
		out = append(out, a)
	}
	f.driver.db = f.db
	return out
}

func (f *fixture) session() *store.Session {
	f.t.Helper()
	s, err := f.db.Session(f.sess.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func TestSwapMovesTheConversationToTheNextFreeAccount(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeSwap, Then: ModeWait})
	accts := f.accounts("work", "spare")
	// "work" is known to be limited until after this reset; "spare" is free.
	until := unix(f.now.Add(2 * time.Hour))
	f.db.MarkAccountLimited(accts[1].ID, unix(f.now), &until)
	tr := f.tracker()
	ctx := context.Background()

	tr.ObservePane(f.sess, limitedPane, false)
	if h := f.hold(); h.Policy != ModeSwap || h.State != StateWaiting {
		t.Fatalf("hold: %+v", h)
	}
	tr.Tick(ctx)
	tr.Tick(ctx) // a second tick while the swap runs must not start another
	tr.Wait()
	if len(f.driver.swaps) != 1 || f.driver.swaps[0] != accts[2].ID {
		t.Fatalf("swaps %v, want exactly one to spare (%d)", f.driver.swaps, accts[2].ID)
	}
	h := f.hold()
	if h.State != StateResuming || h.AccountFrom == nil || *h.AccountFrom != accts[0].ID ||
		h.AccountTo == nil || *h.AccountTo != accts[2].ID || h.Tries != 1 {
		t.Fatalf("after swap: %+v", h)
	}
	if f.driver.nudgeCount() != 1 || f.pushed("Swapped account") != 1 {
		t.Fatalf("nudges=%d swapped pushes=%d", f.driver.nudgeCount(), f.pushed("Swapped account"))
	}
	// The account that hit the limit is remembered as limited until its reset.
	dflt, _ := f.db.Account(accts[0].ID)
	if want := time.Date(2026, 9, 26, 15, 40, 0, 0, time.UTC); !fromUnix(dflt.LimitedUntil).Equal(want) {
		t.Fatalf("default limited until %v, want %v", fromUnix(dflt.LimitedUntil), want)
	}
	// The resumed agent works below the nudge: verified, and no second push.
	f.now = f.now.Add(5 * time.Second)
	tr.ObservePane(f.session(), afterNudgePane("✻ Reading handlers.go… (esc to interrupt)\n"), true)
	if h := f.hold(); h.State != StateResumed {
		t.Fatalf("resume not verified: %+v", h)
	}
	if f.pushed("Resumed") != 0 {
		t.Fatal("a swap should push once, when it happens")
	}
}

func TestSwapFallsBackWhenEveryAccountIsLimited(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeSwap, Then: ModeWait})
	accts := f.accounts("work")
	until := unix(f.now.Add(3 * time.Hour))
	f.db.MarkAccountLimited(accts[1].ID, unix(f.now), &until)
	tr := f.tracker()
	tr.ObservePane(f.sess, limitedPane, false)
	tr.Tick(context.Background())
	tr.Wait()
	if f.driver.swapCount() != 0 {
		t.Fatal("swapped to a limited account")
	}
	h := f.hold()
	if h.Policy != ModeWait || h.DueAt == nil || h.State != StateWaiting {
		t.Fatalf("fallback hold: %+v", h)
	}
	if f.pushed("No account to swap to") != 1 {
		t.Fatal("no fallback push")
	}
	// From here it is an ordinary wait: one nudge after the reset.
	f.now = time.Date(2026, 9, 26, 15, 41, 0, 0, time.UTC)
	tr.ObservePane(f.sess, limitedPane, false)
	tr.Tick(context.Background())
	if f.driver.nudgeCount() != 1 {
		t.Fatalf("%d nudges after fallback to wait", f.driver.nudgeCount())
	}
}

func TestSwapWithNoAccountsNotifies(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeSwap})
	tr := f.tracker()
	tr.ObservePane(f.sess, limitedPane, false)
	tr.Tick(context.Background())
	if h := f.hold(); h.Policy != ModeNotify || f.driver.swapCount() != 0 {
		t.Fatalf("hold %+v swaps %d", h, f.driver.swapCount())
	}
}

func TestFailedSwapFallsBack(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeSwap, Then: ModeNotify})
	f.accounts("work")
	f.driver.swapErr = errors.New("conversation not found in the current account")
	tr := f.tracker()
	tr.ObservePane(f.sess, limitedPane, false)
	tr.Tick(context.Background())
	tr.Wait()
	h := f.hold()
	if h.State != StateWaiting || h.Policy != ModeNotify || f.pushed("Account swap failed") != 1 {
		t.Fatalf("failed swap: %+v", h)
	}
	tr.Tick(context.Background())
	tr.Wait()
	if f.driver.swapCount() != 1 {
		t.Fatalf("%d swaps after a failure, want 1", f.driver.swapCount())
	}
}

// Two processes ticking the same hold: the compare-and-swap lets one swap.
func TestConcurrentTicksSwapOnce(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeSwap})
	f.accounts("work", "spare")
	a, b := f.tracker(), f.tracker()
	a.ObservePane(f.sess, limitedPane, false)
	b.ObservePane(f.sess, limitedPane, false)
	done := make(chan struct{})
	go func() { a.Tick(context.Background()); close(done) }()
	b.Tick(context.Background())
	<-done
	a.Wait()
	b.Wait()
	if f.driver.swapCount() != 1 || f.driver.nudgeCount() != 1 {
		t.Fatalf("swaps=%d nudges=%d, want 1 each", f.driver.swapCount(), f.driver.nudgeCount())
	}
}

// A restart after the agent was relaunched under the new account (the
// commit) but before the nudge: the new process nudges, it does not swap
// again.
func TestNoDoubleSwapAfterRestartPastTheCommit(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeSwap})
	accts := f.accounts("work")
	first := f.tracker()
	first.ObservePane(f.sess, limitedPane, false)
	h := f.hold()
	// What the old process left: swapping, and the row already on "work".
	f.db.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"state": StateSwapping,
		"account_from": accts[0].ID, "account_to": accts[1].ID, "tries": 1})
	f.db.Update("sessions", f.sess.ID, map[string]any{"account_id": accts[1].ID})
	f.db.Exec(`UPDATE limit_holds SET updated_at=updated_at-60 WHERE id=?`, h.ID)

	restarted := f.tracker()
	restarted.ObservePane(f.session(), "╭──────╮\n│ >    │\n╰──────╯\n", false)
	restarted.Tick(context.Background())
	if h := f.hold(); h.State != StateWaiting {
		t.Fatalf("interrupted swap not recovered: %+v", h)
	}
	restarted.Tick(context.Background())
	restarted.Wait()
	if f.driver.swapCount() != 0 || f.driver.nudgeCount() != 1 {
		t.Fatalf("after restart: swaps=%d nudges=%d, want 0 and 1", f.driver.swapCount(), f.driver.nudgeCount())
	}
	if h := f.hold(); h.State != StateResuming {
		t.Fatalf("hold: %+v", h)
	}
}

// A restart before the relaunch: the agent never moved, so the swap is tried
// again — once.
func TestInterruptedSwapBeforeTheCommitIsRetriedOnce(t *testing.T) {
	f := newFixture(t)
	f.policy(Policy{Mode: ModeSwap})
	accts := f.accounts("work")
	f.tracker().ObservePane(f.sess, limitedPane, false)
	h := f.hold()
	f.db.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"state": StateSwapping,
		"account_from": accts[0].ID, "account_to": accts[1].ID, "tries": 1})
	f.db.Exec(`UPDATE limit_holds SET updated_at=updated_at-60 WHERE id=?`, h.ID)

	restarted := f.tracker()
	restarted.ObservePane(f.sess, limitedPane, false)
	for i := 0; i < 3; i++ {
		restarted.Tick(context.Background())
		restarted.Wait()
	}
	if f.driver.swapCount() != 1 || f.driver.swaps[0] != accts[1].ID {
		t.Fatalf("swaps %v, want one retry to work", f.driver.swaps)
	}
	if h := f.hold(); h.State != StateResuming || h.Tries != 2 {
		t.Fatalf("hold: %+v", h)
	}
}

func TestChooseSwapToANamedAccount(t *testing.T) {
	f := newFixture(t)
	accts := f.accounts("work", "spare")
	tr := f.tracker()
	tr.ObservePane(f.sess, limitedPane, false)
	h := f.hold()
	if h.Policy != ModeNotify {
		t.Fatalf("default policy: %+v", h)
	}
	if _, err := tr.Choose(context.Background(), h.ID, ActionSwap, &Policy{AccountID: accts[0].ID}); err == nil {
		t.Fatal("swapping to the account that hit the limit was accepted")
	}
	if _, err := tr.Choose(context.Background(), h.ID, ActionSwap, &Policy{AccountID: accts[2].ID}); err != nil {
		t.Fatal(err)
	}
	tr.Wait()
	if len(f.driver.swaps) != 1 || f.driver.swaps[0] != accts[2].ID {
		t.Fatalf("swaps %v", f.driver.swaps)
	}
	if _, err := tr.Choose(context.Background(), h.ID, ActionSwap, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("second choice: %v", err)
	}
}

func TestSwapPolicyValidation(t *testing.T) {
	p := Policy{Mode: "SWAP"}
	if err := p.Validate(); err != nil || p.Then != ModeNotify || p.Secondary() != ModeNotify {
		t.Fatalf("%+v %v", p, err)
	}
	if err := (&Policy{Mode: ModeSwap, Then: ModeHandoff}).Validate(); err == nil {
		t.Fatal("swap then handoff without a fallback was accepted")
	}
	if err := (&Policy{Mode: ModeSwap, Then: ModeSwap}).Validate(); err == nil {
		t.Fatal("swap then swap was accepted")
	}
	q := Policy{Mode: ModeWait, Then: ModeHandoff}
	if err := q.Validate(); err != nil || q.Then != "" {
		t.Fatalf("then kept on a non-swap policy: %+v %v", q, err)
	}
}
