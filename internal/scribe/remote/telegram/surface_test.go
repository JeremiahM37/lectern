package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fakeSink is a Scribe tab that records what the phone sends it.
type fakeSink struct {
	mu        sync.Mutex
	running   bool
	steerOK   bool
	submitted []string
	steered   []string
	aborts    int
}

func (f *fakeSink) Abort() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborts++
}

func (f *fakeSink) Submit(prompt string, _ []llm.Part) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitted = append(f.submitted, prompt)
}

func (f *fakeSink) Steer(msg string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.steerOK {
		return false
	}
	f.steered = append(f.steered, msg)
	return true
}

func (f *fakeSink) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *fakeSink) calls() (submitted, steered []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.submitted...), append([]string(nil), f.steered...)
}

// enabledSurface turns a fresh Surface on against the fake Bot API, with
// ownerChat already paired.
func enabledSurface(t *testing.T, tg *fakeTG) *Surface {
	t.Helper()
	s := &Surface{}
	err := s.Enable(EnableOptions{
		Token: testToken, APIBase: tg.srv.URL, Static: []string{"4242"},
		HTTP: tg.srv.Client(), QuestionWait: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Disable)
	return s
}

func TestPlainMessageFromPairedChatGoesToBoundTab(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{}
	s.Bind(sink)

	tg.push(message(ownerChat, "fix the failing test"))
	if m := tg.next(t); m.ChatID != ownerChat || m.Text != "Sent to Scribe." {
		t.Fatalf("reply = %q to %d", m.Text, m.ChatID)
	}
	submitted, steered := sink.calls()
	if len(submitted) != 1 || submitted[0] != "[via telegram] fix the failing test" {
		t.Fatalf("submitted = %q", submitted)
	}
	if len(steered) != 0 {
		t.Fatalf("an idle tab was steered: %q", steered)
	}
}

func TestPlainMessageWhileBusySteersTheTurn(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{running: true, steerOK: true}
	s.Bind(sink)

	tg.push(message(ownerChat, "use the sqlite driver"))
	if m := tg.next(t); m.Text != "Steering the running turn." {
		t.Fatalf("reply = %q", m.Text)
	}
	submitted, steered := sink.calls()
	if len(submitted) != 0 || len(steered) != 1 || steered[0] != "[via telegram] use the sqlite driver" {
		t.Fatalf("submitted = %q, steered = %q", submitted, steered)
	}
}

func TestMessageWithNoBoundTabSaysSo(t *testing.T) {
	tg := newFakeTG(t)
	enabledSurface(t, tg)

	tg.push(message(ownerChat, "hello"))
	if m := tg.next(t); !strings.Contains(m.Text, "No Scribe tab is bound") {
		t.Fatalf("reply = %q", m.Text)
	}
}

func TestSlashCommandsAreNotForwarded(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{}
	s.Bind(sink)

	tg.push(message(ownerChat, "/start"))
	if m := tg.next(t); !strings.Contains(m.Text, "Send a message") {
		t.Fatalf("/start reply = %q", m.Text)
	}
	tg.push(message(ownerChat, "/compact"))
	if m := tg.next(t); !strings.Contains(m.Text, "not available from Telegram") {
		t.Fatalf("/compact reply = %q", m.Text)
	}
	if submitted, steered := sink.calls(); len(submitted)+len(steered) != 0 {
		t.Fatalf("slash commands reached the tab: %q %q", submitted, steered)
	}
}

func TestOverlongMessageIsRefused(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{}
	s.Bind(sink)

	tg.push(message(ownerChat, strings.Repeat("x", maxMessage+1)))
	if m := tg.next(t); !strings.Contains(m.Text, "too long") {
		t.Fatalf("reply = %q", m.Text)
	}
	if submitted, _ := sink.calls(); len(submitted) != 0 {
		t.Fatal("an overlong message reached the tab")
	}
}

func TestTurnEndPostsOnlyFromBoundTab(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	bound, other := &fakeSink{}, &fakeSink{}
	s.Bind(bound)

	done := agent.Event{Kind: agent.EvTurnDone, Text: "Renamed the function."}
	s.Notify(other, done)
	tg.quiet(t, 300*time.Millisecond)

	s.Notify(bound, done)
	if m := tg.next(t); !strings.Contains(m.Text, "Renamed the function.") {
		t.Fatalf("turn-end note = %q", m.Text)
	}
	// A subagent's turn end is not the tab's answer.
	s.Notify(bound, agent.Event{Kind: agent.EvTurnDone, Text: "sub", AgentID: "a1"})
	tg.quiet(t, 300*time.Millisecond)
}

func TestNoteIsRedactedBeforeItLeaves(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	bound := &fakeSink{}
	s.Bind(bound)

	// A key-shaped string in the agent's text must not reach the chat.
	secret := "sk-ant-api03-" + strings.Repeat("A1b2", 6)
	s.Notify(bound, agent.Event{Kind: agent.EvTurnDone, Text: "token is " + secret})
	m := tg.next(t)
	if strings.Contains(m.Text, secret) {
		t.Fatalf("secret reached the chat: %q", m.Text)
	}
}

func TestHostWhileOffAnswersLocallyWithoutTelegram(t *testing.T) {
	s := &Surface{}
	inner := &recordingHost{decision: tools.AllowOnce, answer: "local"}
	h := s.Host(inner)

	if d := h.Approve(context.Background(), tools.Approval{Title: "x"}); d != tools.AllowOnce {
		t.Fatalf("approve = %v", d)
	}
	if a, err := h.Ask(context.Background(), tools.Question{Question: "q"}); err != nil || a != "local" {
		t.Fatalf("ask = %q, %v", a, err)
	}
	h.Progress("call", "line")
	if inner.progress != 1 {
		t.Fatalf("progress not passed through: %d", inner.progress)
	}
	if h.(interface{ Interactive() bool }).Interactive() != true {
		t.Fatal("a tab with an interactive local host should report interactive")
	}
}

func TestPairingNeedsTheSurfaceOn(t *testing.T) {
	s := &Surface{}
	if _, _, err := s.Pair(); err != ErrOff {
		t.Fatalf("pair while off = %v, want ErrOff", err)
	}
}

func TestPairMintsACodeWhenOn(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	code, expires, err := s.Pair()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 || expires.Before(time.Now()) {
		t.Fatalf("code %q expires %v", code, expires)
	}
}

// recordingHost is a local host that records what it was asked.
type recordingHost struct {
	decision tools.Decision
	answer   string
	progress int
}

func (r *recordingHost) Approve(context.Context, tools.Approval) tools.Decision { return r.decision }
func (r *recordingHost) Ask(context.Context, tools.Question) (string, error) {
	return r.answer, nil
}
func (r *recordingHost) Progress(string, string) { r.progress++ }
func (r *recordingHost) Interactive() bool       { return true }

func TestStopAbortsOnlyWhenSomethingRuns(t *testing.T) {
	tg := newFakeTG(t)
	s := enabledSurface(t, tg)
	sink := &fakeSink{running: true}
	s.Bind(sink)

	tg.push(message(ownerChat, "/stop"))
	if m := tg.next(t); !strings.Contains(m.Text, "Stopped the running turn") {
		t.Fatalf("/stop reply = %q", m.Text)
	}
	sink.mu.Lock()
	aborts := sink.aborts
	sink.running = false
	sink.mu.Unlock()
	if aborts != 1 {
		t.Fatalf("aborts = %d, want 1", aborts)
	}

	tg.push(message(ownerChat, "/stop"))
	if m := tg.next(t); m.Text != "Nothing is running." {
		t.Fatalf("idle /stop reply = %q", m.Text)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.aborts != 1 {
		t.Fatalf("an idle /stop aborted again: %d", sink.aborts)
	}
}
