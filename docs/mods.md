# Mods

A mod is a small JavaScript module, shipped inside a [plugin](plugins.md), that
changes how Lectern behaves and looks: it watches what happens, rewrites or
blocks it, draws its own buttons, badges and panes, and adds commands. The same
module runs in the web app and in the terminal console (`lectern console`).

```js
// mods/blast-radius.js
export function register(on) {
  on("prompt.submit", async ($, e, next) => {
    if (/rm\s+-rf\s+\//.test(e.text)) return { deny: "That would delete the whole disk." };
    return next(e);
  });
  on("ui.render", "session.card", async ($, e, next) => {
    const out = await next(e);
    const { Badge } = $.ui.resolve(e);
    if (e.props.session.agent === "codex") out.append.push(Badge({ text: "codex", tone: "accent" }));
    return out;
  });
}
```

Code: `internal/pluginpkg` (manifest, validation), `internal/plugins/mods.go`
(serving), `frontend/src/mods/` (web runtime), `internal/console/mods/`
(console runtime).

## The package

```yaml
id: acme.blast-radius
name: Blast radius
version: 0.1.0
capabilities:
  mods: [web, cli]      # where its code runs: your browser, your terminal console
  api: read             # optional: $.api may read Lectern's API ("write" may also change things)
contributes:
  mods:
    - id: blast-radius
      path: mods/blast-radius.js
      surfaces: [web, cli]   # default: both
```

`lectern plugin validate` refuses a mod whose surfaces the `mods` capability
does not list, a file that is missing, larger than 256 KB, not `.js`/`.mjs`, or
that does not parse. A mod is one file: it may not `import` anything (bundle
with esbuild first if you need a library). It defines `register`, either as
`export function register(on, options)`, `export default function (on, options)`,
or a plain `function register(on, options)`.

Install, consent, updates and the content hash work exactly as for every other
plugin: the code that runs is the code a person looked at and allowed, and a
changed file stops the mod until someone trusts it again.

## Hooks

`on(event, [matcher], handler)` adds a handler. Handlers of every mod form one
chain per event, in install order; `next(e)` passes the event on, and the end
of the chain is Lectern itself. A handler can:

- **observe**: `const r = await next(e); …; return r;`
- **rewrite**: `return next({ ...e, text: e.text.trim() });`
- **answer**: return without calling `next`, e.g. `return { deny: "reason" }`.

`matcher` narrows an event: the component name for `ui.render`, the command id
for `command.run`, the stream type for `server.event`. It is a string or a
RegExp.

| Event | `e` | What Lectern does at the end of the chain | A handler may |
| --- | --- | --- | --- |
| `app.start` | `{surface, version}` | nothing | observe |
| `server.event` | `{type, data}` — every live update (`session`, `task`, `approval`, …) | nothing | observe |
| `prompt.submit` | `{session_id, text}` — a message a person sends to a session | sends `text` | rewrite `text`, or `{deny}` |
| `approval.decide` | `{approval, decision}` — `decision` is `allow` or `deny` | records the decision | `{deny}` to stop it (a person decides again) |
| `command.run` | `{id, args}` — a command from the palette | runs it | rewrite, or answer |
| `ui.render` | `{component, props, surface, viewport}` | returns `{hidden:false, append:[]}` | hide the component, append elements |

`{deny}` is a safety net for the person at the keyboard, not a permission
system: agents never go through these hooks. Use permission rules for a hard
block. In the web app `prompt.submit` and `approval.decide` are hooked in the
API client every screen uses; the approval strip inside a terminal tab and
keys typed straight into a terminal do not pass through them.

### Components

| `component` | Where | `props` |
| --- | --- | --- |
| `session.card` | a session in Sessions (web) and the console list (cli) | `{session}` |
| `status` | the top bar (web) and the console footer (cli) | `{}` |
| `pane` | a pane opened with `$.ui.open` | `{id}` |

For `pane` the result's `append` is the pane's content.

## `$`

The module runs in a sandbox of its own: in the browser, an opaque-origin
iframe with no access to Lectern's page, cookies or network; in the console,
an embedded JavaScript engine with no file system or network. Everything goes
through `$`.

| | |
| --- | --- |
| `$.surface` | `"web"` or `"cli"` |
| `$.mod` | `{plugin, id}` |
| `$.ui.resolve(e)` | element constructors: `Box`, `Text`, `Badge`, `Button`, `Link` |
| `$.ui.toast(text)` | a notification |
| `$.ui.status(text)` | this mod's segment of the status component (`null` clears it) |
| `$.ui.open({id, title})` / `$.ui.close(id)` | a pane, drawn by `ui.render` with `component: "pane"` |
| `$.ui.render()` | draw again (`$.state.set` does this for you) |
| `$.state.get(key)` / `$.state.set(key, value)` | JSON values kept for this mod on this device |
| `$.command.register({id, title, run})` | a palette command; `run($, args)` |
| `$.navigate(hash)` | open a Lectern view (`#sessions`, `#session/4`); the console ignores it |
| `$.api.get(path)` | needs `api: read`; `path` starts with `/api/` |
| `$.api.post/put/delete(path, body)` | needs `api: write` |
| `$.sleep(ms)` | wait |
| `$.log(...)` | shown under the mod in Settings → Plugins |

The API is called as the person using the app. Plugin management, approval
decisions, sign-in and secrets are never reachable from a mod.

### Elements

Elements are plain objects, the same on both surfaces; the console draws what
a terminal can and ignores the rest.

| | props |
| --- | --- |
| `Box` | `direction` (`row`/`column`), `gap`, `children` |
| `Text` | `text`, `tone` (`dim`, `accent`, `warn`, `danger`, `ok`), `bold`, `mono` |
| `Badge` | `text`, `tone` |
| `Button` | `label`, `onPress`, `hotkey` (console) |
| `Link` | `label`, `href` (`#view` or `https://`) |

## In the console

`lectern console` runs the same modules on an embedded JavaScript engine, one
per mod, with no file system or network. It differs from the web app where a
terminal has to:

- It polls rather than streams, so a newly enabled mod starts within about 15
  seconds, and `server.event` carries only `session` (a status changed) and
  `approval` (a new one is waiting).
- A session row draws its mod elements after the title on one line. Its
  buttons are in the palette rather than on hotkeys, which would clash with
  the dashboard's own; inside a pane, `hotkey` works and Esc closes it.
- `Link` is drawn as its label, and `Text`'s `mono` is ignored.
- `lectern console --plain` does not load mods.

State lives beside the console's saved view, in your config directory.

## Limits

- A handler has 2 seconds; one that throws or runs over is skipped (the event
  continues as if it had called `next(e)`) and the error is recorded. A mod
  that fails 5 times in a minute is paused until the page or console restarts.
- `ui.render` handlers are synchronous in practice: a render waits at most
  100 ms for them and draws without the late ones.
- State is at most 256 KB per mod.

## Writing one

```sh
lectern plugin new my-mod --mod    # a plugin with one mod and a test
lectern plugin validate ./my-mod
lectern plugin install ./my-mod    # preview, then Allow
```
