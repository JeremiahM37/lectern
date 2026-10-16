package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestUnfinishedDetection(t *testing.T) {
	user := llm.UserText("fix the build")
	assistant := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: llm.PartText, Text: "Done."}}}
	toolRes := llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{Type: llm.PartToolResult, Text: "ok"}}}
	cases := []struct {
		name string
		msgs []llm.Message
		want bool
	}{
		{"empty history", nil, false},
		{"prompt with no answer", []llm.Message{user}, true},
		{"answered prompt", []llm.Message{user, assistant}, false},
		{"tool result with no answer", []llm.Message{user, assistant, toolRes}, true},
		{"steering note with no answer", []llm.Message{user, assistant, llm.UserText("<steering>x</steering>")}, true},
		{"answer after tool result", []llm.Message{user, assistant, toolRes, assistant}, false},
	}
	for _, c := range cases {
		if got := Unfinished(c.msgs); got != c.want {
			t.Errorf("%s: Unfinished = %v, want %v", c.name, got, c.want)
		}
	}
}

// Continue must not send the prompt a second time: the model sees exactly
// one user message, and the turn completes with the model's answer.
func TestContinueResendsNothingAndCompletes(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"Finished."}})))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	a.SetMessages([]llm.Message{llm.UserText("fix the build")})

	stats, err := a.Continue(context.Background())
	must(t, err)
	if stats.Error != "" || stats.Aborted {
		t.Fatalf("stats %+v", stats)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("want one request, got %d", len(reqs))
	}
	if n := len(msgsWithRole(reqs[0], "user")); n != 1 {
		t.Fatalf("the prompt must be sent once, the request had %d user messages", n)
	}
	if got := lastUserText(reqs[0]); got != "fix the build" {
		t.Fatalf("last user text %q", got)
	}
	if done := log.of(EvTurnDone); len(done) != 1 || done[0].Text != "Finished." {
		t.Fatalf("turn done events %+v", done)
	}
	if start := log.of(EvTurnStart); len(start) != 1 || start[0].Prompt != "" {
		t.Fatalf("continue should announce a turn with no prompt, got %+v", start)
	}
	if msgs := a.Messages(); len(msgs) != 2 || msgs[1].Text() != "Finished." {
		t.Fatalf("history should be prompt then answer, got %d messages", len(msgs))
	}
}

func TestContinueRefusesFinishedTurn(t *testing.T) {
	f := newFake(t)
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	a.SetMessages([]llm.Message{
		llm.UserText("hi"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: llm.PartText, Text: "hello"}}},
	})
	_, err := a.Continue(context.Background())
	if !errors.Is(err, ErrNothingToContinue) {
		t.Fatalf("err = %v, want ErrNothingToContinue", err)
	}
	if f.count() != 0 {
		t.Fatalf("a finished turn must not call the model")
	}
}
