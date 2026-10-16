package app

import (
	"context"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
)

// fakeProvider answers every call with one text reply.
type fakeProvider struct{ reply string }

func (f fakeProvider) ID() string { return "fake" }
func (f fakeProvider) Stream(_ context.Context, _ llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event, 2)
	ch <- llm.Event{Type: llm.EvText, Text: f.reply}
	close(ch)
	return ch, nil
}
func (f fakeProvider) Models(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func TestModelSummarizerReadsJSONArrayInOrder(t *testing.T) {
	s := modelSummarizer{model: agent.Model{ID: "m", Provider: fakeProvider{reply: "Sure:\n[\"Parses config.\", \"\"]"}}}
	got, err := s.Summarize(context.Background(), []repomap.SummaryRequest{{Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "Parses config." || got[1] != "" || got[2] != "" {
		t.Fatalf("summaries = %q", got)
	}
}

func TestModelSummarizerRejectsNonJSON(t *testing.T) {
	s := modelSummarizer{model: agent.Model{ID: "m", Provider: fakeProvider{reply: "no array here"}}}
	if _, err := s.Summarize(context.Background(), []repomap.SummaryRequest{{Path: "a.go"}}); err == nil {
		t.Fatal("a reply without a JSON array must be an error")
	}
}

func TestSummarizerUnsetWithoutModel(t *testing.T) {
	a := &App{}
	if _, ok := a.summarizerModel(); ok {
		t.Fatal("no router slot configured should give no summarizer model")
	}
}
