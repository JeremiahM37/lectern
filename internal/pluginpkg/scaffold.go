package pluginpkg

import (
	"fmt"
	"strings"
)

// Scaffold is what `lectern plugin new ID` writes: a manifest with one skill,
// one quick command and one host hook, each a working example of its kind.
func Scaffold(id string) ([]File, error) {
	if !idRe.MatchString(id) || strings.HasPrefix(id, ReservedPrefix) {
		return nil, fmt.Errorf("plugin id %q must be lowercase letters, digits and '-', optionally 'publisher.name', and not %q", id, ReservedPrefix)
	}
	name := id
	if i := strings.LastIndex(id, "."); i >= 0 {
		name = id[i+1:]
	}
	manifest := fmt.Sprintf(`# A Lectern plugin. See docs/plugins.md for every field.
id: %[1]s
name: %[2]s
version: 0.1.0
description: What this plugin does, in one line.
author: You
license: MIT

# Declare only what the contributions below need; install shows this list.
capabilities:
  host_exec: true          # the hook below runs on the Lectern server
  notify: true             # and its result may send a notification

contributes:
  skills:
    - id: %[2]s
      path: skills/%[2]s
      description: Tell agents when to use this skill.
  quick_commands:
    - {id: hello, label: Hello, text: "say hello", enter: true}
  hooks:
    - event: task.finished
      run: host
      command: [python3, hooks/on_finish.py]
      timeout: 30
`, id, name)
	skill := fmt.Sprintf(`---
name: %[1]s
description: Tell agents when to use this skill.
---

# %[1]s

Instructions the agent follows when it uses this skill.
`, name)
	hook := `#!/usr/bin/env python3
"""Runs when a task finishes. The event is JSON on stdin; print one JSON result."""
import json
import sys

event = json.load(sys.stdin)
task = event.get("data") or {}
print(json.dumps({"ok": True, "message": "saw task %s" % task.get("id"),
                  "notify": "Task finished: %s" % task.get("title", "")}))
`
	return []File{
		{Path: ManifestName, Mode: 0o644, Data: []byte(manifest)},
		{Path: "skills/" + name + "/SKILL.md", Mode: 0o644, Data: []byte(skill)},
		{Path: "hooks/on_finish.py", Mode: 0o755, Data: []byte(hook)},
	}, nil
}
