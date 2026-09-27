package api_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// docs/ssh.md: a host in ~/.ssh/config becomes a machine in one request,
// reached by its alias so the whole config entry applies.
func TestSSHConfigHostsImportAsMachines(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config")
	writeFile(t, cfg, "Host build-box\n  HostName 10.0.0.5\n  User dev\n  Port 2222\n  ProxyJump bastion\n  ForwardAgent yes\n  GSSAPIAuthentication yes\nHost bastion\n  HostName b.example\nHost *.corp\n  User x\n")
	t.Setenv("LECTERN_SSH_CONFIG", cfg)
	h := newHarness(t)
	list := h.get("/api/ssh/hosts")
	hosts := list.list("hosts")
	if len(hosts) != 2 || hosts[0].str("alias") != "build-box" || hosts[0].str("hostname") != "10.0.0.5" ||
		hosts[0].num("port") != 2222 || hosts[0].str("proxy_jump") != "bastion" {
		t.Fatalf("hosts: %v", list)
	}
	res := h.post("/api/ssh/import", obj{"aliases": []string{"build-box", "nope"}, "transport": "openssh"}, 200)
	results := res.list("results")
	if results[0].sub("target").str("name") != "build-box" || results[1].str("error") == "" {
		t.Fatalf("import: %v", res)
	}
	target := results[0].sub("target")
	if target.str("host") != "10.0.0.5" || target.str("user") != "dev" || target.num("port") != 2222 {
		t.Fatalf("target: %v", target)
	}
	for _, want := range []string{`"alias":"build-box"`, `"proxy_jump":"bastion"`, `"forward_agent":true`, `GSSAPIAuthentication=yes`, `"transport":"openssh"`} {
		if !strings.Contains(target.str("ssh_json"), want) {
			t.Errorf("ssh_json %s lacks %s", target.str("ssh_json"), want)
		}
	}
	// Importing again reports the machine it already is.
	if again := h.get("/api/ssh/hosts").list("hosts"); again[0].num("target_id") != target.num("id") {
		t.Fatalf("existing target not matched: %v", again[0])
	}
}

func TestSSHOptionsAreValidatedAndConnectionIsReported(t *testing.T) {
	h := newHarness(t)
	id := h.firstTargetID()
	h.request2("PUT", fmt.Sprintf("/api/targets/%d/ssh", id), obj{"options": []string{"ProxyCommand=nc %h %p"}}, 422)
	got := h.request2("PUT", fmt.Sprintf("/api/targets/%d/ssh", id), obj{"proxy_jump": "jump@bastion:2200", "forward_agent": true, "editor_host": "devbox"}, 200)
	if !strings.Contains(got.str("ssh_json"), `"editor_host":"devbox"`) {
		t.Fatalf("saved: %v", got)
	}
	st := h.get(fmt.Sprintf("/api/targets/%d/connection?probe=1", id))
	if st.str("state") != "connected" {
		t.Fatalf("connection: %v", st)
	}
	if h.post(fmt.Sprintf("/api/targets/%d/reconnect", id), nil, 200).str("state") != "connected" {
		t.Fatal("reconnect did not come back connected")
	}
	ports := h.get(fmt.Sprintf("/api/targets/%d/ports", id)).list("ports")
	if len(ports) != 2 || ports[0].num("port") != 22 || ports[1].num("port") != 5173 || ports[1].str("process") != "node" {
		t.Fatalf("ports: %v", ports)
	}
	h.request2("GET", fmt.Sprintf("/api/targets/%d/download?path=relative/path", id), nil, 422)
}

