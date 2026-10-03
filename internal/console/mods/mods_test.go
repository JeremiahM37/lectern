package mods

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
)

// mod converts module source exactly as the server does before serving it.
func mod(t *testing.T, id, api, src string) Mod {
	t.Helper()
	script, err := pluginpkg.ModScript(src)
	if err != nil {
		t.Fatalf("ModScript(%s): %v", id, err)
	}
	return Mod{Plugin: "test." + id, ID: id, Name: id, API: api, Hash: "h-" + id, Script: script}
}

func host(t *testing.T, opts Options, mods ...Mod) *Host {
	t.Helper()
	h := NewHost(opts)
	t.Cleanup(h.Close)
	h.Load(mods)
	if p := h.Problems(); len(p) > 0 {
		t.Fatalf("load problems: %v", p)
	}
	return h
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// sender is a prompt.submit default that records what Lectern would send.
type sender struct {
	mu    sync.Mutex
	texts []string
}

func (s *sender) send(e map[string]any) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, e["text"].(string))
	return map[string]any{"sent": true}, nil
}

func (s *sender) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.texts...)
}

func shorten(t *testing.T, handler time.Duration) {
	old := handlerTimeout
	handlerTimeout = handler
	t.Cleanup(func() { handlerTimeout = old })
}

func TestChainRewritesObservesAndDenies(t *testing.T) {
	trim := mod(t, "trim", "", `export function register(on) {
  on("prompt.submit", async ($, e, next) => next({ ...e, text: e.text.trim() }));
}`)
	observe := mod(t, "observe", "", `export default function (on) {
  on("prompt.submit", async ($, e, next) => {
    const r = await next(e);
    return { ...r, seen: e.text };
  });
}`)
	guard := mod(t, "guard", "", `function register(on) {
  on("prompt.submit", ($, e, next) => /rm\s+-rf\s+\//.test(e.text) ? { deny: "That would delete the whole disk." } : next(e));
}`)
	h := host(t, Options{}, trim, observe, guard)
	var s sender
	res, err := h.Dispatch(context.Background(), "prompt.submit", map[string]any{"session_id": "4", "text": "  hello  "}, s.send)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.sent(); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("sent %q, want the trimmed text once", got)
	}
	if m := res.(map[string]any); m["sent"] != true || m["seen"] != "hello" {
		t.Fatalf("observer did not see the rewritten event: %#v", res)
	}
	res, err = h.Dispatch(context.Background(), "prompt.submit", map[string]any{"session_id": "4", "text": "rm -rf /"}, s.send)
	if err != nil {
		t.Fatal(err)
	}
	if reason, ok := Denied(res); !ok || !strings.Contains(reason, "whole disk") {
		t.Fatalf("deny not returned: %#v", res)
	}
	if len(s.sent()) != 1 {
		t.Fatal("a denied prompt reached Lectern")
	}
}

func TestDefaultRunsOnceWhenNextIsCalledTwice(t *testing.T) {
	twice := mod(t, "twice", "", `export function register(on) {
  on("prompt.submit", async ($, e, next) => { await next(e); return next({ ...e, text: "again" }); });
}`)
	h := host(t, Options{}, twice)
	var s sender
	if _, err := h.Dispatch(context.Background(), "prompt.submit", map[string]any{"text": "once"}, s.send); err != nil {
		t.Fatal(err)
	}
	if got := s.sent(); len(got) != 1 || got[0] != "once" {
		t.Fatalf("default ran %q", got)
	}
}

func TestThrowingHandlerIsSkippedAndPausedAfterFiveFailures(t *testing.T) {
	bad := mod(t, "bad", "", `export function register(on) {
  on("prompt.submit", ($, e, next) => { throw new Error("boom"); });
}`)
	h := host(t, Options{}, bad)
	var s sender
	for i := 0; i < 6; i++ {
		res, err := h.Dispatch(context.Background(), "prompt.submit", map[string]any{"text": "hi"}, s.send)
		if err != nil || res.(map[string]any)["sent"] != true {
			t.Fatalf("dispatch %d: %#v %v", i, res, err)
		}
	}
	if got := s.sent(); len(got) != 6 {
		t.Fatalf("skipped handler should behave as next(e): sent %d", len(got))
	}
	if h.Handles("prompt.submit") {
		t.Fatal("mod not paused after five failures")
	}
	if p := strings.Join(h.Problems(), "\n"); !strings.Contains(p, "paused") || !strings.Contains(p, "boom") {
		t.Fatalf("problems = %q", p)
	}
}

