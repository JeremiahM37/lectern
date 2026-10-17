package agent

import (
	"errors"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// optionGroup is one set of optional request fields a provider may refuse
// (spec 03 §4.8). When a provider answers 400 and names one of its words, the
// fields are dropped and the step is retried once without them.
type optionGroup struct {
	// name is what the user and the log are told was dropped.
	name string
	// words are how a provider's error names the option. Matching is on the
	// lowercased error type and message.
	words []string
	// set reports whether the request carries any of the group's fields.
	set   func(r *llm.Request) bool
	clear func(r *llm.Request)
}

var optionGroups = []optionGroup{
	{
		name:  "reasoning",
		words: []string{"reasoning", "thinking"},
		set: func(r *llm.Request) bool {
			return r.Reasoning != "" || r.ThinkingBudget > 0 || r.ReasoningExclude
		},
		clear: func(r *llm.Request) {
			r.Reasoning, r.ThinkingBudget, r.ReasoningExclude = "", 0, false
		},
	},
	{
		name:  "service_tier",
		words: []string{"service_tier"},
		set:   func(r *llm.Request) bool { return r.ServiceTier != "" },
		clear: func(r *llm.Request) { r.ServiceTier = "" },
	},
	{
		name:  "verbosity",
		words: []string{"verbosity"},
		set:   func(r *llm.Request) bool { return r.Verbosity != "" },
		clear: func(r *llm.Request) { r.Verbosity = "" },
	},
	{
		name:  "parallel tool calls",
		words: []string{"parallel_tool_calls", "parallel_tool_use"},
		set:   func(r *llm.Request) bool { return r.DisableParallelToolUse },
		clear: func(r *llm.Request) { r.DisableParallelToolUse = false },
	},
	{
		name:  "temperature",
		words: []string{"temperature"},
		set:   func(r *llm.Request) bool { return r.Temperature != nil },
		clear: func(r *llm.Request) { r.Temperature = nil },
	},
}

// rejectPhrases are how a 400 says an option itself was the problem. A 400
// that names a field without one of these (a value out of range, say) is a
// real error and is not retried.
var rejectPhrases = []string{
	"unsupported", "not supported", "unknown", "unrecognized",
	"not allowed", "not permitted", "invalid", "not a valid",
}

// optKey identifies a dropped option group for one model.
type optKey struct {
	model, group string
}

// rejectedGroups returns the option groups a provider's 400 names, when the
// error is a 400 that rejects an option. It returns nil for every other error.
// Matching is on the error message alone.
func rejectedGroups(err error) []string {
	var ae *llm.APIError
	if !errors.As(err, &ae) || ae.StatusCode != 400 {
		return nil
	}
	// Only the message is searched. The error type is a fixed class such as
	// "invalid_request_error", which would match every rejection phrase.
	text := strings.ToLower(ae.Message)
	if !containsAny(text, rejectPhrases) {
		return nil
	}
	var out []string
	for _, g := range optionGroups {
		if containsAny(text, g.words) {
			out = append(out, g.name)
		}
	}
	return out
}

func containsAny(text string, words []string) bool {
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// reduceRequest applies the option drops latched for modelRef to r, and
// records which groups r carries after that, so a rejection can be matched
// against what was actually sent. It runs after Tune, so the drops win.
func (a *Agent) reduceRequest(modelRef string, r *llm.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sentGroups = map[string]bool{}
	for _, g := range optionGroups {
		if a.optDropped[optKey{modelRef, g.name}] {
			g.clear(r)
		}
		if g.set(r) {
			a.sentGroups[g.name] = true
		}
	}
}

// dropRejected latches the option groups that err names and the last request
// carried, for modelRef. It returns those group names, or nil when the error
// is not an option rejection or names nothing that was sent, in which case
// the caller treats the error as it always did.
func (a *Agent) dropRejected(modelRef string, err error) []string {
	names := rejectedGroups(err)
	if len(names) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, n := range names {
		if a.sentGroups[n] && !a.optDropped[optKey{modelRef, n}] {
			a.optDropped[optKey{modelRef, n}] = true
			out = append(out, n)
		}
	}
	return out
}
