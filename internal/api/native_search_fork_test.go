package api_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/sessions"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/store"
)

func TestNativeSearchForkUsesChosenSettingsAndVerifiedUntrackedWorkspace(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	cwd, home, cache, proof := t.TempDir(), t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "proof.json")
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", cwd}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(cwd, "base.txt"), []byte("base\n"), 0600)
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "fork search", Kind: "local", Workroot: t.TempDir()})
	stub := filepath.Join(t.TempDir(), "agent")
	os.WriteFile(stub, []byte("#!/usr/bin/env python3\nimport os,sys,json,time\nfrom pathlib import Path\nPath(os.environ['FORK_PROOF']).write_text(json.dumps({'argv':sys.argv[1:],'cwd':os.getcwd(),'home':os.environ['CODEX_HOME']}))\nwhile True:time.sleep(1)\n"), 0700)
	env := map[string]string{"CODEX_HOME": home, "LECTERN_NATIVE_SEARCH_CACHE": cache, "FORK_PROOF": proof, "PRIVATE_TEST": "never-public"}
	saved := sessions.LaunchConfiguration{Version: 1, Spec: sessions.Spec{Name: "codex", Command: shellq.Quote(stub), Env: env, ForkArgs: []string{"saved-fork", "{id}"}}}
	raw, _ := json.Marshal(saved)
	source, _ := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Agent: "codex", Name: "Saved configuration", Workdir: t.TempDir(), TmuxSession: "not-running"})
	h.App.DB.Exec("UPDATE sessions SET ended_at=1,launch_config_json=? WHERE id=?", string(raw), source.ID)
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": shellq.Quote(stub), "env": env, "fork_args": []string{"current-fork", "{id}"}}}, 200, nil)
	cid := "11111111-1111-4111-8111-111111111111"
	file := writeSearchFixture(t, home, cwd, cid, "fork proof needle")
	original, _ := os.ReadFile(file)
	var start obj
	h.decode("POST", "/api/conversation-search", obj{"query": "needle", "agent": "codex", "target_id": target.ID}, 202, &start)
	result := waitNativeSearch(t, h, start["id"].(string))
	if len(result["scopes"].([]any)) != 1 {
		t.Fatal("same environment indexed more than once", result)
	}
	hit := result["results"].([]any)[0].(map[string]any)
	base := "/api/conversation-search/" + start["id"].(string) + "/results/" + hit["id"].(string)
	var page obj
	h.decode("GET", base, nil, 200, &page)
	choices := page["fork_options"].([]any)
	if len(choices) != 2 {
		t.Fatal(page)
	}
	if strings.Contains(fmt.Sprint(page), "never-public") || strings.Contains(fmt.Sprint(page), "configuration:") {
		t.Fatal("private launch configuration exposed")
	}
	selected, currentChoice := "", ""
	for _, choice := range choices {
		c := choice.(map[string]any)
		if c["label"] == "Current agent settings" {
			currentChoice = c["id"].(string)
		}
		if strings.Contains(c["label"].(string), "Saved configuration") {
			selected = c["id"].(string)
		}
	}
	h.decode("POST", base+"/fork", obj{}, 422, nil)
	h.decode("POST", base+"/fork", obj{"configuration_id": "foreign"}, 422, nil)
	// A later settings edit must not change either the chosen command or profile.
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "false", "env": obj{"CODEX_HOME": t.TempDir()}}}, 200, nil)
	for _, isolated := range []bool{false, true} {
		os.Remove(proof)
		body := obj{"configuration_id": selected, "name": "Search fork"}
		expectedCommand := "saved-fork"
		if isolated {
			body["configuration_id"] = currentChoice
			expectedCommand = "current-fork"
			body["worktree"] = obj{"branch": "search-fork-proof"}
		}
		var launched obj
		h.decode("POST", base+"/fork", body, 201, &launched)
		deadline := time.Now().Add(5 * time.Second)
		var data []byte
		for time.Now().Before(deadline) {
			data, _ = os.ReadFile(proof)
			if len(data) > 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		var observed obj
		if json.Unmarshal(data, &observed) != nil {
			t.Fatalf("fork did not reach agent: %s", data)
		}
		if observed["home"] != home || !strings.Contains(fmt.Sprint(observed["argv"]), expectedCommand) || !strings.Contains(fmt.Sprint(observed["argv"]), cid) {
			t.Fatal(observed)
		}
		if isolated == (observed["cwd"] == cwd) {
			t.Fatal("wrong fork workspace", observed)
		}
		if observed["cwd"] != launched["workdir"] {
			t.Fatal("session record disagrees with launched agent")
		}
	}
	after, _ := os.ReadFile(file)
	if string(after) != string(original) {
		t.Fatal("fork changed source history")
	}
	records, _ := h.App.DB.Sessions(true)
	if len(records) != 3 {
		t.Fatal("unexpected placeholder session records", len(records))
	}
	os.WriteFile(file, []byte(strings.ReplaceAll(string(original), "fork proof needle", "different message")), 0600)
	h.decode("POST", base+"/fork", obj{"configuration_id": selected}, 409, nil)
	records, _ = h.App.DB.Sessions(true)
	if len(records) != 3 {
		t.Fatal("stale source created a session")
	}
}

func TestNativeSearchForkRejectsChangedProfileAlias(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "profile alias", Kind: "local"})
	home, other := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "profile")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	writeSearchFixture(t, home, t.TempDir(), "11111111-1111-4111-8111-111111111111", "alias needle")
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "false", "env": obj{"CODEX_HOME": alias, "LECTERN_NATIVE_SEARCH_CACHE": t.TempDir()}, "fork_args": []string{"fork", "{id}"}}}, 200, nil)
	var start obj
	h.decode("POST", "/api/conversation-search", obj{"query": "needle", "target_id": target.ID, "agent": "codex"}, 202, &start)
	result := waitNativeSearch(t, h, start["id"].(string))
	hit := result["results"].([]any)[0].(map[string]any)
	base := "/api/conversation-search/" + start["id"].(string) + "/results/" + hit["id"].(string)
	var page obj
	h.decode("GET", base, nil, 200, &page)
	choice := page["fork_options"].([]any)[0].(map[string]any)
	os.Remove(alias)
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	h.decode("POST", base+"/fork", obj{"configuration_id": choice["id"]}, 409, nil)
	rows, _ := h.App.DB.Sessions(true)
	if len(rows) != 0 {
		t.Fatal("profile change created session records")
	}
}
