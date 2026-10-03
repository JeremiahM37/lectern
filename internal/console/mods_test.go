package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/console/mods"
	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// modTestServer records every request that changes something.
type modTestServer struct {
	mu    sync.Mutex
	posts []string
}

func (s *modTestServer) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			b, _ := json.Marshal(body)
			s.mu.Lock()
			s.posts = append(s.posts, r.Method+" "+r.URL.Path+" "+string(b))
			s.mu.Unlock()
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *modTestServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.posts...)
}

func modDashboard(t *testing.T, base string, sources ...string) *dashboard {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // mod state must not land in the real config dir
	m := newDashboard(New(base, ""), nil)
	t.Cleanup(m.mods.host.Close)
	var list []mods.Mod
	for i, src := range sources {
		script, err := pluginpkg.ModScript(src)
		if err != nil {
			t.Fatal(err)
		}
		id := string(rune('a' + i))
		list = append(list, mods.Mod{Plugin: "test." + id, ID: id, Name: "Mod " + id, Hash: id, Script: script})
	}
	m.receiveMods(modsLoadedMsg{list: list})
	if p := m.mods.host.Problems(); len(p) > 0 {
		t.Fatal(p)
	}
	return m
}

func sessionRows(m *dashboard, rows ...row) {
	m.generation++
	m.Update(rowsMsg{section: "sessions", generation: m.generation, rows: rows})
}

const cardMod = `export function register(on) {
  on("ui.render", "session.card", async ($, e, next) => {
    const out = await next(e);
    if (e.props.session.agent === "hidden-agent") return { ...out, hidden: true };
    const { Badge, Button } = $.ui.resolve(e);
    if (e.props.session.agent === "codex") out.append.push(Badge({ text: "codex-mod", tone: "accent" }), Button({ label: "Ping", hotkey: "P" }));
    return out;
  });
  on("ui.render", "status", async ($, e, next) => {
    const out = await next(e);
    out.append.push($.ui.resolve(e).Text({ text: "3 agents", tone: "ok" }));
    return out;
  });
}`

func TestModBadgeAppearsInSessionRowAndHiddenRowsAreSkipped(t *testing.T) {
	m := modDashboard(t, "http://unused", cardMod)
	m.width, m.height = 120, 30
	sessionRows(m,
		row{"id": float64(1), "name": "Alpha", "agent": "codex", "status": "running"},
		row{"id": float64(2), "name": "Beta", "agent": "claude", "status": "running"},
		row{"id": float64(3), "name": "Gamma", "agent": "hidden-agent", "status": "running"},
	)
	view := ansi.Strip(m.View())
	var alpha string
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Alpha") && alpha == "" {
			alpha = line
		}
	}
	if !strings.Contains(alpha, "codex-mod") || !strings.Contains(alpha, "[P Ping]") {
		t.Fatalf("badge and button not drawn on the session row:\n%s", view)
	}
	if strings.Contains(view, "Gamma") || len(m.visible) != 2 {
		t.Fatalf("hidden session is still listed:\n%s", view)
	}
	if lines := strings.Split(view, "\n"); !strings.Contains(lines[len(lines)-1], "3 agents") {
		t.Fatalf("status segment missing from footer: %q", lines[len(lines)-1])
	}
	m.selected = 0
	found := false
	for _, a := range m.paletteActions() {
		found = found || (a.Label == "Ping" && strings.HasPrefix(a.Operation, "mod-button:1:"))
	}
	if !found {
		t.Fatal("the selected card's button is not in the palette")
	}
}

const guardMod = `export function register(on) {
  on("prompt.submit", async ($, e, next) => {
    if (/rm\s+-rf\s+\//.test(e.text)) return { deny: "That would delete the whole disk." };
    return next({ ...e, text: e.text.trim() });
  });
  on("approval.decide", async ($, e, next) => e.decision === "allow" && e.approval.tool_name === "Bash" ? { deny: "no Bash today" } : next(e));
}`

func runCmd(t *testing.T, m *dashboard, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command to run")
	}
	m.Update(cmd())
}

