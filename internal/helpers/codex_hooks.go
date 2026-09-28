package helpers

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// The codex side of agent events (internal/agentevents/codex_settings.go):
//
//   - codex-notify is CodexNotifyScript, codex's `notify` program: it posts
//     agent-turn-complete to Lectern and never fails.
//   - codex-hook is CodexHookScript, the handler every managed hooks.json
//     entry runs: it forwards the hook body and prints Lectern's answer.
//   - codex-hooks-install is codexHooksInstallPy, which merges Lectern's
//     groups into $CODEX_HOME/hooks.json.
//
// The scripts post with curl; the ports post themselves, with curl's timeout,
// proxy and no-redirect behaviour, so a target needs neither.
func init() {
	Register("codex-notify", codexNotify)
	Register("codex-hook", codexHook)
	Register("codex-hooks-install", codexHooksInstall)
}

func codexNotify(args []string, _ io.Reader, _, _ io.Writer) int {
	token := os.Getenv("LECTERN_HOOK_TOKEN")
	base := strings.TrimRight(os.Getenv("LECTERN_HOOK_URL"), "/")
	if token == "" || base == "" {
		return 0
	}
	var payload any = newPyDict()
	if len(args) > 0 {
		if v, err := pyJSONLoads(args[len(args)-1]); err == nil {
			payload = v
		} else {
			payload = newPyDict()
		}
	}
	body := newPyDict()
	body.set("hook_event_name", "AgentTurnComplete")
	body.set("codex_notify", payload)
	text, err := pyJSONDumps(body, -1, true)
	if err != nil {
		return 0
	}
	curlPost(5, token, base+"/AgentTurnComplete", []byte(text))
	return 0
}

// curlPost is the scripts' `curl -s -m SECONDS -X POST` call; it returns the
// response body, or nothing when the request failed.
func curlPost(seconds int, token, rawURL string, body []byte) []byte {
	req, err := http.NewRequest("POST", rawURL, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	limit := time.Duration(0) // curl -m 0, or more seconds than a Duration holds: no limit
	if seconds > 0 && seconds < 1<<30 {
		limit = time.Duration(seconds) * time.Second
	}
	resp, err := curlClient(limit).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	// curl has already written what arrived when its timer fires.
	out, _ := io.ReadAll(resp.Body)
	return out
}

func codexHook(args []string, stdin io.Reader, stdout, _ io.Writer) int {
	token := os.Getenv("LECTERN_HOOK_TOKEN")
	base := strings.TrimRight(os.Getenv("LECTERN_HOOK_URL"), "/")
	event := ""
	if len(args) > 0 {
		event = args[0]
	}
	timeout := 8
	if len(args) > 1 {
		if n, ok := pyInt(args[1]); ok && n.IsInt64() {
			timeout = int(n.Int64())
		} else if !ok {
			timeout = 8
		} else {
			timeout = -1 // absurdly large: curl refuses it, the script prints {}
		}
	}
	if token == "" || base == "" || event == "" {
		fmt.Fprintln(stdout, "{}")
		return 0
	}
	body, _ := io.ReadAll(stdin)
	var out []byte
	switch {
	case timeout > 0:
		out = curlPost(timeout, token, base+"/"+event, body)
	case timeout == 0:
		// curl -m 0 has no limit; the script's own subprocess timeout
		// (timeout + 2) then ends it after two seconds.
		out = curlPostWithin(2*time.Second, token, base+"/"+event, body)
	}
	text := pyStrip(pyDecodeReplace(out))
	if text == "" {
		text = "{}"
	}
	fmt.Fprintln(stdout, text)
	return 0
}

func curlPostWithin(d time.Duration, token, rawURL string, body []byte) []byte {
	done := make(chan []byte, 1)
	go func() { done <- curlPost(0, token, rawURL, body) }()
	select {
	case out := <-done:
		return out
	case <-time.After(d):
		return nil
	}
}

// pyInt is int(str) in base 10.
func pyInt(s string) (*big.Int, bool) {
	s = pyStrip(s)
	clean, ok := pyUnderscores(s)
	if !ok || clean == "" {
		return nil, false
	}
	digits := strings.TrimLeft(clean, "+-")
	if len(clean)-len(digits) > 1 || digits == "" {
		return nil, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return nil, false
		}
	}
	n, ok := new(big.Int).SetString(clean, 10)
	return n, ok
}