func TestTimedOutHandlersAreSkippedAndTheRuntimeRecovers(t *testing.T) {
	shorten(t, 150*time.Millisecond)
	slow := mod(t, "slow", "", `export function register(on) {
  on("prompt.submit", async ($, e, next) => { await $.sleep(5000); return { deny: "late" }; });
  on("server.event", ($, e, next) => { while (true) {} });
  on("command.run", ($, e, next) => ({ ok: "alive" }));
}`)
	h := host(t, Options{}, slow)
	var s sender
	start := time.Now()
	res, err := h.Dispatch(context.Background(), "prompt.submit", map[string]any{"text": "hi"}, s.send)
	if err != nil || res.(map[string]any)["sent"] != true || time.Since(start) > time.Second {
		t.Fatalf("slow handler not skipped: %#v %v after %s", res, err, time.Since(start))
	}
	if _, err := h.Dispatch(context.Background(), "server.event", map[string]any{"type": "session"}, nil); err != nil {
		t.Fatal(err)
	}
	// The busy loop was interrupted, so the same runtime still answers.
	res, err = h.Dispatch(context.Background(), "command.run", map[string]any{"id": "x"}, nil)
	if err != nil || res.(map[string]any)["ok"] != "alive" {
		t.Fatalf("runtime unusable after interrupt: %#v %v", res, err)
	}
}

func TestWaitingForNextDoesNotCountAgainstTheHandler(t *testing.T) {
	shorten(t, 200*time.Millisecond)
	outer := mod(t, "outer", "", `export function register(on) {
  on("prompt.submit", async ($, e, next) => { const r = await next(e); return { ...r, outer: true }; });
}`)
	h := host(t, Options{}, outer)
	res, err := h.Dispatch(context.Background(), "prompt.submit", map[string]any{"text": "x"}, func(map[string]any) (any, error) {
		time.Sleep(400 * time.Millisecond) // Lectern's own work, longer than a handler's limit
		return map[string]any{"sent": true}, nil
	})
	if err != nil || res.(map[string]any)["outer"] != true {
		t.Fatalf("observer was cut off while waiting for next: %#v %v", res, err)
	}
	if len(h.Problems()) != 0 {
		t.Fatalf("problems = %v", h.Problems())
	}
}

func TestMatchersNarrowEvents(t *testing.T) {
	m := mod(t, "match", "", `export function register(on) {
  on("ui.render", "session.card", async ($, e, next) => {
    const out = await next(e);
    out.append.push($.ui.resolve(e).Badge({ text: "exact", tone: "accent" }));
    return out;
  });
  on("ui.render", /^sta/, async ($, e, next) => {
    const out = await next(e);
    out.append.push($.ui.resolve(e).Text("regexp"));
    return out;
  });
  on("server.event", "approval", ($, e, next) => { $.ui.status("approval " + e.data.id); return next(e); });
}`)
	h := host(t, Options{}, m)
	if _, elems := h.Render("session.card", map[string]any{"session": map[string]any{"id": 1}}); len(elems) != 1 || elems[0].Text != "exact" || elems[0].Tone != "accent" {
		t.Fatalf("session.card = %#v", elems)
	}
	if _, elems := h.Render("status", nil); len(elems) != 1 || elems[0].Text != "regexp" {
		t.Fatalf("status = %#v", elems)
	}
	if _, elems := h.Render("pane", map[string]any{"id": "x"}); len(elems) != 0 {
		t.Fatalf("pane matched neither matcher: %#v", elems)
	}
	_, _ = h.Dispatch(context.Background(), "server.event", map[string]any{"type": "session", "data": map[string]any{"id": 1}}, nil)
	_, _ = h.Dispatch(context.Background(), "server.event", map[string]any{"type": "approval", "data": map[string]any{"id": 7}}, nil)
	if got := h.Status(); len(got) != 1 || got[0] != "approval 7" {
		t.Fatalf("status = %q", got)
	}
}

