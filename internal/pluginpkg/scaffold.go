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

// ScaffoldMod is what `lectern plugin new ID --mod` writes: a plugin with one
// mod (docs/mods.md) that runs in the browser and the console, showing each
// kind of hook a mod usually starts from — a status segment, a badge on every
// session card and a palette command — and a test for it.
func ScaffoldMod(id string) ([]File, error) {
	if !idRe.MatchString(id) || strings.HasPrefix(id, ReservedPrefix) {
		return nil, fmt.Errorf("plugin id %q must be lowercase letters, digits and '-', optionally 'publisher.name', and not %q", id, ReservedPrefix)
	}
	name := id
	if i := strings.LastIndex(id, "."); i >= 0 {
		name = id[i+1:]
	}
	manifest := fmt.Sprintf(`# A Lectern plugin with one mod. See docs/plugins.md and docs/mods.md.
id: %[1]s
name: %[2]s
version: 0.1.0
description: What this mod changes, in one line.
author: You
license: MIT

capabilities:
  mods: [web, cli]         # its code runs in your browser and in lectern console
  # api: read              # uncomment to let $.api read Lectern's API

contributes:
  mods:
    - id: %[2]s
      path: mods/%[2]s.js
      surfaces: [web, cli]
`, id, name)
	mod := fmt.Sprintf(`// %[1]s: a Lectern mod. docs/mods.md lists every event, element and $ call.
// It runs the same in the web app and in `+"`lectern console`"+`.
export function register(on) {
  // app.start runs once when the page or the console starts.
  on("app.start", async ($, e, next) => {
    $.command.register({
      id: "%[1]s.toggle",
      title: "%[1]s: show or hide agent badges",
      run: ($) => {
        const shown = $.state.get("badges") !== false;
        $.state.set("badges", !shown); // set() also draws everything again
        $.ui.toast(shown ? "Agent badges hidden" : "Agent badges shown");
      },
    });
    return next(e);
  });

  // A segment of the top bar (web) or the footer (console).
  on("ui.render", "status", async ($, e, next) => {
    const out = await next(e);
    const { Text } = $.ui.resolve(e);
    out.append.push(Text({ text: "%[1]s", tone: $.state.get("badges") === false ? "dim" : "accent" }));
    return out;
  });

  // A badge on every session card, naming the session's agent.
  on("ui.render", "session.card", async ($, e, next) => {
    const out = await next(e);
    if ($.state.get("badges") !== false) {
      const { Badge } = $.ui.resolve(e);
      out.append.push(Badge({ text: e.props.session.agent || "shell", tone: "accent" }));
    }
    return out;
  });
}
`, name)
	test := fmt.Sprintf(`// Run with: node --test mods/
// A stand-in for Lectern: it collects the handlers and calls them as the
// chain would, with next() returning Lectern's default.
import { test } from "node:test";
import assert from "node:assert/strict";
import { register } from "./%[1]s.js";

function harness() {
  const handlers = [];
  register((event, matcher, fn) => handlers.push({ event, matcher: fn ? matcher : null, fn: fn || matcher }));
  const store = {};
  const commands = {};
  const $ = {
    surface: "cli",
    ui: {
      resolve: () => Object.fromEntries(["Box", "Text", "Badge", "Button", "Link"].map((type) => [type, (props) => ({ type, props })])),
      toast() {}, status() {}, open() {}, close() {}, render() {},
    },
    state: { get: (k) => store[k], set: (k, v) => { store[k] = v; } },
    command: { register: (c) => { commands[c.id] = c; } },
  };
  const fire = async (event, e, def) => {
    const list = handlers.filter((h) => h.event === event && (!h.matcher || h.matcher === e.component));
    const call = (i, ev) => (i === list.length ? def(ev) : list[i].fn($, ev, (n) => call(i + 1, n)));
    return call(0, e);
  };
  return { $, fire, commands };
}

test("badges each session card with its agent", async () => {
  const { fire } = harness();
  const out = await fire("ui.render", { component: "session.card", props: { session: { agent: "codex" } } }, () => ({ hidden: false, append: [] }));
  assert.deepEqual(out.append, [{ type: "Badge", props: { text: "codex", tone: "accent" } }]);
});

test("the palette command turns the badges off", async () => {
  const { $, fire, commands } = harness();
  await fire("app.start", { surface: "cli" }, () => {});
  commands["%[1]s.toggle"].run($);
  const out = await fire("ui.render", { component: "session.card", props: { session: { agent: "codex" } } }, () => ({ hidden: false, append: [] }));
  assert.equal(out.append.length, 0);
});
`, name)
	readme := fmt.Sprintf(`# %[1]s

A Lectern plugin with one mod, `+"`mods/%[1]s.js`"+`: it adds a status segment, a badge
on every session card, and a palette command that toggles the badges. See
docs/mods.md in Lectern for every event, element and `+"`$`"+` call.

`+"```sh"+`
node --test mods/                # the mod's own test
lectern plugin validate .
lectern plugin install .         # preview, then Allow
`+"```"+`
`, name)
	return []File{
		{Path: ManifestName, Mode: 0o644, Data: []byte(manifest)},
		{Path: "mods/" + name + ".js", Mode: 0o644, Data: []byte(mod)},
		{Path: "mods/" + name + ".test.mjs", Mode: 0o644, Data: []byte(test)},
		{Path: "README.md", Mode: 0o644, Data: []byte(readme)},
	}, nil
}
