package headless

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// format is the output mode chosen by the flags.
type format int

const (
	fmtText format = iota
	fmtJSON
	fmtEvents
)

// renderWidth is the column width used for --render.
const renderWidth = 80

// output writes everything the runner reports. Events arrive from the agent
// goroutine while the runner writes from its own, so every method locks.
type output struct {
	mu     sync.Mutex
	stdout io.Writer
	stderr io.Writer
	format format
	quiet  bool
	render bool
	diff   bool

	streamed strings.Builder // this turn's text, kept only for --render
	lastByte byte            // last byte written to stdout this turn
	final    string          // the turn's final answer (EvTurnDone)
	errSeen  bool            // an error was already reported this turn
}

func newOutput(stdout, stderr io.Writer, o options) *output {
	f := fmtText
	switch {
	case o.events:
		f = fmtEvents
	case o.json:
		f = fmtJSON
	}
	return &output{
		stdout: stdout, stderr: stderr, format: f, quiet: o.quiet,
		render: o.render && f == fmtText, diff: o.diff,
	}
}

// tokenCounts is the token block of every JSON report.
type tokenCounts struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

func tokensOf(u llm.Usage) tokenCounts {
	return tokenCounts{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadTokens, CacheWrite: u.CacheWriteTokens}
}

// turnReport is one finished turn: the done event, a chat turn-done event
// and a chat turn object share these fields.
type turnReport struct {
	Turn        int         `json:"turn,omitempty"`
	Output      string      `json:"output"`
	Steps       int         `json:"steps"`
	Tokens      tokenCounts `json:"tokens"`
	ToolCalls   []string    `json:"toolCalls"`
	FilesEdited []string    `json:"filesEdited"`
	Duration    int64       `json:"duration"` // milliseconds
	Error       string      `json:"error,omitempty"`
}

func reportOf(res turnResult, final string) turnReport {
	s := res.stats
	return turnReport{
		Output:      final,
		Steps:       s.Steps,
		Tokens:      tokensOf(s.Tokens),
		ToolCalls:   nonNil(s.ToolCalls),
		FilesEdited: nonNil(s.FilesEdited),
		Duration:    s.Duration.Milliseconds(),
		Error:       s.Error,
	}
}

