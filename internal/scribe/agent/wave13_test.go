package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// noticeHooks records the Notification texts a turn sends.
type noticeHooks struct {
	mu    sync.Mutex
	notes []string
}

func (*noticeHooks) PreTool(context.Context, string, json.RawMessage) string { return "" }
func (*noticeHooks) PostTool(context.Context, string, json.RawMessage, tools.Result) string {
	return ""
}
func (*noticeHooks) Prompt(context.Context, string) (string, string) { return "", "" }
func (*noticeHooks) Stop(context.Context, TurnStats)                 {}
func (h *noticeHooks) Notification(_ context.Context, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notes = append(h.notes, msg)
}

// A reply cut off by the output limit three times in a row ends the turn
// early. The user gets a notice and the Notification hook hears it; a reply
// that finishes normally gets neither.
func TestPrematureStopNoticeAndNotification(t *testing.T) {
	f := newFake(t)
	f.setPick(func(int, recordedReq) reply {
		return turn(sseTurn{text: []string{"half an answer"}, finish: "length"})
	})
	log := &eventLog{}
	hooks := &noticeHooks{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.Hooks = hooks
	a := New(opts)
	stats, err := a.Run(context.Background(), "explain", nil)
	if err != nil || stats.Error != "" {
		t.Fatalf("a premature stop is not an error: err=%v stats.Error=%q", err, stats.Error)
	}
	notes := log.of(EvNotice)
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "Stopped early") || !strings.Contains(notes[0].Text, "output limit") {
		t.Fatalf("want one premature-stop notice, got %+v", notes)
	}
	if len(hooks.notes) != 1 || !strings.Contains(hooks.notes[0], "Stopped early") {
		t.Fatalf("Notification hook got %q", hooks.notes)
	}
}

func TestCleanFinishSendsNoPrematureNotice(t *testing.T) {
	f := newFake(t)
	f.setPick(func(int, recordedReq) reply {
		return turn(sseTurn{text: []string{"done"}})
	})
	log := &eventLog{}
	hooks := &noticeHooks{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.Hooks = hooks
	a := New(opts)
	if _, err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if n := log.of(EvNotice); len(n) != 0 || len(hooks.notes) != 0 {
		t.Fatalf("clean turn sent notices: %+v %q", n, hooks.notes)
	}
}