func TestRenderHidesAppendsAndPressesButtons(t *testing.T) {
	m := mod(t, "cards", "", `export function register(on) {
  on("ui.render", "session.card", async ($, e, next) => {
    const out = await next(e);
    if (e.props.session.agent === "secret-agent") return { ...out, hidden: true };
    const { Badge, Button, Box } = $.ui.resolve(e);
    out.append.push(Box({ direction: "row", children: [
      Badge({ text: e.props.session.agent, tone: "ok" }),
      Button({ label: "Pin", hotkey: "p", onPress: () => $.state.set("pinned", e.props.session.id) }),
    ]}));
    return out;
  });
}`)
	h := host(t, Options{}, m)
	got := h.RenderMany("session.card", []map[string]any{
		{"session": map[string]any{"id": 1, "agent": "codex"}},
		{"session": map[string]any{"id": 2, "agent": "secret-agent"}},
	})
	if got[0].Hidden || !got[1].Hidden {
		t.Fatalf("hidden = %v, %v", got[0].Hidden, got[1].Hidden)
	}
	if len(got[0].Append) != 1 || len(got[0].Append[0].Children) != 2 || got[0].Append[0].Children[0].Text != "codex" {
		t.Fatalf("append = %#v", got[0].Append)
	}
	buttons := Buttons(got[0].Append)
	if len(buttons) != 1 || buttons[0].Label != "Pin" || buttons[0].Hotkey != "p" || !buttons[0].Pressable() {
		t.Fatalf("buttons = %#v", buttons)
	}
	buttons[0].Press()
	r := h.snapshot()[0]
	eventually(t, "button press to set state", func() bool { return r.stateGet("pinned") == float64(1) })
}

func TestRenderWaitsAtMostTheBudget(t *testing.T) {
	m := mod(t, "lazy", "", `export function register(on) {
  on("ui.render", async ($, e, next) => { await $.sleep(1000); const out = await next(e); out.append.push("late"); return out; });
}`)
	h := host(t, Options{}, m)
	start := time.Now()
	rows := make([]map[string]any, 20)
	for i := range rows {
		rows[i] = map[string]any{"session": map[string]any{"id": i}}
	}
	got := h.RenderMany("session.card", rows)
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("render took %s", d)
	}
	for _, r := range got {
		if r.Hidden || len(r.Append) != 0 {
			t.Fatalf("late handler was drawn: %#v", r)
		}
	}
	if len(h.Problems()) != 0 {
		t.Fatalf("a slow render is not a failure: %v", h.Problems())
	}
}

func TestStatePersistsAndIsCapped(t *testing.T) {
	dir := t.TempDir()
	src := `export function register(on) {
  on("command.run", "set", ($, e, next) => { $.state.set("count", ($.state.get("count") || 0) + 1); return { count: $.state.get("count") }; });
  on("command.run", "big", ($, e, next) => {
    try { $.state.set("big", "x".repeat(300 * 1024)); return { stored: true }; }
    catch (err) { return { error: String(err.message) }; }
  });
  on("command.run", "get", ($, e, next) => ({ count: $.state.get("count"), big: $.state.get("big") }));
}`
	h := host(t, Options{StateDir: dir}, mod(t, "store", "", src))
	for i := 0; i < 2; i++ {
		if _, err := h.Dispatch(context.Background(), "command.run", map[string]any{"id": "set"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	res, _ := h.Dispatch(context.Background(), "command.run", map[string]any{"id": "big"}, nil)
	if msg, _ := res.(map[string]any)["error"].(string); !strings.Contains(msg, "256 KB") {
		t.Fatalf("oversized state was not refused: %#v", res)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("state files = %v", files)
	}
	if info, _ := os.Stat(files[0]); info.Size() > 1024 {
		t.Fatalf("refused value was written: %d bytes", info.Size())
	}
	h.Close()

	again := host(t, Options{StateDir: dir}, mod(t, "store", "", src))
	res, _ = again.Dispatch(context.Background(), "command.run", map[string]any{"id": "get"}, nil)
	if m := res.(map[string]any); m["count"] != float64(2) || m["big"] != nil {
		t.Fatalf("state after restart = %#v", m)
	}
}

type fakeAPI struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeAPI) JSON(method, path string, body any) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, method+" "+path)
	f.mu.Unlock()
	if path == "/api/missing" {
		return nil, errors.New("Not Found")
	}
	return []byte(`{"ok":true,"method":"` + method + `"}`), nil
}

