package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

const (
	modelsPageSize = 1000
	maxModelPages  = 20
)

// Prices are USD per million tokens. Cache read is 0.1x input and cache
// write is 1.25x input, as Anthropic bills them.
const (
	opusPriceIn    = 15.0
	opusPriceOut   = 75.0
	sonnetPriceIn  = 3.0
	sonnetPriceOut = 15.0
	haikuPriceIn   = 1.0
	haikuPriceOut  = 5.0
)

// knownSpec is the static metadata for a model id or alias.
type knownSpec struct {
	id        string
	name      string
	ctxWindow int
	maxOut    int
	in, out   float64
}

// knownTable lists Claude models with metadata the models endpoint does not
// return. Entries marked "unverified" are current-generation models whose
// prices are not confirmed; they use the family default.
var knownTable = []knownSpec{
	// Current generation (unverified prices: family defaults).
	{id: "claude-opus-5-5", name: "Claude Opus 5.5", ctxWindow: 200000, maxOut: 64000, in: opusPriceIn, out: opusPriceOut},
	{id: "claude-sonnet-5-5", name: "Claude Sonnet 5.5", ctxWindow: 200000, maxOut: 64000, in: sonnetPriceIn, out: sonnetPriceOut},
	{id: "claude-haiku-5-5", name: "Claude Haiku 5.5", ctxWindow: 200000, maxOut: 64000, in: haikuPriceIn, out: haikuPriceOut},
	{id: "claude-fable-5-1", name: "Claude Fable 5.1", ctxWindow: 200000, maxOut: 64000, in: opusPriceIn, out: opusPriceOut},
	// Claude 4.x.
	{id: "claude-opus-4-5", name: "Claude Opus 4.5", ctxWindow: 200000, maxOut: 64000, in: 5.0, out: 25.0},
	{id: "claude-opus-4-1", name: "Claude Opus 4.1", ctxWindow: 200000, maxOut: 32000, in: opusPriceIn, out: opusPriceOut},
	{id: "claude-opus-4-0", name: "Claude Opus 4", ctxWindow: 200000, maxOut: 32000, in: opusPriceIn, out: opusPriceOut},
	{id: "claude-sonnet-4-5", name: "Claude Sonnet 4.5", ctxWindow: 200000, maxOut: 64000, in: sonnetPriceIn, out: sonnetPriceOut},
	{id: "claude-sonnet-4-0", name: "Claude Sonnet 4", ctxWindow: 200000, maxOut: 64000, in: sonnetPriceIn, out: sonnetPriceOut},
	{id: "claude-haiku-4-5", name: "Claude Haiku 4.5", ctxWindow: 200000, maxOut: 64000, in: haikuPriceIn, out: haikuPriceOut},
}

