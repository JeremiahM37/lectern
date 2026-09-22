package api_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/sessions"
	"github.com/JeremiahM37/lectern/internal/store"
)

func waitNativeSearch(t *testing.T, h *harness, id string) obj {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var result obj
		h.decode("GET", "/api/conversation-search/"+id, nil, 200, &result)
		if result["done"] == true {
			return result
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("search did not finish")
	return nil
}
func writeSearchFixture(t *testing.T, home, cwd, id, text string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	rows := []obj{{"type": "session_meta", "payload": obj{"id": id, "cwd": cwd}},
		{"type": "response_item", "payload": obj{"type": "message", "role": "assistant", "channel": "analysis", "content": []obj{{"type": "output_text", "text": "PRIVATE_SENTINEL"}}}},
		{"type": "response_item", "payload": obj{"type": "message", "role": "assistant", "channel": "final", "content": []obj{{"type": "output_text", "text": text}}}}}
	var b strings.Builder
	for _, row := range rows {
		data, _ := json.Marshal(row)
		b.Write(data)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestNativeSearchAcrossSavedProfilesAndUntrackedWorkspaces(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "search local", Kind: "local"})
	home, old, cache := t.TempDir(), t.TempDir(), t.TempDir()
	cwd := t.TempDir()
	env := obj{"CODEX_HOME": home, "LECTERN_NATIVE_SEARCH_CACHE": cache, "SEARCH_TEST_SECRET": "never-public"}
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "codex", "env": env}}, 200, nil)
	first := "11111111-1111-4111-8111-111111111111"
	second := "22222222-2222-4222-8222-222222222222"
	file := writeSearchFixture(t, home, cwd, first, "--needle résumé current profile")
	writeSearchFixture(t, old, t.TempDir(), second, "--needle résumé historical profile")
	original, _ := os.ReadFile(file)
	snapshot, _ := json.Marshal(sessions.LaunchConfiguration{Version: 1, Spec: sessions.Spec{Name: "codex", Command: "codex", Env: map[string]string{"CODEX_HOME": old, "LECTERN_NATIVE_SEARCH_CACHE": cache, "SEARCH_TEST_SECRET": "never-public"}}})
	ended := float64(1)
	for i := 0; i < 2; i++ {
		row, err := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: fmt.Sprintf("historical%d", i), Agent: "codex", Workdir: t.TempDir(), TmuxSession: fmt.Sprintf("not-running%d", i), EndedAt: &ended, LaunchConfigJSON: string(snapshot)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.App.DB.Exec("UPDATE sessions SET launch_config_json=?,ended_at=?,archived_at=? WHERE id=?", string(snapshot), ended, ended, row.ID); err != nil {
			t.Fatal(err)
		}
	}
	var start obj
	h.decode("POST", "/api/conversation-search", obj{"query": "--needle résumé", "target_id": target.ID, "agent": "codex"}, 202, &start)
	result := waitNativeSearch(t, h, start["id"].(string))
	if result["complete"] != true {
		t.Fatal(result)
	}
	if len(result["scopes"].([]any)) != 2 {
		t.Fatalf("profiles not deduplicated: %v", result)
	}
	hits := result["results"].([]any)
	if len(hits) != 2 {
		t.Fatal(result)
	}
	encoded := fmt.Sprint(result)
	for _, secret := range []string{"never-public", home, old, cache, "PRIVATE_SENTINEL", "fingerprint", "profile_key"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("private data leaked: %s", secret)
		}
	}
	var selected obj
	for _, hit := range hits {
		m := hit.(map[string]any)
		if m["conversation_id"] == first {
			selected = m
		}
	}
	if selected == nil {
		t.Fatal(result)
	}
	base := "/api/conversation-search/" + start["id"].(string) + "/results/" + selected["id"].(string)
	var page obj
	h.decode("GET", base, nil, 200, &page)
	h.decode("GET", base+"?latest=1", nil, 200, nil)
	h.decode("GET", base+"?latest=0", nil, 422, nil)
	h.decode("GET", base+"?before=1&after=2", nil, 422, nil)
	h.decode("GET", base+"?before=not-a-number", nil, 422, nil)
	h.decode("GET", base+"?before=1", nil, 409, nil)
	if !strings.Contains(fmt.Sprint(page), "--needle résumé current profile") || strings.Contains(fmt.Sprint(page), "PRIVATE_SENTINEL") {
		t.Fatal(page)
	}
	// Current settings may change while the result remains bound to its captured profile.
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "codex", "env": obj{"CODEX_HOME": t.TempDir(), "LECTERN_NATIVE_SEARCH_CACHE": cache}}}, 200, nil)
	h.decode("GET", base, nil, 200, &page)
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "codex", "env": env}}, 200, nil)
	after, _ := os.ReadFile(file)
	if string(after) != string(original) {
		t.Fatal("reader mutated source")
	}
	if err := os.WriteFile(file, []byte(strings.ReplaceAll(string(original), "current profile", "changed profile")), 0600); err != nil {
		t.Fatal(err)
	}
	h.decode("GET", base, nil, 409, nil)
	// A new query refreshes the cache and exposes current text, including explicit rebuild.
	h.decode("POST", "/api/conversation-search", obj{"query": "changed profile", "target_id": target.ID, "agent": "codex", "reset": true}, 202, &start)
	result = waitNativeSearch(t, h, start["id"].(string))
	if len(result["results"].([]any)) != 1 {
		t.Fatal(result)
	}
}
func TestNativeSearchDefaultsWithoutTrackedSessionsAndPartialTargetFailure(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "search local", Kind: "local"})
	_, _ = h.App.DB.InsertTarget(&store.Target{Name: "unsupported", Kind: "sandbox"})
	home := t.TempDir()
	writeSearchFixture(t, home, t.TempDir(), "11111111-1111-4111-8111-111111111111", "available needle")
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "codex", "env": obj{"CODEX_HOME": home, "LECTERN_NATIVE_SEARCH_CACHE": t.TempDir()}}}, 200, nil)
	var start obj
	h.decode("POST", "/api/conversation-search", obj{"query": "needle", "agent": "codex"}, 202, &start)
	result := waitNativeSearch(t, h, start["id"].(string))
	if result["complete"] != false || len(result["results"].([]any)) != 1 {
		t.Fatal(result)
	}
	if !strings.Contains(fmt.Sprint(result), "unavailable for this target kind") {
		t.Fatal(result)
	}
	h.decode("POST", "/api/conversation-search", obj{"query": "needle", "agent": "codex", "target_id": target.ID}, 202, &start)
	if result = waitNativeSearch(t, h, start["id"].(string)); result["complete"] != true {
		t.Fatal(result)
	}
	for _, body := range []obj{{"query": " "}, {"query": strings.Repeat("a", 501)}, {"query": "x", "agent": "invalid"}, {"query": "x", "target_id": 999999}} {
		h.decode("POST", "/api/conversation-search", body, 422, nil)
	}
	h.decode("GET", "/api/conversation-search/missing", nil, 404, nil)
	h.decode("GET", "/api/conversation-search/"+start["id"].(string)+"/results/missing", nil, 404, nil)
}
func TestNativeSearchProjectProfileAliasesAndChangedTarget(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "aliases", Kind: "local"})
	home, cache := t.TempDir(), t.TempDir()
	writeSearchFixture(t, home, t.TempDir(), "11111111-1111-4111-8111-111111111111", "alias needle")
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "codex", "env": obj{"CODEX_HOME": home, "LECTERN_NATIVE_SEARCH_CACHE": cache}}}, 200, nil)
	environment, _ := json.Marshal(obj{"CODEX_HOME": home + "/."})
	if _, err := h.App.DB.InsertProject(&store.Project{Name: "alias", TargetID: target.ID, RepoPath: t.TempDir(), EnvJSON: string(environment)}); err != nil {
		t.Fatal(err)
	}
	var start obj
	h.decode("POST", "/api/conversation-search", obj{"query": "needle", "agent": "codex", "target_id": target.ID}, 202, &start)
	result := waitNativeSearch(t, h, start["id"].(string))
	if len(result["scopes"].([]any)) != 2 || len(result["results"].([]any)) != 1 {
		t.Fatal(result)
	}
	hit := result["results"].([]any)[0].(map[string]any)
	if _, err := h.App.DB.Exec("UPDATE targets SET command_prefix=? WHERE id=?", "changed", target.ID); err != nil {
		t.Fatal(err)
	}
	h.decode("GET", "/api/conversation-search/"+start["id"].(string)+"/results/"+hit["id"].(string), nil, 409, nil)
}

func TestNativeSearchCancellationAndActiveJobLimit(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "slow search", Kind: "local"})
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte("#!/bin/sh\nexec /bin/sleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "codex", "env": obj{"PATH": bin + ":/usr/bin:/bin"}}}, 200, nil)
	ids := []string{}
	for i := 0; i < 4; i++ {
		var start obj
		h.decode("POST", "/api/conversation-search", obj{"query": "needle", "agent": "codex", "target_id": target.ID}, 202, &start)
		ids = append(ids, start["id"].(string))
	}
	h.decode("POST", "/api/conversation-search", obj{"query": "needle", "agent": "codex", "target_id": target.ID}, 429, nil)
	for _, id := range ids {
		h.decode("DELETE", "/api/conversation-search/"+id, nil, 202, nil)
	}
	for _, id := range ids {
		result := waitNativeSearch(t, h, id)
		if result["complete"] != false || !strings.Contains(fmt.Sprint(result), "paused") {
			t.Fatal(result)
		}
	}
}