// docs/sandboxes.md: a Docker sandbox target makes a container per attempt,
// gives it the dispatch's own environment, and removes it at the end.
func TestDockerSandboxLifecycleAndDispatchEnvironment(t *testing.T) {
	h := newHarness(t, withCreds(t))
	tgt := h.post("/api/targets", obj{"name": "docker-sb", "kind": "sandbox", "sandbox": true,
		"sandbox_config": obj{"provider": "docker", "image": "ghcr.io/me/dev:1", "env": obj{"TEAM": "core"}}}, 201)
	p := h.post("/api/projects", obj{"name": "dk", "target_id": tgt.id(), "repo_path": "https://github.com/user/demo.git"}, 201)
	task := h.task(p.id(), "dk run", "do it", obj{"permission_mode": "bypassPermissions"})
	h.request2("POST", fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{"env": obj{"BAD-NAME": "x"}}, 422)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{"env": obj{"FEATURE_FLAG": "on"}}, 200)
	h.waitStatus(task.id(), "review")
	att, err := h.App.DB.LatestAttempt(task.id())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("lec-sb-%d-", att.ID)
	if !strings.HasPrefix(att.SandboxVMID, name) {
		t.Fatalf("sandbox id %q", att.SandboxVMID)
	}
	for _, want := range []string{
		"docker run -d --name " + att.SandboxVMID,
		"-e FEATURE_FLAG=on", "-e TEAM=core", fmt.Sprintf("-e LECTERN_ATTEMPT_ID=%d", att.ID),
		"ghcr.io/me/dev:1 sleep infinity",
		"docker rm -f " + att.SandboxVMID,
	} {
		if !h.cmdLogHas(want) {
			t.Errorf("missing from the command log: %q", want)
		}
	}
	// The dispatch env also reaches the agent's own launch.
	if !strings.Contains(h.launchCmd(), "FEATURE_FLAG=on") {
		t.Errorf("agent env lacks the dispatch env: %s", h.launchCmd())
	}
	rows := h.getList("/api/sandboxes?all=1")
	if len(rows) != 1 || rows[0].str("status") != "destroyed" || rows[0].str("provider") != "docker" || rows[0].num("task_id") != float64(task.id()) {
		t.Fatalf("sandboxes: %v", rows)
	}
	if len(h.getList("/api/sandboxes")) != 0 {
		t.Fatal("a destroyed sandbox is not live")
	}
}

func TestManualSandboxSuspendResumeDestroyAndKeepOnFinish(t *testing.T) {
	h := newHarness(t, withCreds(t))
	tgt := h.post("/api/targets", obj{"name": "keep-sb", "kind": "sandbox", "sandbox": true,
		"sandbox_config": obj{"provider": "docker", "image": "img:1", "on_finish": "suspend"}}, 201)
	sb := h.post("/api/sandboxes", obj{"target_id": tgt.id()}, 201)
	if sb.str("status") != "running" || sb.sub("can")["suspend"] != true {
		t.Fatalf("created: %v", sb)
	}
	path := fmt.Sprintf("/api/sandboxes/%d/", sb.id())
	if s := h.post(path+"suspend", nil, 200); s.str("status") != "suspended" || s.sub("can")["resume"] != true {
		t.Fatalf("suspend: %v", s)
	}
	h.post(path+"resume", nil, 200)
	h.post(path+"destroy", nil, 200)
	h.request2("POST", path+"destroy", nil, 409)
	for _, want := range []string{"docker pause " + sb.str("ext_id"), "docker unpause " + sb.str("ext_id"), "docker rm -f " + sb.str("ext_id")} {
		if !h.cmdLogHas(want) {
			t.Errorf("missing %q", want)
		}
	}
	// on_finish: suspend keeps an attempt's sandbox, paused, for later.
	p := h.post("/api/projects", obj{"name": "keep", "target_id": tgt.id(), "repo_path": "https://github.com/user/demo.git"}, 201)
	task := h.task(p.id(), "keep run", "do it", obj{"permission_mode": "bypassPermissions"})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "review")
	h.waitUntil("the sandbox to be suspended", func() bool {
		for _, row := range h.getList("/api/sandboxes") {
			if row.num("task_id") == float64(task.id()) && row.str("status") == "suspended" {
				return true
			}
		}
		return false
	})
}