// codexHookEvents is DESIRED in codexHooksInstallPy.
var codexHookEvents = []struct {
	name    string
	timeout int
}{
	{"SessionStart", 8},
	{"UserPromptSubmit", 8},
	{"PreToolUse", 3},
	{"PostToolUse", 3},
	{"Stop", 8},
	{"SessionEnd", 8},
	{"PreCompact", 8},
	{"PermissionRequest", 130},
}

// codexHookMarker is in every hooks.json command the Go installer writes, so
// a later install finds its own groups even after the binary moved.
const codexHookMarker = " helper codex-hook "

// codexHooksInstall takes the script's arguments — "1"/"0" for
// PermissionRequest and the Python handler's path — plus an optional lectern
// binary path. Without it the result is byte-for-byte the script's; with it
// each group runs `LECTERN helper codex-hook EVENT TIMEOUT`, groups naming
// the Python handler are replaced, and ~/.lectern/hooks is created (the
// script relied on the shell step that wrote the handler).
func codexHooksInstall(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: codex-hooks-install ASK SCRIPT_PATH [LECTERN]")
		return 1
	}
	ask, scriptPath := args[0] == "1", args[1]
	lectern := ""
	if len(args) > 2 {
		lectern = args[2]
	}
	fail := func(err any) int {
		fmt.Fprintln(stderr, "codex-hooks-install:", err)
		return 1
	}

	home := pyHome()
	hooksDir := pyJoin(home, ".lectern", "hooks")
	if lectern != "" {
		if err := pyMakedirs(hooksDir, 0o700); err != nil {
			return fail(err)
		}
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = pyJoin(home, ".codex")
	}
	if err := pyMakedirs(codexHome, 0o700); err != nil {
		return fail(err)
	}
	hooksPath := pyJoin(codexHome, "hooks.json")

	doc := newPyDict()
	if raw, err := os.ReadFile(hooksPath); err == nil {
		if v, err := pyJSONLoadBytes(raw); err == nil {
			if d, ok := v.(*pyDict); ok {
				doc = d
			}
		}
	}
	events, ok := doc.vals["hooks"].(*pyDict)
	if !ok {
		events = newPyDict()
	}
	doc.set("hooks", events)

	isOurs := func(group any) (bool, bool) {
		g, ok := group.(*pyDict)
		if !ok {
			return false, true
		}
		hooksVal, present := g.vals["hooks"]
		if !present {
			return false, true
		}
		switch hs := hooksVal.(type) {
		case string, *pyDict:
			return false, true // iterates characters or keys: never a dict
		case []any:
			for _, item := range hs {
				h, ok := item.(*pyDict)
				if !ok || h.vals["type"] != "command" {
					continue
				}
				mine, ok := pyContains(h.vals["command"], scriptPath)
				if !ok {
					return false, false
				}
				if !mine && lectern != "" {
					if cmd, isStr := h.vals["command"].(string); isStr && strings.Contains(cmd, codexHookMarker) {
						mine = true
					}
				}
				if mine {
					return true, true
				}
			}
			return false, true
		default:
			return false, false // not iterable: a TypeError in the script
		}
	}

	for _, e := range codexHookEvents {
		existing, _ := events.vals[e.name].([]any)
		kept := []any{}
		for _, g := range existing {
			mine, ok := isOurs(g)
			if !ok {
				return fail("an existing hook group is malformed")
			}
			if !mine {
				kept = append(kept, g)
			}
		}
		if e.name == "PermissionRequest" && !ask {
			if len(kept) > 0 {
				events.set(e.name, kept)
			} else {
				events.pop(e.name)
			}
			continue
		}
		command := "python3 " + pyShlexQuote(scriptPath) + " " + e.name + " " + strconv.Itoa(e.timeout)
		if lectern != "" {
			command = Invocation(lectern, "codex-hook", []string{e.name, strconv.Itoa(e.timeout)})
		}
		hook := newPyDict()
		hook.set("type", "command")
		hook.set("command", command)
		group := newPyDict()
		group.set("hooks", []any{hook})
		events.set(e.name, append(kept, group))
	}

	text, err := pyJSONDumps(doc, 2, true)
	if err == nil {
		err = pyReplaceFile(hooksDir, hooksPath, []byte(text))
	}
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout, hooksPath)
	return 0
}

// pyContains is `needle in (value or "")`. ok is false where Python raises.
func pyContains(value any, needle string) (bool, bool) {
	if !pyTruthy(value) {
		return needle == "", true
	}
	switch v := value.(type) {
	case string:
		return strings.Contains(v, needle), true
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == needle {
				return true, true
			}
		}
		return false, true
	case *pyDict:
		_, ok := v.vals[needle]
		return ok, true
	}
	return false, false
}
