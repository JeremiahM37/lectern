package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// streamThenDrop answers with one text delta and then ends the connection
// with no finish reason, which the client reports as a transient stream error.
func streamThenDrop(text string) reply {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseLine(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}}))
	}
}

// A transient failure after answer text is resumed from that text: the retry
// asks the model to continue it, and the reader sees each word once (spec 03
// §2.7 step 4). Before, the retry streamed the whole answer again.
func TestTransientFailureResumesFromTheTextAlreadyShown(t *testing.T) {
	f := newFake(t)
	f.setPick(func(n int, req recordedReq) reply {
		switch n {
		case 0:
			return streamThenDrop("half the ")
		case 1:
			msgs := req.body.Messages
			if len(msgs) < 2 {
				t.Errorf("retry has %d messages", len(msgs))
				return providerError(500)
			}
			prev, last := msgs[len(msgs)-2], msgs[len(msgs)-1]
			if prev.Role != "assistant" || prev.text() != "half the " {
				t.Errorf("retry does not continue the shown text: previous message %s %q", prev.Role, prev.text())
			}
			if last.Role != "user" || last.text() != resumeText {
				t.Errorf("retry does not ask to continue: last message %s %q", last.Role, last.text())
			}
			return turn(sseTurn{text: []string{"answer"}})
		}
		t.Errorf("unexpected request #%d", n+1)
		return providerError(500)
	})
	log := &eventLog{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.RetryBase = 250 * time.Millisecond
	a := New(opts)
	if _, err := a.Run(context.Background(), "explain", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var shown strings.Builder
	for _, e := range log.of(EvText) {
		shown.WriteString(e.Text)
	}
	if shown.String() != "half the answer" {
		t.Fatalf("reader saw %q, want the answer once", shown.String())
	}
	msgs := a.Messages()
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleAssistant || last.Text() != "half the answer" {
		t.Fatalf("history ends with %+v, want the whole answer once", last)
	}
	if n := len(log.of(EvRetry)); n != 1 {
		t.Fatalf("EvRetry count = %d, want 1", n)
	}
}

// An abort during the wait before a transient retry keeps the text already
// shown in the history, as an abort during a stream does (spec 03 §2.3).
func TestAbortDuringRetryWaitKeepsTheTextShown(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, streamThenDrop("half of the answer")))
	log := &eventLog{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.RetryBase = 10 * time.Second
	a := New(opts)

	done := make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), "explain", nil)
		done <- err
	}()
	waitUntil(t, 5*time.Second, "the transient retry to be scheduled", func() bool { return len(log.of(EvRetry)) > 0 })
	a.Abort()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Abort")
	}
	msgs := a.Messages()
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleAssistant || last.Text() != "half of the answer" {
		t.Fatalf("history ends with %+v, want the partial answer kept", last)
	}
	if n := f.count(); n != 1 {
		t.Fatalf("model requests = %d, want 1 (the retry was cancelled before it was sent)", n)
	}
}

// A transient failure keeps the text of any reply that has some. A reply
// with no text is returned unchanged.
func TestPartialFailureResumesOnlyTextOnlyReplies(t *testing.T) {
	streamErr := errors.New("stream ended before the response finished")
	text := []llm.Part{{Type: llm.PartText, Text: "half"}}
	withCall := []llm.Part{
		{Type: llm.PartText, Text: "half"},
		{Type: llm.PartToolCall, ToolCallID: "c1", ToolName: "read", Args: []byte(`{}`)},
	}

	var pf *partialError
	if err := partialFailure(text, streamErr); !errors.As(err, &pf) || pf.partial != "half" {
		t.Fatalf("text-only reply: got %v, want a partialError keeping the text", err)
	}
	// A completed call is not run until the step returns, so its text is
	// kept and the retry asks for the call again.
	if err := partialFailure(withCall, streamErr); !errors.As(err, &pf) || pf.partial != "half" {
		t.Fatalf("a reply with a completed call: got %v, want a partialError keeping the text", err)
	}
	if err := partialFailure(nil, streamErr); errors.As(err, &pf) {
		t.Fatalf("an empty reply has nothing to resume, got %v", err)
	}
	// A failure that is not transient is returned as it is, even with text.
	if err := partialFailure(text, errors.New("invalid api key")); errors.As(err, &pf) {
		t.Fatalf("a permanent error must not be wrapped, got %v", err)
	}
}
