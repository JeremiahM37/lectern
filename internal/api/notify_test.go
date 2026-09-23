package api_test

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
)

// sinkSpy captures what would have been delivered, with no network at all.
type sinkSpy struct {
	mu   sync.Mutex
	sent []sinks.Payload
}

func (s *sinkSpy) hook(p []sinks.Payload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, p...)
}

func (s *sinkSpy) all() []sinks.Payload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sinks.Payload(nil), s.sent...)
}

func TestSettingsRoundtrip(t *testing.T) {
	h := newHarness(t)
	got := h.get("/api/settings")
	for _, k := range sinks.Keys {
		if got.str(k) != "" {
			t.Errorf("%s should start empty: %q", k, got.str(k))
		}
	}
	var saved obj
	h.decode("PUT", "/api/settings", obj{
		"discord_webhook": "https://discord/hook",
		"ntfy_server":     "https://ntfy.sh", "ntfy_topic": "lec"}, 200, &saved)
	if h.get("/api/settings").str("ntfy_topic") != "lec" {
		t.Error("settings did not persist")
	}
	// an unknown key must fail loudly, not silently disable notifications
	if code := h.status("PUT", "/api/settings", obj{"evil": "x"}); code != 400 {
		t.Errorf("unknown setting: %d", code)
	}
	if code := h.status("PUT", "/api/settings", obj{"ntfy_topic": 5}); code != 400 {
		t.Errorf("non-string setting: %d", code)
	}
}

func TestTestNotificationEndpoint(t *testing.T) {
	h := newHarness(t)
	spy := &sinkSpy{}
	h.App.Notifier.Hook = spy.hook
	h.decode("PUT", "/api/settings", obj{"discord_webhook": "https://discord/hook"}, 200, nil)
	got := h.post("/api/settings/test-notification", nil, 200)
	if got["sent"] != true {
		t.Fatalf("response: %v", got)
	}
	h.waitUntil("a payload to be built", func() bool { return len(spy.all()) > 0 })
	if spy.all()[0].Kind != "discord" {
		t.Errorf("sink: %+v", spy.all()[0])
	}
}

func TestNotifyFiresOnReview(t *testing.T) {
	h := newHarness(t)
	spy := &sinkSpy{}
	h.App.Notifier.Hook = spy.hook
	h.decode("PUT", "/api/settings",
		obj{"ntfy_server": "https://ntfy.sh", "ntfy_topic": "lec"}, 200, nil)
	task := h.task(h.seededProjectID(), "notify me", "x", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitUntil("an ntfy payload for the review", func() bool {
		for _, p := range spy.all() {
			title, _ := p.Body["title"].(string)
			if p.Kind == "ntfy" && strings.Contains(title, "Ready for review") {
				return true
			}
		}
		return false
	})
}

func TestTerminalAttachEndpoint(t *testing.T) {
	h := newHarness(t)
	// stub out ttyd: the endpoint's job is resolving the right session, not
	// proving a terminal emulator works
	h.App.Terminals.LookPath = func(string) (string, error) { return "/usr/bin/ttyd", nil }
	var gotArgv []string
	h.App.Terminals.Spawn = func(port int, basePath string, argv []string) (*exec.Cmd, error) {
		gotArgv = argv
		return exec.Command("true"), nil
	}

	task := h.task(h.seededProjectID(), "term", "x [mock:slow]", nil)
	// not running yet
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/terminal", task.id()), nil); code != 409 {
		t.Errorf("attaching before the agent runs: %d", code)
	}
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "running")
	got := h.post(fmt.Sprintf("/api/tasks/%d/terminal", task.id()), nil, 200)
	port := int(got.num("port"))
	if port < terminal.PortLo || port > terminal.PortHi {
		t.Errorf("port outside the terminal range: %d", port)
	}
	if len(gotArgv) < 4 || !strings.HasPrefix(gotArgv[3], "lec-") {
		t.Errorf("ttyd must wrap this attempt's tmux session: %v", gotArgv)
	}
	if code := h.status("POST", "/api/tasks/9999/terminal", nil); code != 404 {
		t.Errorf("unknown task: %d", code)
	}
}

