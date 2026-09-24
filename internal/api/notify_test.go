package api_test

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
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

// The owner must never have to configure VAPID keys by hand: a fresh harness
// sets neither LECTERN_VAPID_PRIVATE nor LECTERN_VAPID_PUBLIC, so this proves
// the whole install auto-provisions push rather than leaving it dark.
func TestPushVAPIDIsAutoProvisionedWithNoEnvKeys(t *testing.T) {
	h := newHarness(t)
	got := h.get("/api/push/vapid")
	if got.str("key") == "" {
		t.Fatal("push should be auto-provisioned; got an empty key")
	}
	if code := h.status("POST", "/api/push/subscribe",
		obj{"endpoint": "https://push.example/x", "keys": obj{"p256dh": "k", "auth": "a"}}); code != 201 {
		t.Errorf("subscribe: %d", code)
	}
}

// LECTERN_VAPID_PRIVATE/PUBLIC, when both are set, are an explicit operator
// choice and must win over the auto-generated pair.
func TestPushVAPIDEnvKeysWinOverAutoProvisioning(t *testing.T) {
	h := newHarness(t, func(cfg *config.Config) {
		cfg.VAPIDPrivateKey = "operator-private-key"
		cfg.VAPIDPublicKey = "operator-public-key"
	})
	got := h.get("/api/push/vapid")
	if got.str("key") != "operator-public-key" {
		t.Fatalf("expected the operator's own key, got %q", got.str("key"))
	}
}

// Neither the auto-generated nor an env-configured private key may ever be
// exposed by an API — not /api/push/vapid (public key only, asserted above),
// and not /api/settings, which is a generic key/value surface and does not
// even know these settings keys exist.
func TestPushPrivateKeyIsNeverExposedBySettings(t *testing.T) {
	h := newHarness(t)
	got := h.get("/api/settings")
	if _, ok := got["vapid_private"]; ok {
		t.Fatalf("private key leaked through /api/settings: %v", got)
	}
	if _, ok := got["vapid_public"]; ok {
		t.Fatalf("vapid_public leaked through /api/settings: %v", got)
	}
	// and PUT must reject it exactly like any other unknown key, so nothing can
	// overwrite the generated identity through the settings surface either
	if code := h.status("PUT", "/api/settings", obj{"vapid_private": "x"}); code != 400 {
		t.Errorf("PUT /api/settings accepted vapid_private: %d", code)
	}
	if code := h.status("PUT", "/api/settings", obj{"vapid_public": "x"}); code != 400 {
		t.Errorf("PUT /api/settings accepted vapid_public: %d", code)
	}
}

func TestPushSubscriptionListAndUnsubscribe(t *testing.T) {
	h := newHarness(t)
	endpoint := "https://push.example/device-1"
	h.decode("POST", "/api/push/subscribe",
		obj{"endpoint": endpoint, "keys": obj{"p256dh": "k", "auth": "a"}}, 201, nil)

	subs := h.getList("/api/push/subscriptions")
	if len(subs) != 1 || subs[0].str("endpoint") != endpoint {
		t.Fatalf("subscriptions: %v", subs)
	}
	if _, ok := subs[0]["p256dh"]; ok {
		t.Errorf("subscription keys must not be listed: %v", subs[0])
	}

	if code := h.status("DELETE", "/api/push/subscribe", obj{"endpoint": endpoint}); code != 200 {
		t.Fatalf("unsubscribe: %d", code)
	}
	if subs := h.getList("/api/push/subscriptions"); len(subs) != 0 {
		t.Fatalf("subscription survived unsubscribe: %v", subs)
	}
	// unsubscribing something already gone is not an error
	if code := h.status("DELETE", "/api/push/subscribe", obj{"endpoint": endpoint}); code != 200 {
		t.Errorf("unsubscribe of an already-gone endpoint: %d", code)
	}
	if code := h.status("DELETE", "/api/push/subscribe", obj{}); code != 422 {
		t.Errorf("unsubscribe with no endpoint: %d", code)
	}
}

// A push send that comes back 404/410 means the browser dropped the
// subscription; the notifier must prune it rather than retry forever.
func TestExpiredPushSubscriptionIsPrunedOnDelivery(t *testing.T) {
	h := newHarness(t)
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(410)
	}))
	defer gone.Close()
	h.decode("POST", "/api/push/subscribe",
		obj{"endpoint": gone.URL, "keys": obj{"p256dh": browserP256dh(), "auth": browserAuth()}}, 201, nil)

	h.post("/api/settings/test-notification", nil, 200)
	h.waitUntil("the expired subscription to be pruned", func() bool {
		return len(h.getList("/api/push/subscriptions")) == 0
	})
}

func browserP256dh() string {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
}

func browserAuth() string {
	auth := make([]byte, 16)
	rand.Read(auth)
	return base64.RawURLEncoding.EncodeToString(auth)
}
