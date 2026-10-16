package hooks

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// ifPattern is the `if` syntax: ToolName(glob).
var ifPattern = regexp.MustCompile(`^(\w+)\((.+)\)$`)

// toolCond is a parsed `if` condition. A hook with one runs only for the
// named tool whose first string input field matches the glob.
type toolCond struct {
	tool string         // Claude-style tool name, compared exactly
	re   *regexp.Regexp // the glob, anchored
}

// parseCond parses an `if` string. An empty or malformed string gives nil,
// which allows the hook (spec 7.5).
func parseCond(s string) *toolCond {
	m := ifPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil
	}
	re, err := regexp.Compile("^" + globRegexp(m[2]) + "$")
	if err != nil {
		return nil
	}
	return &toolCond{tool: m[1], re: re}
}

// globRegexp turns a glob into a regexp body: `*` matches any run of
// characters, `?` one character, everything else is literal.
func globRegexp(glob string) string {
	var b strings.Builder
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

// ok reports whether the condition admits a call to the tool claudeName
// with the given arguments. A nil condition admits everything.
func (c *toolCond) ok(claudeName string, args json.RawMessage) bool {
	if c == nil {
		return true
	}
	return c.tool == claudeName && c.re.MatchString(firstString(args))
}

// firstString returns the first string-valued field of a JSON object, in
// document order. It returns "" when there is none or args is not an object.
func firstString(args json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(args))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return ""
	}
	for dec.More() {
		if _, err := dec.Token(); err != nil {
			return ""
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return ""
		}
		if len(v) > 0 && v[0] == '"' {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
		}
	}
	return ""
}