func TestAttachArgvPerTargetKind(t *testing.T) {
	att := terminal.Attachment{Key: "attempt:7", TmuxSession: "lec-7", SandboxVMID: "9001"}
	sandbox, _ := terminal.AttachArgv(att, &store.Target{Kind: "sandbox"})
	if strings.Join(sandbox, " ") != "sudo pct exec 9001 -- tmux attach -t lec-7 ; set-option -w -t =lec-7: window-size latest" {
		t.Errorf("sandbox: %v", sandbox)
	}
	pct, _ := terminal.AttachArgv(terminal.Attachment{TmuxSession: "lec-7"},
		&store.Target{Kind: "pct", Host: "105"})
	if strings.Join(pct, " ") != "sudo pct exec 105 -- tmux attach -t lec-7 ; set-option -w -t =lec-7: window-size latest" {
		t.Errorf("pct: %v", pct)
	}
	ssh, _ := terminal.AttachArgv(terminal.Attachment{TmuxSession: "lec-7"},
		&store.Target{Kind: "ssh", Host: "192.0.2.9", User: "root", KeyPath: "/k"})
	joined := strings.Join(ssh, " ")
	if !strings.Contains(joined, "-i /k") || !strings.Contains(joined, "root@192.0.2.9") {
		t.Errorf("ssh: %v", ssh)
	}
	local, _ := terminal.AttachArgv(terminal.Attachment{TmuxSession: "lec-7"}, &store.Target{Kind: "local"})
	if strings.Join(local, " ") != "tmux attach -t lec-7 ; set-option -w -t =lec-7: window-size latest" {
		t.Errorf("local: %v", local)
	}
}

func TestTemplatesRoundtrip(t *testing.T) {
	h := newHarness(t)
	if got := h.getList("/api/templates"); len(got) != 0 {
		t.Fatalf("templates start empty: %v", got)
	}
	tpls := []obj{
		{"name": "bugfix", "title": "Fix: ", "prompt": "Reproduce, fix, add a test.",
			"permission_mode": "acceptEdits"},
		{"name": "local-quick", "model": "qwen3.5:4b", "prompt": "small change"},
	}
	h.decode("PUT", "/api/templates", tpls, 200, nil)
	if got := h.getList("/api/templates"); len(got) != 2 || got[0].str("name") != "bugfix" {
		t.Fatalf("saved templates: %v", got)
	}
	if code := h.status("PUT", "/api/templates", []obj{{"prompt": "no name"}}); code != 400 {
		t.Errorf("a nameless template must be rejected, got %d", code)
	}
}

func TestStatsAggregatesCosts(t *testing.T) {
	h := newHarness(t)
	task := h.run(h.seededProjectID(), "cost me", "x", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/complete", task.id()), nil, 200)
	s := h.get("/api/stats")
	if s.num("total_cost_usd") < 0.0123 || s.num("last_7d_usd") < 0.0123 {
		t.Fatalf("costs: %v", s)
	}
	if s.num("tasks_done") < 1 {
		t.Errorf("tasks_done: %v", s["tasks_done"])
	}
	byProject := s.list("by_project")
	if len(byProject) == 0 || byProject[0].num("cost_usd") <= 0 {
		t.Errorf("by_project: %v", byProject)
	}
}

func TestPushVAPIDRequiresConfiguration(t *testing.T) {
	h := newHarness(t)
	if code := h.status("GET", "/api/push/vapid", nil); code != 404 {
		t.Fatalf("push is unconfigured here, expected 404, got %d", code)
	}
	// a subscription is still accepted, so a device can register before keys exist
	if code := h.status("POST", "/api/push/subscribe",
		obj{"endpoint": "https://push.example/x", "keys": obj{"p256dh": "k", "auth": "a"}}); code != 201 {
		t.Errorf("subscribe: %d", code)
	}
}
