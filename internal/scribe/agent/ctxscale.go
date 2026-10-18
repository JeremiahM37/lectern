package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// Context-window learning. A model's window is pinned from the catalogue, and
// the catalogue can be wrong or missing: a local server may load a smaller
// window than the model supports. When a provider refuses a request as too
// long, the agent takes the window the error states, or steps the window down
// when the error states none, compacts the history once and retries the step
// once. The learned window lasts for the session. It only ever shrinks: an
// error naming a larger window than the one in use is not evidence that the
// request fits, so it is treated as a refusal without a number.

const (
	// The window is kept at ctxStepDownNum/ctxStepDownDen of its size when
	// the error gives no number.
	ctxStepDownNum = 3
	ctxStepDownDen = 4
	// Windows outside this range are not believed. A message claiming a
	// window below the floor is most likely a count of something else.
	minLearnedWindow = 1024
	maxLearnedWindow = 10_000_000
)

// tooLongMessage matches how providers word an oversized request. The
// wording differs by provider; each alternative is a phrase one of them uses.
var tooLongMessage = regexp.MustCompile(`maximum context length|context[ _]length[ _]exceeded|input length exceeds|prompt is too long|prompt too long|exceeds the context (?:window|length)|exceeded the context (?:window|length)`)

// reportedWindow pulls the stated window out of a refusal, lower-cased. The
// patterns are tried in order and the first that fits wins.
var reportedWindow = []*regexp.Regexp{
	// OpenAI: "maximum context length is 128000 tokens. However, ..."
	regexp.MustCompile(`maximum context length (?:is|of) ([\d,]+)`),
	// Anthropic: "prompt is too long: 201000 tokens > 200000 maximum"
	regexp.MustCompile(`prompt is too long: [\d,]+ tokens? > ([\d,]+)`),
	// Gateways and local servers: "context window of 8192", "context length is 8192"
	regexp.MustCompile(`context (?:window|length) (?:is|of|size is|size of) ([\d,]+)`),
}

// contextLengthError reports whether err is a provider refusing the request
// because it is too long. window is the context window the error states, or 0
// when it states none (or states one outside the believable range).
func contextLengthError(err error) (window int, tooLong bool) {
	var ae *llm.APIError
	if !errors.As(err, &ae) {
		return 0, false
	}
	msg := strings.ToLower(ae.Message)
	if !tooLongMessage.MatchString(msg) && !strings.Contains(strings.ToLower(ae.Type), "context_length") {
		return 0, false
	}
	for _, re := range reportedWindow {
		if m := re.FindStringSubmatch(msg); m != nil {
			if n, ok := parseWindow(m[1]); ok {
				return n, true
			}
			break
		}
	}
	return 0, true
}

// parseWindow reads a token count such as "128,000" and reports whether it is
// in the believable range.
func parseWindow(s string) (int, bool) {
	n, err := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
	if err != nil || n < minLearnedWindow || n > maxLearnedWindow {
		return 0, false
	}
	return n, true
}

// nextWindow returns the window to use after a refusal as too long. When the
// error states a window below the current one, that is the new window. The
// current window is never raised, and a stated window that is not smaller is
// ignored in favour of a step down. It returns 0 when there is no window to
// learn: the current one is unknown and the error gave none.
func nextWindow(current, reported int) int {
	if reported > 0 && (current <= 0 || reported < current) {
		return reported
	}
	if current <= 0 {
		return 0
	}
	stepped := current * ctxStepDownNum / ctxStepDownDen
	if stepped < minLearnedWindow {
		return 0
	}
	return stepped
}

// learnWindow records window as the context window of the model with ref for
// the rest of the session, and applies it when that model is the current one.
func (a *Agent) learnWindow(ref string, window int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.learned == nil {
		a.learned = map[string]int{}
	}
	a.learned[ref] = window
	if a.model.Ref == ref {
		a.model.Info.ContextWindow = window
	}
}

// recoverContextLength handles a step that a provider refused as too long. It
// learns the window and compacts the history once, and reports whether the
// step should be retried. The caller retries at most once per step.
func (a *Agent) recoverContextLength(ctx context.Context, reported int) bool {
	m := a.CurrentModel()
	window := nextWindow(m.Info.ContextWindow, reported)
	if window == 0 {
		return false
	}
	a.learnWindow(m.Ref, window)
	err := a.compact(ctx, fmt.Sprintf("%s takes %d tokens", m.Ref, window), true)
	if err != nil {
		// Without a compaction the same request would be refused again, so the
		// step is not retried. The window is still remembered for next time.
		a.emit(Event{Kind: EvNotice, Text: fmt.Sprintf(
			"%s rejected the request as too long. Its context window is %d tokens; that is remembered for this session. The history could not be compacted, so this step was not retried.",
			m.Ref, window)})
		return false
	}
	a.emit(Event{Kind: EvNotice, Text: fmt.Sprintf(
		"%s rejected the request as too long. Its context window is %d tokens; that is remembered for this session. The history was compacted and the step is being retried.",
		m.Ref, window)})
	return true
}
