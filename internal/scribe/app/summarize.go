package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
)

// modelSummarizer implements repomap.Summarizer over one configured model.
// It sends file paths, doc comments and exported signatures only, never
// source text, and asks for a JSON array with one line per file.
type modelSummarizer struct {
	model agent.Model
}

const summarizerPrompt = "You write one-line summaries of source files for a code map. " +
	"For each file in the request, write one plain sentence of at most 20 words saying what the file is for. " +
	"Reply with only a JSON array of strings, one per file, in the same order. Use an empty string when you cannot tell."

func (s modelSummarizer) Summarize(ctx context.Context, files []repomap.SummaryRequest) ([]string, error) {
	if s.model.Provider == nil {
		return nil, fmt.Errorf("summarizer has no provider")
	}
	in, err := json.Marshal(files)
	if err != nil {
		return nil, err
	}
	req := llm.Request{
		Model:     s.model.ID,
		System:    summarizerPrompt,
		Messages:  []llm.Message{llm.UserText(string(in))},
		MaxTokens: 1024,
	}
	text, err := agent.Collect(ctx, s.model.Provider, req)
	if err != nil {
		return nil, err
	}
	var out []string
	start, end := strings.Index(text, "["), strings.LastIndex(text, "]")
	if start < 0 || end <= start || json.Unmarshal([]byte(text[start:end+1]), &out) != nil {
		return nil, fmt.Errorf("summarizer answered without a JSON array")
	}
	// One entry per request, in order; short answers pad with "no summary".
	res := make([]string, len(files))
	copy(res, out)
	return res, nil
}

// summarizerModel is the model used for code map summaries: the semantic
// slot, else the compact slot, else the explore slot. It returns false when
// none is configured,
// and then the map gets no model summaries.
func (a *App) summarizerModel() (agent.Model, bool) {
	for _, ref := range []string{a.Cfg.Router.Semantic, a.Cfg.Router.Compact, a.Cfg.Router.Explore} {
		if ref == "" {
			continue
		}
		if m, err := a.resolve(ref); err == nil {
			return m, true
		}
	}
	return agent.Model{}, false
}

// installSummarizer hands the map a model summariser when one is configured
// and summarises the top files in the background. With no model configured
// it does nothing.
func (a *App) installSummarizer(m *repomap.Map) {
	model, ok := a.summarizerModel()
	if !ok {
		return
	}
	// Summaries are asked for only when auto-regenerate is on and the mode
	// shows them (finishMapBuild); the default mode does not, so asking at
	// every start spent model calls on text nobody saw.
	m.SetSummarizer(modelSummarizer{model: model})
}
