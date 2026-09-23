package agentevents

import "github.com/JeremiahM37/lectern/v2/internal/shellq"

// ClaudeSettingsInstallCommand returns a shell command that, run on a
// session's target (the same executor.Run seam internal/sessions/agents.go's
// claudeTrust/codexTrust probes use), writes a per-session Claude settings
// file plus a statusline script under ~/.lectern/hooks/ and prints the
// settings file's absolute path on stdout — which the caller passes to
// `claude --settings <path>`.
//
// Per docs/agent-events.md section 2, `--settings` MERGES with the user's
// own settings rather than replacing them, so this file only ever adds the
// hooks/statusLine lectern needs; it never touches ~/.claude/settings.json,
// which is read-only here (to learn an existing statusLine command so this
// session's wrapper can still run it — see the generated script).
func ClaudeSettingsInstallCommand(tmuxName, hookURL string, askPermission bool) string {
	ask := "0"
	if askPermission {
		ask = "1"
	}
	header := "python3 - " + shellq.Quote(tmuxName) + " " + shellq.Quote(hookURL) + " " + ask + " <<'ADKHOOKINSTALL'\n"
	return header + claudeSettingsInstallPy + "\nADKHOOKINSTALL"
}

// claudeSettingsInstallPy takes three positional args: tmux session name,
// hook base URL (".../api/hook/session/<id>"), and "1"/"0" for whether the
// PermissionRequest hook should be registered (permission mode "ask").
//
// Written through a temp file and rename, same reasoning as claudeTrust in
// internal/sessions/agents.go: Claude Code writes ~/.claude.json
// continuously, and while this is a different file, the habit of never
// leaving a half-written JSON file for a CLI to trip over is worth keeping
// everywhere lectern writes into an agent's config surface.
const claudeSettingsInstallPy = `import json, os, shlex, sys, tempfile

tmux_name, hook_url, ask = sys.argv[1], sys.argv[2], sys.argv[3] == "1"

home = os.path.expanduser("~")
hooks_dir = os.path.join(home, ".lectern", "hooks")
os.makedirs(hooks_dir, mode=0o700, exist_ok=True)
settings_path = os.path.join(hooks_dir, tmux_name + ".json")
statusline_path = os.path.join(hooks_dir, tmux_name + "-statusline.sh")

# Read-only: learn the operator's own statusLine command (if any) so the
# generated wrapper can still run it after reporting to lectern.
user_cmd = ""
config_dir = os.environ.get("CLAUDE_CONFIG_DIR")
user_settings_path = os.path.join(os.path.expanduser(config_dir), "settings.json") if config_dir \
    else os.path.join(home, ".claude", "settings.json")
try:
    with open(user_settings_path) as f:
        user_doc = json.load(f)
    sl = user_doc.get("statusLine")
    if isinstance(sl, dict) and sl.get("type") == "command" and sl.get("command"):
        user_cmd = sl["command"]
except Exception:
    pass


def http_hook(event, timeout):
    return {"hooks": [{
        "type": "http",
        "url": hook_url + "/" + event,
        "headers": {"Authorization": "Bearer $LECTERN_HOOK_TOKEN"},
        "allowedEnvVars": ["LECTERN_HOOK_TOKEN"],
        "timeout": timeout,
    }]}


def tool_hook(event, timeout):
    entry = http_hook(event, timeout)
    entry["matcher"] = "*"
    return entry


hooks = {
    "SessionStart": [http_hook("SessionStart", 8)],
    "UserPromptSubmit": [http_hook("UserPromptSubmit", 8)],
    # PreToolUse sits on the tool-call hot path; a slow lectern must never
    # meaningfully delay the agent, hence the short timeout.
    "PreToolUse": [tool_hook("PreToolUse", 3)],
    "PostToolUse": [tool_hook("PostToolUse", 3)],
    "Notification": [http_hook("Notification", 8)],
    "Stop": [http_hook("Stop", 8)],
    "StopFailure": [http_hook("StopFailure", 8)],
    "PreCompact": [http_hook("PreCompact", 8)],
    "SessionEnd": [http_hook("SessionEnd", 8)],
}
if ask:
    # 130s, deliberately above LECTERN_APPROVAL_HOLD's 120s default: this
    # pass answers every PermissionRequest with {} immediately (see
    # agentevents.IngestEvent), but a later worker holds the HTTP request for
    # a real decision, and the hook's OWN client timeout must not cut that
    # hold short once it exists.
    hooks["PermissionRequest"] = [tool_hook("PermissionRequest", 130)]

statusline_script = "#!/bin/sh\n" \
    "INPUT=$(cat)\n" \
    "if command -v curl >/dev/null 2>&1; then\n" \
    "  printf %s \"$INPUT\" | curl -s -m 2 -X POST" \
    " -H \"Authorization: Bearer $LECTERN_HOOK_TOKEN\" -H \"Content-Type: application/json\"" \
    " --data-binary @- " + shlex.quote(hook_url + "/statusline") + " >/dev/null 2>&1 &\n" \
    "fi\n"
if user_cmd:
    # Run the operator's own statusline exactly as Claude Code would have,
    # piping it the same stdin we already consumed into $INPUT.
    statusline_script += "printf %s \"$INPUT\" | " + user_cmd + "\n"
else:
    statusline_script += (
        "printf %s \"$INPUT\" | python3 -c '\n"
        "import json, sys\n"
        "try:\n"
        "    d = json.load(sys.stdin)\n"
        "except Exception:\n"
        "    sys.exit(0)\n"
        "model = (d.get(\"model\") or {}).get(\"display_name\") or (d.get(\"model\") or {}).get(\"id\") or \"?\"\n"
        "ctx = (d.get(\"context_window\") or {}).get(\"used_percentage\")\n"
        "cost = (d.get(\"cost\") or {}).get(\"total_cost_usd\")\n"
        "parts = [model]\n"
        "if ctx is not None:\n"
        "    parts.append(str(ctx) + \"% ctx\")\n"
        "if cost is not None:\n"
        "    parts.append(\"$%.2f\" % cost)\n"
        "print(\" \\u00b7 \".join(parts))\n"
        "'\n"
    )

fd, tmp = tempfile.mkstemp(dir=hooks_dir)
with os.fdopen(fd, "w") as f:
    json.dump({"hooks": hooks, "statusLine": {"type": "command", "command": statusline_path}}, f, indent=2)
os.replace(tmp, settings_path)

fd, tmp = tempfile.mkstemp(dir=hooks_dir)
with os.fdopen(fd, "w") as f:
    f.write(statusline_script)
os.replace(tmp, statusline_path)
os.chmod(statusline_path, 0o700)

print(settings_path)
`
