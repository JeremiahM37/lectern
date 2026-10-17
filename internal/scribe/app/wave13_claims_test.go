package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// spyHooks records which optional observer calls reached the inner chain.
type spyHooks struct {
	pre, post, failure, notes int
}

func (*spyHooks) PreTool(context.Context, string, json.RawMessage) string { return "" }
func (*spyHooks) PostTool(context.Context, string, json.RawMessage, tools.Result) string {
	return ""
}
func (*spyHooks) Prompt(context.Context, string) (string, string) { return "", "" }
func (*spyHooks) Stop(context.Context, agent.TurnStats)           {}
func (s *spyHooks) PreCompact(context.Context)                    { s.pre++ }
func (s *spyHooks) PostCompact(context.Context)                   { s.post++ }
func (s *spyHooks) Notification(context.Context, string)          { s.notes++ }
func (s *spyHooks) PostToolFailure(context.Context, string, json.RawMessage, tools.Result) string {
	s.failure++
	return "failure note"
}

// The agent finds optional hook observers by type assertion on its own Hooks,
// which in the app is the claims wrapper. The wrapper must forward them.
func TestClaimHooksForwardObserverEvents(t *testing.T) {
	spy := &spyHooks{}
	h := claimHooks{inner: spy, reg: newClaimRegistry(time.Now, time.Minute), owner: "a", workdir: t.TempDir()}
	var w agent.Hooks = h
	w.(interface{ PreCompact(context.Context) }).PreCompact(context.Background())
	w.(interface{ PostCompact(context.Context) }).PostCompact(context.Background())
	w.(interface{ Notification(context.Context, string) }).Notification(context.Background(), "x")
	got := w.(interface {
		PostToolFailure(context.Context, string, json.RawMessage, tools.Result) string
	}).PostToolFailure(context.Background(), "shell", nil, tools.Result{})
	if spy.pre != 1 || spy.post != 1 || spy.notes != 1 || spy.failure != 1 || got != "failure note" {
		t.Fatalf("forwarded calls pre=%d post=%d notes=%d failure=%d text=%q", spy.pre, spy.post, spy.notes, spy.failure, got)
	}
}

// Another tab's new claim reaches this tab's next step once, not on every step.
func TestStepNoticeReportsNewClaimsOnce(t *testing.T) {
	dir := t.TempDir()
	reg := newClaimRegistry(time.Now, time.Minute)
	mine := claimHooks{reg: reg, owner: "tab-a", workdir: dir}
	if note := mine.StepNotice(context.Background(), 2); note != "" {
		t.Fatalf("nothing claimed yet, got %q", note)
	}
	reg.claim(filepath.Join(dir, "main.go"), "tab-b", "tab B")
	note := mine.StepNotice(context.Background(), 3)
	if !strings.Contains(note, "main.go") {
		t.Fatalf("new claim not reported: %q", note)
	}
	if again := mine.StepNotice(context.Background(), 4); again != "" {
		t.Fatalf("claim repeated on the next step: %q", again)
	}
	mine.Prompt(context.Background(), "next turn")
	reg.turnStarted("tab-a")
	if note := mine.StepNotice(context.Background(), 2); !strings.Contains(note, "main.go") {
		t.Fatalf("a new turn should hear about live claims again: %q", note)
	}
}