// runObject is the single --json object of a one-shot run.
type runObject struct {
	Model       string      `json:"model"`
	Mode        string      `json:"mode"`
	Prompt      string      `json:"prompt"`
	Output      string      `json:"output"`
	Steps       int         `json:"steps"`
	Tokens      tokenCounts `json:"tokens"`
	CostUSD     float64     `json:"costUSD"`
	ToolCalls   []string    `json:"toolCalls"`
	FilesEdited []string    `json:"filesEdited"`
	Duration    int64       `json:"duration"`
	SessionID   string      `json:"sessionId,omitempty"`
	Error       string      `json:"error,omitempty"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// start announces the run: an event in events mode, a line on stderr otherwise.
func (o *output) start(model, mode string, chat bool, sessionID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.format == fmtEvents {
		ev := map[string]any{"type": "start", "model": model, "mode": mode, "session": sessionPrefix(sessionID), "repoMap": nil, "workers": nil}
		if chat {
			ev["chat"] = true
		}
		o.emitL(ev)
		return
	}
	o.progressL("model: %s · mode: %s", model, mode)
}

func (o *output) ready() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.format == fmtEvents {
		o.emitL(map[string]any{"type": "ready"})
	}
}

// prompt marks the chat input line on a terminal.
func (o *output) prompt() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.format == fmtText && !o.quiet {
		fmt.Fprint(o.stderr, "> ")
	}
}

func (o *output) beginTurn() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.streamed.Reset()
	o.lastByte = 0
	o.final = ""
	o.errSeen = false
}

func (o *output) finalText() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.final
}

// handle turns one agent event into output.
func (o *output) handle(e agent.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch e.Kind {
	case agent.EvText:
		if o.format == fmtEvents {
			o.emitL(map[string]any{"type": "text", "content": e.Text})
		}
		if o.format == fmtText {
			if o.render {
				o.streamed.WriteString(e.Text)
			} else {
				o.writeStdoutL(e.Text)
			}
		}
	case agent.EvToolRun:
		o.progressL("▸ %s", toolLine(e.Tool, e.Args))
		if o.format == fmtEvents {
			ev := map[string]any{"type": "tool-call", "tool": e.Tool}
			if e.CallID != "" {
				ev["toolCallId"] = e.CallID
			}
			if input := jsonObject(e.Args); input != nil {
				ev["input"] = input
			}
			o.emitL(ev)
		}
	case agent.EvToolResult:
		if o.format == fmtEvents {
			text := ""
			if e.Result != nil {
				text = e.Result.Output
				if text == "" && e.Result.Summary != "" {
					text = e.Result.Summary
				}
			}
			ev := map[string]any{"type": "tool-result", "tool": e.Tool, "summary": clip(text, 200)}
			if e.CallID != "" {
				ev["toolCallId"] = e.CallID
			}
			o.emitL(ev)
		}
	case agent.EvStepFinish:
		if o.format == fmtEvents {
			o.emitL(map[string]any{"type": "step", "step": e.Step, "tokens": map[string]any{
				"input": e.Total.InputTokens, "output": e.Total.OutputTokens, "cacheRead": e.Total.CacheReadTokens,
				"cacheWrite":        e.Total.CacheWriteTokens,
				"lastStepInput":     e.Usage.InputTokens,
				"lastStepOutput":    e.Usage.OutputTokens,
				"lastStepCacheRead": e.Usage.CacheReadTokens,
			}})
		}
	case agent.EvRetry:
		o.progressL("retrying (%d/%d): %s", e.Attempt, e.MaxAttempts, e.Text)
		if o.format == fmtEvents {
			o.emitL(map[string]any{"type": "retry", "attempt": e.Attempt, "maxAttempts": e.MaxAttempts,
				"category": string(e.Category), "reason": e.Text, "delay": e.Delay.Milliseconds()})
		}
	case agent.EvFallback:
		o.progressL("switching to %s: %s", e.Model, e.Text)
	case agent.EvCompactStart:
		o.progressL("compacting the conversation")
	case agent.EvWarning:
		if o.format == fmtEvents {
			o.emitL(map[string]any{"type": "warning", "message": e.Text})
		} else {
			o.warnL(e.Text)
		}
	case agent.EvError:
		o.errSeen = true
		if o.format == fmtEvents {
			o.emitL(map[string]any{"type": "error", "error": e.Text})
		} else if o.format == fmtText {
			fmt.Fprintf(o.stderr, "scribe: %s\n", e.Text)
		}
	case agent.EvTurnDone:
		o.final = e.Text
	}
}

// endText finishes a text-mode turn: the newline or rendered Markdown, the
// stats line and, with --diff, the edited files.
func (o *output) endText(s agent.TurnStats) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.format != fmtText {
		return
	}
	if o.render {
		body := o.streamed.String()
		if strings.TrimSpace(body) != "" {
			fmt.Fprintln(o.stdout, render.Markdown(body, renderWidth, theme.Default(), false))
		}
	} else if o.lastByte != 0 && o.lastByte != '\n' {
		fmt.Fprintln(o.stdout)
	}
	if !o.quiet {
		o.progressL("%s", statsLine(s))
		if o.diff && len(s.FilesEdited) > 0 {
			o.progressL("edited:")
			for _, f := range s.FilesEdited {
				fmt.Fprintf(o.stderr, "  %s\n", f)
			}
		}
	}
}

// finishRun reports a one-shot run in json or events mode. Text mode was
// finished by endText.
func (o *output) finishRun(model, mode, prompt string, res turnResult, sessionID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rep := reportOf(res, res.final)
	switch o.format {
	case fmtJSON:
		obj := runObject{
			Model: model, Mode: mode, Prompt: prompt, Output: rep.Output, Steps: rep.Steps,
			Tokens: rep.Tokens, CostUSD: res.stats.CostUSD, ToolCalls: rep.ToolCalls,
			FilesEdited: rep.FilesEdited, Duration: rep.Duration, SessionID: sessionID, Error: rep.Error,
		}
		o.writeJSONL(obj, true)
	case fmtEvents:
		o.emitL(doneEvent{Type: "done", turnReport: rep})
	}
}

// turnDone reports the end of a chat turn.
func (o *output) turnDone(turn int, res turnResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rep := reportOf(res, res.final)
	rep.Turn = turn
	switch o.format {
	case fmtJSON:
		o.writeJSONL(rep, false)
	case fmtEvents:
		o.emitL(doneEvent{Type: "turn-done", turnReport: rep})
	}
}

// chatDone is the last event of a chat session.
func (o *output) chatDone(turns int, usage llm.Usage, sessionID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.format == fmtEvents {
		ev := map[string]any{"type": "chat-done", "turns": turns, "tokens": tokensOf(usage)}
		if sessionID != "" {
			ev["sessionId"] = sessionID
		}
		o.emitL(ev)
	}
}

// saved reports where --save-session kept the session.
func (o *output) saved(sessionID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch o.format {
	case fmtEvents:
		o.emitL(map[string]any{"type": "session-saved", "sessionId": sessionID})
	case fmtText:
		if !o.quiet {
			fmt.Fprintf(o.stderr, "session saved; resume with: lectern code --resume %s\n", sessionID)
		}
	}
}

// turnError reports a failed or aborted turn. A message already reported as
// an agent error event is not repeated.
func (o *output) turnError(msg string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.errSeen {
		return
	}
	o.errSeen = true
	switch o.format {
	case fmtEvents:
		o.emitL(map[string]any{"type": "error", "error": msg})
	case fmtText:
		fmt.Fprintf(o.stderr, "scribe: %s\n", msg)
	}
}

// fail reports an error before any run started.
func (o *output) fail(msg string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.format == fmtEvents {
		o.emitL(map[string]any{"type": "error", "error": msg})
		return
	}
	fmt.Fprintf(o.stderr, "scribe: %s\n", msg)
}

func (o *output) warn(msg string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.warnL(msg)
}

// warnL sends a warning to the events stream, or to stderr in other modes.
// Warnings are not progress, so --quiet does not hide them.
func (o *output) warnL(msg string) {
	if o.format == fmtEvents {
		o.emitL(map[string]any{"type": "warning", "message": msg})
		return
	}
	fmt.Fprintf(o.stderr, "scribe: warning: %s\n", msg)
}

// doneEvent is the done (or turn-done) event: its type plus the turn fields.
type doneEvent struct {
	Type string `json:"type"`
	turnReport
}

// The helpers below assume o.mu is held.

func (o *output) emitL(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	o.stdout.Write(append(b, '\n'))
}

func (o *output) writeJSONL(v any, indent bool) {
	var b []byte
	var err error
	if indent {
		b, err = json.MarshalIndent(v, "", "  ")
	} else {
		b, err = json.Marshal(v)
	}
	if err != nil {
		return
	}
	o.stdout.Write(append(b, '\n'))
}

func (o *output) writeStdoutL(s string) {
	if s == "" {
		return
	}
	io.WriteString(o.stdout, s)
	o.lastByte = s[len(s)-1]
}

// progressL writes to stderr in text mode unless --quiet.
func (o *output) progressL(format string, args ...any) {
	if o.format != fmtText || o.quiet {
		return
	}
	fmt.Fprintf(o.stderr, format+"\n", args...)
}

// statsLine is the text-mode summary after a turn.
func statsLine(s agent.TurnStats) string {
	cached := 0
	if s.Tokens.InputTokens > 0 {
		cached = s.Tokens.CacheReadTokens * 100 / s.Tokens.InputTokens
	}
	return fmt.Sprintf("%d steps — %s in, %s out, %d%% cached — $%.4f — %s",
		s.Steps, humanTokens(s.Tokens.InputTokens), humanTokens(s.Tokens.OutputTokens), cached, s.CostUSD,
		s.Duration.Round(100*time.Millisecond))
}

func humanTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// toolLine is the progress line for a tool call: the tool and its most
// telling argument.
func toolLine(tool string, args json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return tool
	}
	for _, k := range []string{"command", "path", "file_path", "pattern", "query", "url", "task", "name"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return tool + " " + clip(strings.Join(strings.Fields(s), " "), 100)
		}
	}
	return tool
}

// jsonObject returns raw when it is a JSON object, else nil.
func jsonObject(raw json.RawMessage) json.RawMessage {
	t := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(t, "{") || !json.Valid(raw) {
		return nil
	}
	return json.RawMessage(t)
}

// clip keeps the first n runes and marks a cut with an ellipsis.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// sessionPrefix is the short id events carry; "" becomes null.
func sessionPrefix(id string) any {
	if id == "" {
		return nil
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
