package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestContextLengthErrorReadsProviderWording(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		window int
		long   bool
	}{
		{"openai states the window", &llm.APIError{Provider: "openai", StatusCode: 400, Type: "invalid_request_error",
			Message: "This model's maximum context length is 128000 tokens. However, your messages resulted in 150321 tokens."}, 128000, true},
		{"openai with thousands separators", &llm.APIError{Provider: "openai", StatusCode: 400,
			Message: "maximum context length is 32,768 tokens"}, 32768, true},
		{"anthropic states the limit after >", &llm.APIError{Provider: "anthropic", StatusCode: 400, Type: "invalid_request_error",
			Message: "prompt is too long: 201000 tokens > 200000 maximum"}, 200000, true},
		{"ollama names no number", &llm.APIError{Provider: "ollama", StatusCode: 400,
			Message: "input length exceeds the context length"}, 0, true},
		{"a context_length_exceeded type counts", &llm.APIError{Provider: "gw", StatusCode: 400,
			Type: "context_length_exceeded", Message: "too much input"}, 0, true},
		{"an implausible number is not believed", &llm.APIError{Provider: "openai", StatusCode: 400,
			Message: "maximum context length is 12 tokens"}, 0, true},
		{"output limits are not input limits", &llm.APIError{Provider: "anthropic", StatusCode: 400,
			Message: "max_tokens: 9000 > 4096, which is the maximum allowed number of output tokens"}, 0, false},
		{"a rate limit is not a context error", &llm.APIError{Provider: "openai", StatusCode: 429,
			Message: "rate limit reached"}, 0, false},
		{"plain errors are not provider refusals", errors.New("maximum context length is 1000 tokens"), 0, false},
		{"nil is not an error", nil, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, long := contextLengthError(c.err)
			if w != c.window || long != c.long {
				t.Fatalf("contextLengthError = (%d, %v), want (%d, %v)", w, long, c.window, c.long)
			}
		})
	}
}

func TestNextWindowOnlyEverShrinks(t *testing.T) {
	cases := []struct {
		name            string
		current, stated int
		want            int
	}{
		{"a stated smaller window is taken", 128000, 100000, 100000},
		{"a stated window that is not smaller is not believed", 128000, 200000, 96000},
		{"no stated window steps down by a quarter", 128000, 0, 96000},
		{"an unknown window with a stated one takes the stated one", 0, 4000, 4000},
		{"an unknown window with no stated one learns nothing", 0, 0, 0},
		{"a step down below the floor learns nothing", 1200, 0, 0},
		{"a step down that stays above the floor is taken", 1500, 0, 1125},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextWindow(c.current, c.stated); got != c.want {
				t.Fatalf("nextWindow(%d, %d) = %d, want %d", c.current, c.stated, got, c.want)
			}
		})
	}
}