func TestModDenyBlocksSendAndRewriteChangesIt(t *testing.T) {
	var server modTestServer
	srv := server.start(t)
	m := modDashboard(t, srv.URL, guardMod)
	sessionRows(m, row{"id": float64(4), "name": "Alpha", "agent": "claude", "status": "running"})
	m.sendForm()
	if m.form == nil {
		t.Fatal("send form did not open")
	}
	runCmd(t, m, m.form.submit(map[string]any{"text": "rm -rf /"}))
	if got := server.requests(); len(got) != 0 {
		t.Fatalf("a denied message was sent: %v", got)
	}
	if !strings.Contains(m.notice, "Blocked by a mod: That would delete the whole disk.") || m.form == nil {
		t.Fatalf("deny not shown, or the draft was dropped: notice %q, form %v", m.notice, m.form != nil)
	}
	runCmd(t, m, m.form.submit(map[string]any{"text": "  run the tests  "}))
	got := server.requests()
	if len(got) != 1 || got[0] != `POST /api/sessions/4/send {"text":"run the tests"}` {
		t.Fatalf("sent %v", got)
	}
	if m.form != nil {
		t.Fatal("a sent message should close the form")
	}
}

func TestModDenyStopsAnApprovalDecision(t *testing.T) {
	var server modTestServer
	srv := server.start(t)
	m := modDashboard(t, srv.URL, guardMod)
	bash := row{"id": float64(9), "tool_name": "Bash", "session_id": float64(4), "input": map[string]any{"command": "ls"}}
	runCmd(t, m, m.decide(bash, "approved", false, ""))
	if len(server.requests()) != 0 || !strings.Contains(m.notice, "no Bash today") {
		t.Fatalf("allow went through: %v, notice %q", server.requests(), m.notice)
	}
	runCmd(t, m, m.decide(bash, "denied", false, ""))
	if got := server.requests(); len(got) != 1 || !strings.HasPrefix(got[0], "POST /api/approvals/9/decision") {
		t.Fatalf("deny decision not sent: %v", got)
	}
}

func TestModCommandsRunFromThePaletteAndOpenPanes(t *testing.T) {
	m := modDashboard(t, "http://unused", `export function register(on) {
  on("app.start", async ($, e, next) => {
    $.command.register({ id: "hello", title: "Say hello from a mod", run: ($) => { $.ui.open({ id: "p", title: "Hello pane" }); } });
    return next(e);
  });
  on("ui.render", "pane", async ($, e, next) => {
    const out = await next(e);
    const { Text, Button } = $.ui.resolve(e);
    out.append.push(Text("pane body " + ($.state.get("n") || 0)), Button({ label: "Count", hotkey: "c", onPress: () => $.state.set("n", ($.state.get("n") || 0) + 1) }));
    return out;
  });
}`)
	deadline := time.Now().Add(3 * time.Second)
	for len(m.mods.host.Commands()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	m.openPalette("hello mod")
	list := m.paletteActions()
	if len(list) == 0 || list[0].Label != "Say hello from a mod" {
		t.Fatalf("mod command not found in the palette: %#v", list)
	}
	cmd := m.updatePalette(tea.KeyMsg{Type: tea.KeyEnter})
	runCmd(t, m, cmd)
	m.Update(m.waitModNotice()())
	if !m.modPaneOpen() || m.mods.pane.Title != "Hello pane" {
		t.Fatal("pane did not open")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Hello pane") || !strings.Contains(view, "pane body 0") || !strings.Contains(view, "c Count") {
		t.Fatalf("pane not drawn:\n%s", view)
	}
	m.Update(key("c"))
	// The press runs in the mod; its $.state.set asks for a render.
	m.Update(m.waitModNotice()())
	if view := ansi.Strip(m.View()); !strings.Contains(view, "pane body 1") {
		t.Fatalf("button hotkey did not run onPress:\n%s", view)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.modPaneOpen() {
		t.Fatal("Esc did not close the pane")
	}
}
