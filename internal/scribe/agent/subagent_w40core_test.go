package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// retryableStreamErr is a stream failure the classifier retries.
func retryableStreamErr() llm.Event {
	return llm.Event{Type: llm.EvError, Err: errors.New("stream ended before the response finished")}
}

// A transient failure after text and a completed tool call keeps the text:
// the retry continues it, and the reader does not see it streamed twice.
func TestTransientFailureAfterToolCallKeepsItsText(t *testing.T) {
	p := &scriptProv{scripts: []streamScript{
		streamOf(textEv("Let me read. "), toolStartEv("c1", "echo"), toolEndEv("c1", "echo", llm.ToolArgs("{}")), retryableStreamErr()),
		streamOf(textEv("Reading now."), finishEv(llm.FinishStop)),
	}}
	var runs int
	a, log := rsAgent(t, p, echoRegistry(&runs, "ok", nil), nil)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := p.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests=%d, want 2", len(reqs))
	}
	msgs := reqs[1].Messages
	n := len(msgs)
	if n < 2 || msgs[n-2].Role != llm.RoleAssistant || msgs[n-2].Text() != "Let me read. " ||
		msgs[n-1].Role != llm.RoleUser || msgs[n-1].Text() != "Continue." {
		t.Fatalf("retry request ends with %+v, want the kept text then \"Continue.\"", msgs[n-2:])
	}
	var streamed strings.Builder
	for _, e := range log.of(EvText) {
		streamed.WriteString(e.Text)
	}
	if got := streamed.String(); strings.Count(got, "Let me read. ") != 1 {
		t.Fatalf("kept text was streamed again: %q", got)
	}
	if runs != 0 {
		// The call was not run by the failed attempt, so it is not run twice.
		t.Fatalf("tool ran %d times during the failed attempt", runs)
	}
}

// When the retries run out, the text of the call that gave up is kept for
// the caller, and a run that ends with it reports it as partial.
func TestGivenUpPartialIsKeptForTheReport(t *testing.T) {
	p := &scriptProv{scripts: []streamScript{
		streamOf(textEv("half done"), retryableStreamErr()),
		streamOf(textEv("more"), retryableStreamErr()),
	}}
	a, _ := rsAgent(t, p, tools.NewRegistry(), func(o *Options) { o.MaxRetries = 1 })
	_, err := a.Run(context.Background(), "go", nil)
	if err == nil {
		t.Fatal("Run succeeded, want the retries to run out")
	}
	if got := a.Partial(); got != "half donemore" {
		t.Fatalf("Partial() = %q, want the text of the call that gave up", got)
	}
	report := SubagentReport(a, err)
	if !strings.HasPrefix(report, "Partial output") || !strings.Contains(report, "half donemore") {
		t.Fatalf("report = %q, want the partial output marked as partial", report)
	}
	if !SalvageSucceeds(err, report) {
		t.Fatal("a provider failure with partial text should count as salvaged")
	}
}

// A run that succeeded has no partial report, whatever a failed call left.
func TestSuccessfulRunReportsItsAnswerNotAPartial(t *testing.T) {
	p := &scriptProv{scripts: []streamScript{
		streamOf(textEv("final answer"), finishEv(llm.FinishStop)),
	}}
	a, _ := rsAgent(t, p, tools.NewRegistry(), nil)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a.Partial() != "" {
		t.Fatalf("Partial() = %q after a successful run", a.Partial())
	}
	if got := SubagentReport(a, nil); got != "final answer" {
		t.Fatalf("report = %q, want the final answer", got)
	}
}

func TestSalvageSucceedsOnlyWithTextAndNoCancel(t *testing.T) {
	provider := errors.New("provider: 503 overloaded")
	cases := []struct {
		name   string
		err    error
		report string
		want   bool
	}{
		{"failure with text", provider, "partial", true},
		{"failure with no text", provider, "", false},
		{"user cancel is not salvaged", errCancelledByUser, "partial", false},
		{"dispatch cancel is not salvaged", errDispatchCancelled, "partial", false},
		{"breaker is not salvaged", errBreakerTripped, "partial", false},
		{"context cancel is not salvaged", context.Canceled, "partial", false},
	}
	for _, c := range cases {
		if got := SalvageSucceeds(c.err, c.report); got != c.want {
			t.Errorf("%s: SalvageSucceeds = %v, want %v", c.name, got, c.want)
		}
	}
}

// Subagent step limit (spec 03 §3.6): max(300 s, tool timeout x 2.5), or 24 h
// when tool timeouts are off.
func TestSubagentStepTimeout(t *testing.T) {
	cases := []struct {
		tool, want time.Duration
	}{
		{0, 24 * time.Hour},
		{unlimitedToolTimeout, 24 * time.Hour},
		{2 * time.Minute, 5 * time.Minute},
		{10 * time.Minute, 25 * time.Minute},
	}
	for _, c := range cases {
		if got := SubagentStepTimeout(c.tool); got != c.want {
			t.Errorf("SubagentStepTimeout(%v) = %v, want %v", c.tool, got, c.want)
		}
	}
}