// A script provider's hooks run only once a person trusted that exact file.
func TestScriptSandboxHooksNeedTrust(t *testing.T) {
	h := newHarness(t, withCreds(t))
	tgt := h.post("/api/targets", obj{"name": "script-sb", "kind": "sandbox", "sandbox": true,
		"sandbox_config": obj{"provider": "script", "config_path": "/srv/demo/lectern.sandbox.yaml"}}, 201)
	hooks := "create: echo sb-1\nexec: bash -c \"$LECTERN_COMMAND\"\ndestroy: \"true\"\n"
	h.App.Reg.For(mustTarget(t, h, tgt.id()))
	h.mock().WriteFile(context.Background(), "/srv/demo/lectern.sandbox.yaml", []byte(hooks))
	view := h.get(fmt.Sprintf("/api/targets/%d/sandbox/hooks", tgt.id()))
	if view.num("trusted") != 0 && view["trusted"] != false || view.str("content") != hooks {
		t.Fatalf("hooks: %v", view)
	}
	h.request2("POST", "/api/sandboxes", obj{"target_id": tgt.id()}, 409)
	h.request2("POST", fmt.Sprintf("/api/targets/%d/sandbox/trust", tgt.id()), obj{"path": view.str("path"), "sha256": "0000"}, 409)
	h.post(fmt.Sprintf("/api/targets/%d/sandbox/trust", tgt.id()), obj{"path": view.str("path"), "sha256": view.str("sha256")}, 200)
	if sb := h.post("/api/sandboxes", obj{"target_id": tgt.id()}, 201); sb.str("provider") != "script" {
		t.Fatalf("created %v", sb)
	}
	// Editing the file withdraws the trust.
	h.mock().WriteFile(context.Background(), "/srv/demo/lectern.sandbox.yaml", []byte(hooks+"# changed\n"))
	h.request2("POST", "/api/sandboxes", obj{"target_id": tgt.id()}, 409)
}

func TestProviderUsageReport(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "codex live", "agent": "codex"})
	reset := float64(time.Now().Add(time.Hour).Unix())
	h.App.DB.Update("sessions", sess.id(), map[string]any{"rate_5h_pct": 91, "rate_5h_reset": reset,
		"rate_7d_pct": 30, "rate_7d_reset": reset, "usage_at": float64(time.Now().Unix())})
	today := time.Now().UTC().Format("2006-01-02")
	h.App.DB.UpsertUsageDelta(today, sess.id(), "codex", "gpt-5", 1.5, 1000, 200)
	h.request2("PUT", "/api/model-prices", obj{"prices": obj{"gpt-5": obj{"input_per_1m": 1, "output_per_1m": 10}}}, 200)
	rep := h.get("/api/usage/providers?days=7")
	if rep.num("warn_percent") != 80 {
		t.Fatalf("warn: %v", rep)
	}
	var codex obj
	for _, p := range rep.list("providers") {
		if p.str("agent") == "codex" {
			codex = p
		}
	}
	if codex == nil || codex["warn"] != true || codex.num("cost_usd") != 1.5 || len(codex.list("daily")) != 7 || codex.num("peak_pct") != 91 {
		t.Fatalf("codex: %v", codex)
	}
	if acc := codex.list("accounts"); len(acc) != 1 || acc[0].str("label") != "Default" || acc[0]["warn"] != true {
		t.Fatalf("accounts: %v", acc)
	}
	table := rep.list("cost_table")
	if len(table) != 1 || table[0]["priced"] != true || table[0].num("list_usd") < 0.0029 || table[0].num("list_usd") > 0.0031 {
		t.Fatalf("cost table: %v", table)
	}
}

func mustTarget(t *testing.T, h *harness, id int64) *store.Target {
	t.Helper()
	tg, err := h.App.DB.Target(id)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}
