package agent

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// Local models sometimes type a tool call into their reply instead of making
// one. Nothing runs, and the markup reaches the user. The functions here turn
// the recognised shapes back into real calls. They only do so when the step
// made no real call, and only for tool names the request offered, so text
// that merely looks like a call is left alone.

const (
	openHermes  = "<tool_call>"
	closeHermes = "</tool_call>"
	openFunc    = "<function="
	closeFunc   = "</function>"
	openCode    = "<tool_code>"
	closeCode   = "</tool_code>"
)

var leakOpeners = []string{openHermes, openFunc, openCode}

var (
	funcHead  = regexp.MustCompile(`^<function=([A-Za-z0-9_.\-]+)>`)
	paramRe   = regexp.MustCompile(`(?s)<parameter=([A-Za-z0-9_.\-]+)>(.*?)</parameter>`)
	fenceCode = regexp.MustCompile("(?s)\\A```([A-Za-z_]+)[ \\t]*\\n(.*?)\\n?```\\z")
)

// textCall is one tool call recovered from text.
type textCall struct {
	name  string
	args  json.RawMessage
	start int // byte offset of the markup in the text
	end   int // byte offset just past it
}

// recoverTextCalls finds tool calls written as text. It returns the text with
// each recovered call's markup removed, and the calls in order. Markup that
// does not parse, or that names a tool not in offered, stays in the text.
func recoverTextCalls(text string, offered map[string]bool) (string, []textCall) {
	var calls []textCall
	var clean strings.Builder
	pos := 0
	for pos < len(text) {
		i, opener := nextOpener(text, pos)
		if i < 0 {
			break
		}
		c, ok := parseLeak(text, i, opener, offered)
		if !ok {
			// Keep this opener as text and search on after it.
			clean.WriteString(text[pos : i+1])
			pos = i + 1
			continue
		}
		clean.WriteString(text[pos:c.start])
		calls = append(calls, c)
		pos = c.end
	}
	if len(calls) == 0 {
		return text, nil
	}
	clean.WriteString(text[pos:])
	return clean.String(), calls
}

// nextOpener returns the position of the earliest opener at or after from,
// and which opener it is. The position is -1 when there is none.
func nextOpener(text string, from int) (int, string) {
	best, bestOp := -1, ""
	for _, op := range leakOpeners {
		j := strings.Index(text[from:], op)
		if j < 0 {
			continue
		}
		if best < 0 || from+j < best {
			best, bestOp = from+j, op
		}
	}
	return best, bestOp
}

// parseLeak parses the markup that opens at i. It returns the call and where
// its markup ends.
func parseLeak(text string, i int, opener string, offered map[string]bool) (textCall, bool) {
	rest := text[i+len(opener):]
	switch opener {
	case openHermes:
		j := strings.Index(rest, closeHermes)
		if j < 0 {
			return textCall{}, false
		}
		name, args, ok := parseCallBody(rest[:j], offered)
		if !ok {
			return textCall{}, false
		}
		return textCall{name: name, args: args, start: i, end: i + len(opener) + j + len(closeHermes)}, true
	case openCode:
		j := strings.Index(rest, closeCode)
		if j < 0 {
			return textCall{}, false
		}
		name, args, ok := parseCodeBody(rest[:j], offered)
		if !ok {
			return textCall{}, false
		}
		return textCall{name: name, args: args, start: i, end: i + len(opener) + j + len(closeCode)}, true
	default: // openFunc: the "<function=" is the start of the block itself
		name, args, used, ok := parseFunctionBlock(text[i:], offered)
		if !ok {
			return textCall{}, false
		}
		return textCall{name: name, args: args, start: i, end: i + used}, true
	}
}

// parseCallBody reads the inside of a <tool_call> block: a JSON call in the
// Hermes/Qwen shape, or a function block.
func parseCallBody(body string, offered map[string]bool) (string, json.RawMessage, bool) {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") {
		return parseJSONCall(trimmed, offered)
	}
	if k := strings.Index(trimmed, openFunc); k >= 0 {
		name, args, _, ok := parseFunctionBlock(trimmed[k:], offered)
		return name, args, ok
	}
	return "", nil, false
}

// parseCodeBody reads the inside of a <tool_code> block. A fenced ```shell
// block becomes shell with its contents as the command; JSON is read as in
// parseCallBody.
func parseCodeBody(body string, offered map[string]bool) (string, json.RawMessage, bool) {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") {
		return parseJSONCall(trimmed, offered)
	}
	m := fenceCode.FindStringSubmatch(trimmed)
	if m == nil || m[1] != "shell" || !offered["shell"] {
		return "", nil, false
	}
	args, err := json.Marshal(map[string]string{"command": strings.TrimRight(m[2], "\n")})
	if err != nil {
		return "", nil, false
	}
	return "shell", args, true
}

// parseJSONCall reads {"name": ..., "arguments": {...}}. The arguments may
// also be a JSON string that holds the object, as some servers send them.
func parseJSONCall(s string, offered map[string]bool) (string, json.RawMessage, bool) {
	var v struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal([]byte(s), &v) != nil || !offered[v.Name] {
		return "", nil, false
	}
	args, ok := objectArgs(v.Arguments)
	if !ok {
		return "", nil, false
	}
	return v.Name, args, true
}

// objectArgs accepts an arguments object, a JSON string holding one, or no
// arguments at all. Anything else is not a call.
func objectArgs(raw json.RawMessage) (json.RawMessage, bool) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("{}"), true
	}
	if raw[0] == '{' && json.Valid(raw) {
		return raw, true
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.HasPrefix(strings.TrimSpace(s), "{") && json.Valid([]byte(s)) {
			return json.RawMessage(s), true
		}
	}
	return nil, false
}

