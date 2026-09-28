package helpers

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// approval-hook is internal/hooks/hook.py, the PreToolUse gate: it holds a
// tool call until the operator decides. Exit 0 allows the call; exit 2 blocks
// it and Claude Code feeds stderr back to the agent as the reason.
func init() { Register("approval-hook", approvalHook) }

// approvalPollPause is how long hook.py waits after a failed poll.
var approvalPollPause = 5 * time.Second

func approvalHook(_ []string, stdin io.Reader, _, stderr io.Writer) int {
	base := strings.TrimRight(os.Getenv("LECTERN_URL"), "/")
	token := os.Getenv("LECTERN_TOKEN")
	if base == "" || token == "" {
		return 0 // unconfigured → don't block local runs
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return 0
	}
	payload, err := pyJSONLoadBytes(raw)
	if err != nil {
		return 0
	}
	call, ok := payload.(*pyDict)
	if !ok {
		fmt.Fprintln(stderr, "lectern approval hook: the tool call is not a JSON object")
		return 1
	}
	body := newPyDict()
	body.set("token", token)
	name, ok := call.get("tool_name")
	if !ok {
		name = "?"
	}
	body.set("tool_name", name)
	input, ok := call.get("tool_input")
	if !ok {
		input = newPyDict()
	}
	body.set("tool_input", input)

	client := urllibClient(60 * time.Second)
	// api is hook.py's api(): only a network or HTTP failure is something the
	// script catches; an unreadable answer is an uncaught exception (exit 1).
	api := func(method, path string, req any) (any, error, bool) {
		var data []byte
		if req != nil {
			s, err := pyJSONDumps(req, -1, true)
			if err != nil {
				return nil, err, true
			}
			data = []byte(s)
		}
		out, err := urlopen(client, method, base+path, data)
		if err != nil {
			return nil, err, err == errNotHTTP
		}
		v, err := pyJSONLoadBytes(out)
		return v, err, err != nil
	}

	created, err, uncaught := api("POST", "/api/hook/approval", body)
	if uncaught {
		fmt.Fprintf(stderr, "lectern approval hook: %v\n", err)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "lectern approval server unreachable (%s); blocking for safety\n", urllibErrorText(err))
		return 2
	}
	createdDict, ok := created.(*pyDict)
	var id any
	if ok {
		id, ok = createdDict.get("id")
	}
	if !ok {
		fmt.Fprintln(stderr, "lectern approval hook: the approval server returned no id")
		return 1
	}
	timeout, ok := pyFloat(envOr("LECTERN_APPROVAL_TIMEOUT", "900"))
	if !ok {
		fmt.Fprintln(stderr, "lectern approval hook: LECTERN_APPROVAL_TIMEOUT is not a number")
		return 1
	}
	deadline := float64(time.Now().UnixNano())/1e9 + timeout
	for float64(time.Now().UnixNano())/1e9 < deadline {
		got, err, uncaught := api("GET", "/api/hook/approval/"+pyStr(id)+"/decision", nil)
		if uncaught {
			fmt.Fprintf(stderr, "lectern approval hook: %v\n", err)
			return 1
		}
		if err != nil {
			time.Sleep(approvalPollPause)
			continue
		}
		d, ok := got.(*pyDict)
		if !ok {
			fmt.Fprintln(stderr, "lectern approval hook: the decision is not a JSON object")
			return 1
		}
		status, _ := d.get("status")
		if s, ok := status.(string); ok {
			switch s {
			case "approved":
				return 0
			case "denied", "expired":
				note, _ := d.get("note")
				if !pyTruthy(note) {
					note = "operator denied this action"
				}
				fmt.Fprintf(stderr, "Blocked by operator: %s\n", pyStr(note))
				return 2
			}
		}
	}
	fmt.Fprintln(stderr, "Approval timed out with no operator decision; action blocked.")
	return 2
}

func envOr(name, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}

// pyFloat is float(str).
func pyFloat(s string) (float64, bool) {
	s = pyStrip(s)
	low := strings.ToLower(strings.TrimLeft(s, "+-"))
	neg := strings.HasPrefix(s, "-")
	switch {
	case (low == "inf" || low == "infinity") && len(s)-len(low) <= 1:
		if neg {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	case low == "nan" && len(s)-len(low) <= 1:
		return math.NaN(), true
	}
	clean, ok := pyUnderscores(s)
	if !ok || strings.ContainsAny(clean, "xXpP") || strings.EqualFold(strings.TrimLeft(clean, "+-"), "infinity") {
		return 0, false
	}
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil && !isRangeErr(err) {
		return 0, false
	}
	return f, true
}

func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// pyUnderscores drops the single underscores Python allows between digits.
func pyUnderscores(s string) (string, bool) {
	if !strings.Contains(s, "_") {
		return s, true
	}
	isD := func(c byte) bool { return c >= '0' && c <= '9' }
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '_' {
			if i == 0 || i == len(s)-1 || !isD(s[i-1]) || !isD(s[i+1]) {
				return "", false
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String(), true
}
