package helpers

import (
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
)

// claude-settings-install is agentevents' claudeSettingsInstallPy: it writes a
// session's Claude settings file (hooks + statusLine) and statusline wrapper
// under ~/.lectern/hooks and prints the settings path.
//
// Arguments are the script's — tmux session name, hook base URL, "1"/"0" for
// the PermissionRequest hook — plus an optional lectern binary path. Without
// it the files are byte-for-byte the script's; with it the default status
// line runs `LECTERN helper claude-statusline` instead of `python3 -c`.
//
// claude-statusline is that default status line: model · context · cost.
func init() {
	Register("claude-settings-install", claudeSettingsInstall)
	Register("claude-statusline", claudeStatusline)
}

func claudeSettingsInstall(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		fmt.Fprintln(stderr, "usage: claude-settings-install TMUX_NAME HOOK_URL ASK [LECTERN]")
		return 1
	}
	tmuxName, hookURL, ask := args[0], args[1], args[2] == "1"
	lectern := ""
	if len(args) > 3 {
		lectern = args[3]
	}

	home := pyHome()
	hooksDir := pyJoin(home, ".lectern", "hooks")
	if err := pyMakedirs(hooksDir, 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	settingsPath := pyJoin(hooksDir, tmuxName+".json")
	statuslinePath := pyJoin(hooksDir, tmuxName+"-statusline.sh")

	// Read-only: learn the operator's own statusLine command (if any) so the
	// generated wrapper can still run it after reporting to lectern.
	var userCmd any = ""
	userSettings := pyJoin(home, ".claude", "settings.json")
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		userSettings = pyJoin(pyExpanduser(dir), "settings.json")
	}
	if raw, err := os.ReadFile(userSettings); err == nil {
		if doc, err := pyJSONLoadBytes(raw); err == nil {
			if d, ok := doc.(*pyDict); ok {
				if sl, ok := d.vals["statusLine"].(*pyDict); ok {
					if sl.vals["type"] == "command" && pyTruthy(sl.vals["command"]) {
						userCmd = sl.vals["command"]
					}
				}
			}
		}
	}

	httpHook := func(event string, timeout int) *pyDict {
		h := newPyDict()
		h.set("type", "http")
		h.set("url", hookURL+"/"+event)
		headers := newPyDict()
		headers.set("Authorization", "Bearer $LECTERN_HOOK_TOKEN")
		h.set("headers", headers)
		h.set("allowedEnvVars", []any{"LECTERN_HOOK_TOKEN"})
		h.set("timeout", timeout)
		entry := newPyDict()
		entry.set("hooks", []any{h})
		return entry
	}
	toolHook := func(event string, timeout int) *pyDict {
		entry := httpHook(event, timeout)
		entry.set("matcher", "*")
		return entry
	}
	hooks := newPyDict()
	hooks.set("SessionStart", []any{httpHook("SessionStart", 8)})
	hooks.set("UserPromptSubmit", []any{httpHook("UserPromptSubmit", 8)})
	hooks.set("PreToolUse", []any{toolHook("PreToolUse", 3)})
	hooks.set("PostToolUse", []any{toolHook("PostToolUse", 3)})
	hooks.set("Notification", []any{httpHook("Notification", 8)})
	hooks.set("Stop", []any{httpHook("Stop", 8)})
	hooks.set("StopFailure", []any{httpHook("StopFailure", 8)})
	hooks.set("PreCompact", []any{httpHook("PreCompact", 8)})
	hooks.set("SessionEnd", []any{httpHook("SessionEnd", 8)})
	if ask {
		hooks.set("PermissionRequest", []any{toolHook("PermissionRequest", 130)})
	}

	script := "#!/bin/sh\n" +
		"INPUT=$(cat)\n" +
		"if command -v curl >/dev/null 2>&1; then\n" +
		"  printf %s \"$INPUT\" | curl -s -m 2 -X POST" +
		" -H \"Authorization: Bearer $LECTERN_HOOK_TOKEN\" -H \"Content-Type: application/json\"" +
		" --data-binary @- " + pyShlexQuote(hookURL+"/statusline") + " >/dev/null 2>&1 &\n" +
		"fi\n"
	switch cmd := userCmd.(type) {
	case string:
		if cmd != "" {
			script += "printf %s \"$INPUT\" | " + cmd + "\n"
		} else if lectern != "" {
			script += "printf %s \"$INPUT\" | " + Invocation(lectern, "claude-statusline", nil) + "\n"
		} else {
			script += claudeStatuslinePython
		}
	default:
		// str + a non-string command: a TypeError in the script.
		fmt.Fprintln(stderr, "claude-settings-install: statusLine.command is not a string")
		return 1
	}

	settings := newPyDict()
	settings.set("hooks", hooks)
	statusLine := newPyDict()
	statusLine.set("type", "command")
	statusLine.set("command", statuslinePath)
	settings.set("statusLine", statusLine)
	text, err := pyJSONDumps(settings, 2, true)
	if err == nil {
		err = pyReplaceFile(hooksDir, settingsPath, []byte(text))
	}
	if err == nil {
		err = pyReplaceFile(hooksDir, statuslinePath, []byte(script))
	}
	if err == nil {
		err = os.Chmod(statuslinePath, 0o700)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, settingsPath)
	return 0
}