// parseFunctionBlock reads a <function=NAME> block at the start of s, with
// <parameter=KEY> children. It returns how many bytes of s the block used.
// Anything other than parameters inside the block makes it unreadable.
func parseFunctionBlock(s string, offered map[string]bool) (string, json.RawMessage, int, bool) {
	head := funcHead.FindStringSubmatch(s)
	if head == nil || !offered[head[1]] {
		return "", nil, 0, false
	}
	k := strings.Index(s, closeFunc)
	if k < 0 {
		return "", nil, 0, false
	}
	inner := s[len(head[0]):k]
	if strings.TrimSpace(paramRe.ReplaceAllString(inner, "")) != "" {
		return "", nil, 0, false
	}
	params := map[string]string{}
	for _, m := range paramRe.FindAllStringSubmatch(inner, -1) {
		// The newline after the opening tag and before the closing one are
		// layout, not part of the value.
		params[m[1]] = strings.TrimSuffix(strings.TrimPrefix(m[2], "\n"), "\n")
	}
	args, err := json.Marshal(params)
	if err != nil {
		return "", nil, 0, false
	}
	return head[1], args, k + len(closeFunc), true
}

// newTextCallID makes an id for a recovered call. Ids must not collide with
// ids the provider issues, so they carry a random part as well as a count.
func newTextCallID(n int) string {
	return fmt.Sprintf("textcall_%016x_%d", rand.Uint64(), n)
}

// leakGate decides what part of a streamed reply is safe to show. Text that
// could be the start of a tool call is held back until the step ends, so the
// markup never appears on screen. Once the step has a real call, nothing is
// held, since recovery can no longer apply.
type leakGate struct {
	all     strings.Builder // the step's text so far, including any resume text
	shown   int             // bytes of all that have been sent to the reader
	open    bool            // false once the step has a real tool call
	matched bool            // the held text starts with a full opener
}

// newLeakGate starts a gate. resume is text from an earlier attempt that the
// reader already has, so it is counted as shown.
func newLeakGate(resume string) *leakGate {
	g := &leakGate{open: true}
	g.all.WriteString(resume)
	g.shown = len(resume)
	return g
}

// feed takes a streamed text delta and returns what may be shown now.
func (g *leakGate) feed(delta string) string {
	g.all.WriteString(delta)
	if !g.open {
		return g.release(len(g.held()))
	}
	if g.matched {
		return ""
	}
	held := g.held()
	if i, _ := nextOpener(held, 0); i >= 0 {
		// Show the prose before the markup, and hold the rest to the end.
		g.matched = true
		return g.release(i)
	}
	if i := partialOpenerAt(held); i >= 0 {
		return g.release(i)
	}
	return g.release(len(held))
}

// stop ends holding, because the step has a real call. It returns what was
// held so the reader sees it as the plain text it is.
func (g *leakGate) stop() string {
	g.open = false
	return g.release(len(g.held()))
}

// finish returns the part of the step's final text that the reader has not
// yet seen. clean is the text after recovery; it has the same prefix as the
// text already shown, because recovered markup always starts in the held part.
func (g *leakGate) finish(clean string) string {
	shown := g.all.String()[:g.shown]
	if !strings.HasPrefix(clean, shown) {
		return ""
	}
	return clean[len(shown):]
}

// text returns everything the step has produced so far.
func (g *leakGate) text() string { return g.all.String() }

func (g *leakGate) held() string { return g.all.String()[g.shown:] }

func (g *leakGate) release(n int) string {
	out := g.all.String()[g.shown : g.shown+n]
	g.shown += n
	return out
}

// partialOpenerAt returns the position of the first '<' whose remaining text
// could still grow into an opener, or -1.
func partialOpenerAt(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		suffix := s[i:]
		for _, op := range leakOpeners {
			if len(suffix) < len(op) && strings.HasPrefix(op, suffix) {
				return i
			}
		}
	}
	return -1
}

// recoverStepCalls runs recovery on a finished step. It returns the parts to
// keep and the text the reader has not yet seen. Steps with a real call are
// left as they are.
func recoverStepCalls(parts []llm.Part, g *leakGate, offered map[string]bool) ([]llm.Part, string) {
	for _, p := range parts {
		if p.Type == llm.PartToolCall {
			return parts, g.finish(g.text())
		}
	}
	full := g.text()
	clean, calls := recoverTextCalls(full, offered)
	if len(calls) == 0 {
		return parts, g.finish(full)
	}
	// The text parts hold the whole step's text in order, so they are
	// replaced by one part at the first text position. Reasoning parts stay.
	var out []llm.Part
	placed := false
	for _, p := range parts {
		if p.Type != llm.PartText {
			out = append(out, p)
			continue
		}
		if !placed && strings.TrimSpace(clean) != "" {
			out = append(out, llm.Part{Type: llm.PartText, Text: clean})
		}
		placed = true
	}
	if !placed && strings.TrimSpace(clean) != "" {
		out = append(out, llm.Part{Type: llm.PartText, Text: clean})
	}
	for n, c := range calls {
		out = append(out, llm.Part{Type: llm.PartToolCall, ToolCallID: newTextCallID(n), ToolName: c.name, Args: c.args})
	}
	return out, g.finish(clean)
}

// offeredNames is the set of tool names in a request.
func offeredNames(specs []llm.ToolSpec) map[string]bool {
	names := make(map[string]bool, len(specs))
	for _, s := range specs {
		names[s.Name] = true
	}
	return names
}