var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// Claude ids are "claude-<family>-<major>-<minor>" (claude-opus-4-7) or the
// older "claude-<major>-<minor>-<family>" (claude-3-7-sonnet). A trailing
// date is removed before matching.
var (
	familyFirst = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable|mythos)-(\d+)(?:-(\d+))?$`)
	familyLast  = regexp.MustCompile(`^claude-(\d+)(?:-(\d+))?-(opus|sonnet|haiku)$`)
)

// ReasoningStyle reports how a Claude model takes thinking.
//
//   - "adaptive": Claude 5 models (any family) and Opus 4.7 and later. They
//     accept only thinking {type: "adaptive"} with an output_config effort
//     level; budget-style thinking is a 400. They also reject temperature.
//   - "budget": other reasoning models, which take {type: "enabled",
//     budget_tokens}. This is also the answer for ids this table does not
//     recognise, so unknown ids keep the budget behaviour they always had.
//   - "" : Claude 3.x models before 3.7, which have no extended thinking.
func ReasoningStyle(model string) string {
	id := dateSuffix.ReplaceAllString(model, "")
	if m := familyFirst.FindStringSubmatch(id); m != nil {
		major, minor := atoi(m[2]), atoi(m[3])
		switch {
		case major >= 5:
			return llm.ReasoningAdaptive
		case m[1] == "opus" && (major > 4 || (major == 4 && minor >= 7)):
			return llm.ReasoningAdaptive
		}
		return llm.ReasoningBudget
	}
	if m := familyLast.FindStringSubmatch(id); m != nil {
		major, minor := atoi(m[1]), atoi(m[2])
		if major == 3 && minor < 7 {
			return llm.ReasoningNone
		}
		if major >= 5 {
			return llm.ReasoningAdaptive
		}
		return llm.ReasoningBudget
	}
	return llm.ReasoningBudget
}

// atoi reads a version component. The regexp guarantees digits, and an
// absent optional component is 0.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// lookupKnown finds static metadata for id. A dated id such as
// claude-haiku-4-5-20251001 matches its alias claude-haiku-4-5.
func lookupKnown(id string) (knownSpec, bool) {
	for _, k := range knownTable {
		if k.id == id {
			return k, true
		}
	}
	if base := dateSuffix.ReplaceAllString(id, ""); base != id {
		for _, k := range knownTable {
			if k.id == base {
				return k, true
			}
		}
	}
	return knownSpec{}, false
}

func (k knownSpec) info(id string) llm.ModelInfo {
	name := k.name
	if name == "" {
		name = id
	}
	return llm.ModelInfo{
		ID:                id,
		Name:              name,
		ContextWindow:     k.ctxWindow,
		MaxOutput:         k.maxOut,
		InputPerMTok:      k.in,
		OutputPerMTok:     k.out,
		CacheReadPerMTok:  k.in * 0.1,
		CacheWritePerMTok: k.in * 1.25,
		Reasoning:         true,
		ReasoningStyle:    ReasoningStyle(id),
		Vision:            true,
		Tools:             true,
	}
}

// KnownModels returns the static table of Claude models, in table order.
func KnownModels() []llm.ModelInfo {
	out := make([]llm.ModelInfo, 0, len(knownTable))
	for _, k := range knownTable {
		out = append(out, k.info(k.id))
	}
	return out
}

// Models lists models from GET /v1/models, following pagination, and
// enriches them with the static table. Known models the endpoint did not
// return are appended, so the list is never empty when the static table
// has entries. If the request fails, the static table is returned together
// with the error.
func (p *Provider) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	remote, err := p.listRemote(ctx)
	if err != nil {
		return KnownModels(), err
	}

	seen := map[string]bool{}
	out := make([]llm.ModelInfo, 0, len(remote)+len(knownTable))
	for _, m := range remote {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if k, ok := lookupKnown(m.ID); ok {
			info := k.info(m.ID)
			if m.DisplayName != "" {
				info.Name = m.DisplayName
			}
			out = append(out, info)
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		out = append(out, llm.ModelInfo{ID: m.ID, Name: name, Reasoning: true, ReasoningStyle: ReasoningStyle(m.ID), Vision: true, Tools: true})
	}
	for _, k := range knownTable {
		if !seen[k.id] {
			seen[k.id] = true
			out = append(out, k.info(k.id))
		}
	}
	return out, nil
}

type remoteModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// listRemote walks GET /v1/models with after_id cursors.
func (p *Provider) listRemote(ctx context.Context) ([]remoteModel, error) {
	var all []remoteModel
	after := ""
	for page := 0; page < maxModelPages; page++ {
		u := fmt.Sprintf("%s/v1/models?limit=%d", p.opts.BaseURL, modelsPageSize)
		if after != "" {
			u += "&after_id=" + url.QueryEscape(after)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("anthropic: build models request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		p.setAuth(req.Header, nil)

		resp, err := p.opts.HTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("anthropic: list models: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			apiErr := p.readAPIError(resp)
			_ = resp.Body.Close()
			return nil, apiErr
		}
		var body struct {
			Data    []remoteModel `json:"data"`
			HasMore bool          `json:"has_more"`
			LastID  string        `json:"last_id"`
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("anthropic: read models: %w", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("anthropic: decode models: %w", err)
		}
		all = append(all, body.Data...)
		if !body.HasMore || body.LastID == "" {
			break
		}
		after = body.LastID
	}
	return all, nil
}