// A refusal that states the window: the agent learns it, compacts once,
// retries the step once and completes the turn.
func TestContextRefusalLearnsWindowCompactsAndRetries(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		apiError(400, "", `{"error":{"message":"This model's maximum context length is 4000 tokens. However, your messages resulted in 9000 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`),
		turn(sseTurn{text: []string{"done"}}),
	))
	log := &eventLog{}
	cc := &countingCompactor{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.Compactor = cc
	a := New(opts)
	a.msgs = bigTranscript()

	if _, err := a.Run(context.Background(), "keep going", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := f.count(); n != 2 {
		t.Fatalf("model requests = %d, want 2 (refused, then retried once)", n)
	}
	if cc.n != 1 {
		t.Fatalf("compactions = %d, want 1", cc.n)
	}
	if w := a.CurrentModel().Info.ContextWindow; w != 4000 {
		t.Fatalf("window after learning = %d, want 4000", w)
	}
	if !noticeMentions(log, "4000 tokens") {
		t.Fatalf("no notice names the learned window; notices: %v", noticeTexts(log))
	}
}

// A refusal with no number steps the window down and still retries only once.
func TestContextRefusalWithoutNumberStepsDown(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		apiError(400, "", `{"error":{"message":"input length exceeds the context length"}}`),
		turn(sseTurn{text: []string{"done"}}),
	))
	log := &eventLog{}
	cc := &countingCompactor{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.Compactor = cc
	a := New(opts)
	a.msgs = bigTranscript()

	if _, err := a.Run(context.Background(), "keep going", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if w := a.CurrentModel().Info.ContextWindow; w != priced.ContextWindow*3/4 {
		t.Fatalf("window after step down = %d, want %d", w, priced.ContextWindow*3/4)
	}
	if f.count() != 2 || cc.n != 1 {
		t.Fatalf("requests = %d, compactions = %d; want 2 and 1", f.count(), cc.n)
	}
}

// A provider that refuses every request is retried once, not in a loop.
func TestContextRefusalNeverLoops(t *testing.T) {
	f := newFake(t)
	refused := apiError(400, "", `{"error":{"message":"prompt is too long: 300000 tokens > 200000 maximum"}}`)
	f.setPick(func(int, recordedReq) reply { return refused })
	log := &eventLog{}
	cc := &countingCompactor{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.Compactor = cc
	a := New(opts)
	a.msgs = bigTranscript()

	if _, err := a.Run(context.Background(), "keep going", nil); err == nil {
		t.Fatal("Run succeeded against a provider that always refuses")
	}
	if n := f.count(); n != 2 {
		t.Fatalf("model requests = %d, want 2", n)
	}
	if cc.n != 1 {
		t.Fatalf("compactions = %d, want 1", cc.n)
	}
}

// With nothing to compact with, the step is not retried, but the window is
// still learned for the next turn.
func TestContextRefusalWithoutCompactorDoesNotRetry(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		apiError(400, "", `{"error":{"message":"maximum context length is 4000 tokens"}}`),
	))
	log := &eventLog{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	a := New(opts)
	a.msgs = bigTranscript()

	if _, err := a.Run(context.Background(), "keep going", nil); err == nil {
		t.Fatal("Run succeeded although the step could not be retried")
	}
	if n := f.count(); n != 1 {
		t.Fatalf("model requests = %d, want 1", n)
	}
	if w := a.CurrentModel().Info.ContextWindow; w != 4000 {
		t.Fatalf("window = %d, want 4000 learned for next time", w)
	}
}

// The learned window is what compaction thresholds use.
func TestLearnedWindowDrivesCompactionThreshold(t *testing.T) {
	cc := &countingCompactor{}
	a := New(Options{
		Model:     Model{Ref: "p/m", ID: "m", Info: llm.ModelInfo{ContextWindow: 100000}},
		Tools:     tools.NewRegistry(),
		Compactor: cc,
		Sink:      func(Event) {},
	})
	a.msgs = bigTranscript()
	a.learnWindow("p/m", 1000)
	a.lastIn = 800 // 80% of the learned window, 1% of the catalogue one
	if err := a.maybeCompact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cc.n != 1 {
		t.Fatalf("compactions = %d, want 1 against the learned window", cc.n)
	}
}

// A window learned for one model is applied when the agent switches back to it.
func TestLearnedWindowSurvivesModelSwitch(t *testing.T) {
	a := New(Options{Model: Model{Ref: "p/a", ID: "a", Info: llm.ModelInfo{ContextWindow: 100000}}, Sink: func(Event) {}})
	a.learnWindow("p/b", 5000)
	a.SetModel(Model{Ref: "p/b", ID: "b", Info: llm.ModelInfo{ContextWindow: 100000}})
	if w := a.CurrentModel().Info.ContextWindow; w != 5000 {
		t.Fatalf("window after switching to p/b = %d, want 5000", w)
	}
	a.SetModel(Model{Ref: "p/a", ID: "a", Info: llm.ModelInfo{ContextWindow: 100000}})
	if w := a.CurrentModel().Info.ContextWindow; w != 100000 {
		t.Fatalf("window for p/a changed to %d; only p/b was learned", w)
	}
}

func noticeTexts(log *eventLog) []string {
	var out []string
	for _, e := range log.of(EvNotice) {
		out = append(out, e.Text)
	}
	return out
}

func noticeMentions(log *eventLog, s string) bool {
	for _, t := range noticeTexts(log) {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}