func apiMod(t *testing.T, id, level string) Mod {
	return mod(t, id, level, `export function register(on) {
  on("command.run", async ($, e, next) => {
    try { return { result: await $.api[e.args.method](e.args.path, { a: 1 }) }; }
    catch (err) { return { error: String(err.message) }; }
  });
}`)
}

func call(t *testing.T, h *Host, method, path string) map[string]any {
	t.Helper()
	res, err := h.Dispatch(context.Background(), "command.run", map[string]any{"id": "api", "args": map[string]any{"method": method, "path": path}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.(map[string]any)
}

func TestAPIIsGatedByTheDeclaredLevel(t *testing.T) {
	for _, tc := range []struct {
		level, method, path string
		ok                  bool
	}{
		{"", "get", "/api/sessions", false},
		{"read", "get", "/api/sessions", true},
		{"read", "post", "/api/sessions/4/send", false},
		{"read", "delete", "/api/sessions/4", false},
		{"write", "post", "/api/sessions/4/send", true},
		{"write", "put", "/api/projects/1", true},
		{"write", "delete", "/api/sessions/4", true},
		{"write", "get", "sessions", false},
		{"write", "post", "/api/approvals/9/decision", false},
		{"write", "get", "/api/plugins/mods?surface=cli", false},
		{"write", "post", "/api/plugins/x/trust", false},
		{"write", "get", "/api/auth/whoami", false},
		{"write", "get", "/api/secrets", false},
		{"write", "get", "/api/settings?show=token", false},
		{"write", "get", "/api/sessions/../plugins", false},
		{"write", "get", "/api/%70lugins", false},
		{"write", "post", "/api/accounts/1/login", false},
		{"read", "get", "/api/approvals?status=pending", true},
	} {
		api := &fakeAPI{}
		h := host(t, Options{API: api}, apiMod(t, "api", tc.level))
		got := call(t, h, tc.method, tc.path)
		if tc.ok != (got["result"] != nil) {
			t.Errorf("%s %s %s: %#v", tc.level, tc.method, tc.path, got)
		}
		if !tc.ok && len(api.calls) != 0 {
			t.Errorf("%s %s %s reached the API: %v", tc.level, tc.method, tc.path, api.calls)
		}
		h.Close()
	}
	api := &fakeAPI{}
	h := host(t, Options{API: api}, apiMod(t, "api", "read"))
	if got := call(t, h, "get", "/api/missing"); got["error"] != "Not Found" {
		t.Fatalf("API error not passed to the mod: %#v", got)
	}
}

func TestCommandsPanesAndAppStart(t *testing.T) {
	cmds := mod(t, "cmds", "", `export function register(on) {
  on("app.start", async ($, e, next) => {
    $.command.register({ id: "hello", title: "Say hello", run: async ($, args) => {
      $.ui.toast("hello " + args.who);
      $.ui.open({ id: "hello-pane", title: "Hello" });
      return { said: args.who };
    }});
    $.ui.status("ready on " + e.surface);
    return next(e);
  });
  on("ui.render", "pane", async ($, e, next) => {
    const out = await next(e);
    if (e.props.id === "hello-pane") out.append.push($.ui.resolve(e).Text({ text: "inside", bold: true }));
    return out;
  });
}`)
	shout := mod(t, "shout", "", `export function register(on) {
  on("command.run", "hello", ($, e, next) => next({ ...e, args: { who: e.args.who.toUpperCase() } }));
}`)
	h := host(t, Options{}, cmds, shout)
	eventually(t, "app.start to register a command", func() bool { return len(h.Commands()) == 1 })
	if c := h.Commands()[0]; c.ID != "hello" || c.Title != "Say hello" || c.Mod != "test.cmds/cmds" {
		t.Fatalf("command = %#v", c)
	}
	if got := h.Status(); len(got) != 1 || got[0] != "ready on cli" {
		t.Fatalf("status = %q", got)
	}
	res, err := h.RunCommand(context.Background(), "hello", map[string]any{"who": "ada"})
	if err != nil || res.(map[string]any)["said"] != "ADA" {
		t.Fatalf("command.run chain: %#v %v", res, err)
	}
	var toast Notice
	eventually(t, "toast", func() bool {
		for {
			select {
			case n := <-h.Notices():
				if n.Kind == NoticeToast {
					toast = n
					return true
				}
			default:
				return false
			}
		}
	})
	if toast.Text != "hello ADA" || toast.Mod != "cmds" {
		t.Fatalf("toast = %#v", toast)
	}
	panes := h.OpenPanes()
	if len(panes) != 1 || panes[0].Title != "Hello" {
		t.Fatalf("panes = %#v", panes)
	}
	if elems := h.RenderPane(panes[0]); len(elems) != 1 || elems[0].Text != "inside" || !elems[0].Bold {
		t.Fatalf("pane content = %#v", elems)
	}
	h.ClosePane(panes[0])
	if len(h.OpenPanes()) != 0 {
		t.Fatal("pane did not close")
	}
}

func TestReloadKeepsUnchangedModsAndStartsChangedOnes(t *testing.T) {
	a := mod(t, "a", "", `export function register(on) { on("command.run", ($, e) => ({ v: 1 })); }`)
	h := host(t, Options{}, a)
	first := h.snapshot()[0]
	if h.Load([]Mod{a}) {
		t.Fatal("an identical list reported a change")
	}
	if h.snapshot()[0] != first {
		t.Fatal("unchanged mod was restarted")
	}
	b := mod(t, "a", "", `export function register(on) { on("command.run", ($, e) => ({ v: 2 })); }`)
	b.Hash = "h-new"
	if !h.Load([]Mod{b}) {
		t.Fatal("changed hash not reported")
	}
	res, _ := h.Dispatch(context.Background(), "command.run", map[string]any{"id": "x"}, nil)
	if res.(map[string]any)["v"] != float64(2) {
		t.Fatalf("old code still runs: %#v", res)
	}
	broken := Mod{Plugin: "test.c", ID: "c", Name: "c", Error: "the plugin's files changed"}
	h.Load([]Mod{b, broken})
	if p := h.Problems(); len(p) != 1 || !strings.Contains(p[0], "files changed") {
		t.Fatalf("problems = %v", p)
	}
}

func TestSandboxHasNoHostAccess(t *testing.T) {
	m := mod(t, "probe", "", `export function register(on) {
  on("command.run", ($, e) => ({
    require: typeof require, process: typeof process, fetch: typeof fetch,
    setTimeout: typeof setTimeout, XMLHttpRequest: typeof XMLHttpRequest,
  }));
}`)
	h := host(t, Options{}, m)
	res, _ := h.Dispatch(context.Background(), "command.run", map[string]any{"id": "x"}, nil)
	for k, v := range res.(map[string]any) {
		if v != "undefined" {
			t.Errorf("%s is %v inside a mod", k, v)
		}
	}
}

func TestBadModsReportInsteadOfRunning(t *testing.T) {
	h := NewHost(Options{})
	defer h.Close()
	h.Load([]Mod{
		{Plugin: "p", ID: "noreg", Script: "var x = 1;"},
		{Plugin: "p", ID: "throws", Script: "function register(on) { throw new Error('nope'); }"},
	})
	p := strings.Join(h.Problems(), "\n")
	if h.Loaded() != 0 || !strings.Contains(p, "no register") || !strings.Contains(p, "nope") {
		t.Fatalf("loaded %d, problems %q", h.Loaded(), p)
	}
}

func TestScaffoldedModRunsInTheConsole(t *testing.T) {
	files, err := pluginpkg.ScaffoldMod("acme.hello")
	if err != nil {
		t.Fatal(err)
	}
	var src string
	for _, f := range files {
		if f.Path == "mods/hello.js" {
			src = string(f.Data)
		}
	}
	h := host(t, Options{StateDir: t.TempDir()}, mod(t, "hello", "", src))
	eventually(t, "the scaffold's command", func() bool { return len(h.Commands()) == 1 })
	card := map[string]any{"session": map[string]any{"agent": "codex"}}
	if _, elems := h.Render("session.card", card); len(elems) != 1 || elems[0].Type != "Badge" || elems[0].Text != "codex" {
		t.Fatalf("card = %#v", elems)
	}
	if _, elems := h.Render("status", nil); len(elems) != 1 || elems[0].Text != "hello" || elems[0].Tone != "accent" {
		t.Fatalf("status = %#v", elems)
	}
	if _, err := h.RunCommand(context.Background(), h.Commands()[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, elems := h.Render("session.card", card); len(elems) != 0 {
		t.Fatalf("toggle did not hide the badge: %#v", elems)
	}
}
