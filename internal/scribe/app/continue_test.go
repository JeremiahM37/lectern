package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// An interrupted turn is a saved prompt with nothing after it. Continuing it
// runs the model again and writes the file once, without the prompt being
// saved a second time.
func TestContinueFinishesInterruptedTurnOnce(t *testing.T) {
	be, repo := testBackend(t)
	sb := be.(*scribeBackend)
	a := sb.current()

	// What a killed process leaves behind: the prompt, saved, no answer.
	prompt := llm.UserText("WRITE a greeting file")
	a.Agent.SetMessages([]llm.Message{prompt})
	if err := a.Store.ReplaceMessages(a.Meta.ID, []llm.Message{prompt}); err != nil {
		t.Fatal(err)
	}

	started, err := be.(tui.Continuer).Continue()
	if err != nil || !started {
		t.Fatalf("Continue = %v, %v; want started", started, err)
	}
	evs := waitTurnDone(t, be)
	waitIdle(t, be)
	if r := toolResult(evs, "write_file"); r == nil || r.IsError {
		t.Fatalf("write_file did not run after continue: %+v", r)
	}
	if got, err := os.ReadFile(filepath.Join(repo, "hello.txt")); err != nil || string(got) != "hi there\n" {
		t.Fatalf("hello.txt = %q, %v", got, err)
	}

	msgs, err := a.Store.LoadMessages(a.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, m := range msgs {
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), "WRITE") {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("the prompt must be saved once, found %d user messages in %d", users, len(msgs))
	}
	if last := msgs[len(msgs)-1]; last.Role != llm.RoleAssistant || last.Text() != "All done." {
		t.Fatalf("the saved turn should end with the answer, got %q", last.Text())
	}

	// Now the turn has an answer, so there is nothing to continue.
	started, err = be.(tui.Continuer).Continue()
	if err != nil || started {
		t.Fatalf("second Continue = %v, %v; want nothing to do", started, err)
	}
}

// A session whose last turn finished has nothing to continue, and asking
// does not start a turn.
func TestContinueReportsFinishedTurn(t *testing.T) {
	be, _ := testBackend(t)
	runTurn(t, be, "ok", nil)
	waitIdle(t, be)
	started, err := be.(tui.Continuer).Continue()
	if err != nil || started {
		t.Fatalf("Continue after a finished turn = %v, %v; want false, nil", started, err)
	}
	if be.Running() {
		t.Fatal("nothing should be running")
	}
}

// Shutdown aborts a turn that is still running and returns once it has
// finished saving; the session is then closed.
func TestShutdownAbortsAndCloses(t *testing.T) {
	be, _ := testBackend(t)
	runTurn(t, be, "ok", nil)
	be.Submit("ok", nil)
	deadline := time.Now().Add(10 * time.Second)
	for !be.Running() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	be.(tui.Stopper).Shutdown(10 * time.Second)
	if be.Running() {
		t.Fatal("Shutdown must leave no turn running")
	}
	if _, err := be.(tui.Continuer).Continue(); err == nil {
		t.Fatal("a shut down session must refuse to continue")
	}
}

// waitTurnDone reads events until the turn reports done.
func waitTurnDone(t *testing.T, be tui.Backend) []agent.Event {
	t.Helper()
	var evs []agent.Event
	deadline := time.After(30 * time.Second)
	for {
		select {
		case e := <-be.Events():
			evs = append(evs, e)
			if e.Kind == agent.EvTurnDone {
				return evs
			}
		case <-be.Prompts():
			t.Fatal("unexpected approval prompt")
		case <-deadline:
			t.Fatalf("turn did not finish; events %v", kinds(evs))
		}
	}
}

// waitIdle waits until the backend has cleared its turn. The turn reports
// done a moment before it is saved and marked finished.
func waitIdle(t *testing.T, be tui.Backend) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for be.Running() {
		if time.Now().After(deadline) {
			t.Fatal("backend still running")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