// claudeStatuslinePython is the default status line the script writes when
// the target has no lectern binary.
const claudeStatuslinePython = "printf %s \"$INPUT\" | python3 -c '\n" +
	"import json, sys\n" +
	"try:\n" +
	"    d = json.load(sys.stdin)\n" +
	"except Exception:\n" +
	"    sys.exit(0)\n" +
	"model = (d.get(\"model\") or {}).get(\"display_name\") or (d.get(\"model\") or {}).get(\"id\") or \"?\"\n" +
	"ctx = (d.get(\"context_window\") or {}).get(\"used_percentage\")\n" +
	"cost = (d.get(\"cost\") or {}).get(\"total_cost_usd\")\n" +
	"parts = [model]\n" +
	"if ctx is not None:\n" +
	"    parts.append(str(ctx) + \"% ctx\")\n" +
	"if cost is not None:\n" +
	"    parts.append(\"$%.2f\" % cost)\n" +
	"print(\" \\u00b7 \".join(parts))\n" +
	"'\n"

func claudeStatusline(_ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return 0
	}
	doc, err := pyJSONLoadBytes(raw)
	if err != nil {
		return 0
	}
	fail := func(why string) int {
		fmt.Fprintln(stderr, "claude-statusline:", why)
		return 1
	}
	d, ok := doc.(*pyDict)
	if !ok {
		return fail("status is not a JSON object")
	}
	// section is `(d.get(name) or {})`, which must then support .get.
	section := func(name string) (*pyDict, bool) {
		v := d.vals[name]
		if !pyTruthy(v) {
			return newPyDict(), true
		}
		s, ok := v.(*pyDict)
		return s, ok
	}
	m, ok := section("model")
	if !ok {
		return fail("model is not an object")
	}
	model := m.vals["display_name"]
	if !pyTruthy(model) {
		model = m.vals["id"]
	}
	if !pyTruthy(model) {
		model = "?"
	}
	name, ok := model.(string)
	if !ok {
		return fail("model name is not a string")
	}
	parts := []string{name}
	cw, ok := section("context_window")
	if !ok {
		return fail("context_window is not an object")
	}
	c, ok := section("cost")
	if !ok {
		return fail("cost is not an object")
	}
	if ctx, present := cw.vals["used_percentage"]; present && ctx != nil {
		parts = append(parts, pyStr(ctx)+"% ctx")
	}
	if cost, present := c.vals["total_cost_usd"]; present && cost != nil {
		s, ok := pyPercentF2(cost)
		if !ok {
			return fail("total_cost_usd is not a number")
		}
		parts = append(parts, "$"+s)
	}
	fmt.Fprintln(stdout, strings.Join(parts, " · "))
	return 0
}

// pyPercentF2 is "%.2f" % value.
func pyPercentF2(v any) (string, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case bool:
		if x {
			f = 1
		}
	case *big.Int:
		bf, _ := new(big.Float).SetInt(x).Float64()
		if math.IsInf(bf, 0) {
			return "", false // OverflowError
		}
		f = bf
	default:
		return "", false
	}
	switch {
	case math.IsNaN(f):
		return "nan", true
	case math.IsInf(f, 1):
		return "inf", true
	case math.IsInf(f, -1):
		return "-inf", true
	}
	return strconv.FormatFloat(f, 'f', 2, 64), true
}
