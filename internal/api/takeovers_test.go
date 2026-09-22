package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// One binary speaks headless stream-json, then accepts terminal input when
// resumed. Both halves run as actual processes, in the same git worktree.
const takeoverAgent = `#!/bin/bash
if [[ " $* " == *" -p "* ]]; then
 echo '{"type":"system","subtype":"init","session_id":"takeover-conversation-123"}'
 echo "kept edit" >> NOTES.md
 echo $$ > background-pid
 exec sleep 120
fi
` + fakeInteractiveAgent

func waitTakeover(t *testing.T, r *realRig, id int64) *store.Takeover {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tr, err := r.app.DB.Takeover(id)
		if err != nil {
			t.Fatal(err)
		}
		if tr != nil && tr.Status == "ready" {
			return tr
		}
		if tr != nil && tr.Status == "failed" {
			t.Fatalf("takeover failed: %s", tr.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("takeover did not complete")
	return nil
}

func TestRealRunningRoutineTakeover(t *testing.T) {
	r := newRealRig(t)
	if err := os.WriteFile(r.app.Cfg.ClaudeBin, []byte(takeoverAgent), 0755); err != nil {
		t.Fatal(err)
	}
	// The takeover must continue with the declaration captured when the
	// background attempt was staged, even if the project is edited meanwhile.
	home := filepath.Join(t.TempDir(), "agent-home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.app.DB.Update("projects", r.project, map[string]any{
		"env_json":   `{"HOME":"` + home + `"}`,
		"mcp_json":   `{}`,
		"strict_mcp": 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Exercise the routine itself, rather than a lookalike manually-created task.
	code, raw := r.do("POST", "/api/routines", map[string]any{"name": "Inspect repo", "prompt": "Keep my edits", "project_ids": []int64{r.project}, "agent": "claude", "dispatch": true})
	if code != 201 && code != 200 {
		t.Fatalf("routine: %d %s", code, raw)
	}
	var routine struct{ ID int64 }
	json.Unmarshal(raw, &routine)
	code, raw = r.do("POST", fmt.Sprintf("/api/routines/%d/run", routine.ID), map[string]any{})
	if code != 200 {
		t.Fatalf("run: %d %s", code, raw)
	}
	var result struct{ Tasks []int64 }
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("run response: %s: %v", raw, err)
	}
	if len(result.Tasks) != 1 {
		t.Fatalf("run response: %s", raw)
	}
	id := result.Tasks[0]
	r.waitStatus(id, "running")
	att, _ := r.app.DB.LatestAttempt(id)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(att.WorktreePath, "background-pid")); err == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	pidRaw, err := os.ReadFile(filepath.Join(att.WorktreePath, "background-pid"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.app.DB.Update("projects", r.project, map[string]any{
		"mcp_json": `{"new_tools":{"command":"new-mcp"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/tasks/%d/takeover", id)
	for i := 0; i < 2; i++ {
		code, raw = r.do("POST", path, map[string]any{})
		if code != 202 {
			t.Fatalf("takeover: %d %s", code, raw)
		}
	}
	tr := waitTakeover(t, r, id)
	sess, err := r.app.DB.Session(*tr.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Workdir != att.WorktreePath {
		t.Fatalf("moved worktree: %s != %s", sess.Workdir, att.WorktreePath)
	}
	log := r.waitForLog(sess.Workdir, "argv:", 5*time.Second)
	if !strings.Contains(log, "--resume takeover-conversation-123") || strings.Contains(log, "--continue") {
		t.Fatalf("wrong conversation: %s", log)
	}
	fields := strings.Fields(log)
	for i := range fields {
		if fields[i] == "--mcp-config" && i+1 < len(fields) {
			raw, readErr := os.ReadFile(fields[i+1])
			if readErr != nil || !strings.Contains(string(raw), "mcpServers") || strings.Contains(string(raw), "new_tools") {
				t.Fatalf("takeover did not preserve staged MCP snapshot: %q (%v)", raw, readErr)
			}
			goto checkedMCP
		}
	}
	t.Fatalf("takeover omitted MCP config: %s", log)
checkedMCP:
	if tmuxAlive("=" + att.TmuxSession) {
		t.Fatal("background tmux survived")
	}
	if exec.Command("kill", "-0", strings.TrimSpace(string(pidRaw))).Run() == nil {
		t.Fatal("background process survived")
	}
	code, raw = r.do("POST", fmt.Sprintf("/api/sessions/%d/send", sess.ID), map[string]any{"text": "Now let me steer this"})
	if code != 200 {
		t.Fatalf("send: %d %s", code, raw)
	}
	r.waitForLog(sess.Workdir, "typed:Now let me steer this", 5*time.Second)
	// Re-run recovery as after a restart between launch and the ready receipt.
	r.app.Sched.Stop()
	r.app.DB.Exec("UPDATE task_takeovers SET status='pending' WHERE task_id=?", id)
	r.app.Sched.ProcessTakeovers(context.Background())
	again, _ := r.app.DB.Takeover(id)
	if again.Status != "ready" || *again.SessionID != sess.ID {
		t.Fatalf("recovery changed session: %+v", again)
	}
	if strings.Count(r.sessionLog(sess.Workdir), "argv:") != 1 {
		t.Fatal("recovery launched twice")
	}
	for _, suffix := range []string{"dispatch", "messages", "cleanup"} {
		code, raw = r.do("POST", fmt.Sprintf("/api/tasks/%d/%s", id, suffix), map[string]any{"text": "another writer", "request_id": "no"})
		if code != 409 {
			t.Fatalf("%s was allowed: %d %s", suffix, code, raw)
		}
	}
	// A completed task's worktree ordinarily expires; this one belongs to a session.
	r.app.DB.Update("projects", r.project, map[string]any{"keep_worktrees": 0})
	r.app.DB.Update("attempts", att.ID, map[string]any{"finished_at": 1})
	if _, err := r.app.Sched.Janitor(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	code, raw = r.do("DELETE", fmt.Sprintf("/api/tasks/%d", id), nil)
	if code != 200 {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if _, err := r.app.DB.Task(id); err != store.ErrNotFound {
		t.Fatalf("task retained: %v", err)
	}
	kept, err := os.ReadFile(filepath.Join(sess.Workdir, "NOTES.md"))
	if err != nil || !strings.Contains(string(kept), "kept edit") {
		t.Fatalf("lost edits: %s %v", kept, err)
	}
	if !tmuxAlive("=" + sess.TmuxSession) {
		t.Fatal("cleanup killed interactive session")
	}
}

func TestTakeoverFailureKeepsRunAndRetriesSameRequest(t *testing.T) {
	r := newRealRig(t)
	os.WriteFile(r.app.Cfg.ClaudeBin, []byte(takeoverAgent), 0755)
	id := r.dispatch("retry takeover", "existing task")
	r.waitStatus(id, "running")
	r.app.Sched.Stop()
	att, _ := r.app.DB.LatestAttempt(id)
	settingsPath := filepath.Join(att.WorktreePath, ".lectern", "settings.json")
	settings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(settingsPath, []byte("broken json"), 0644)
	path := fmt.Sprintf("/api/tasks/%d/takeover", id)
	code, raw := r.do("POST", path, map[string]any{})
	if code != 202 {
		t.Fatalf("request: %d %s", code, raw)
	}
	r.app.Sched.ProcessTakeovers(context.Background())
	tr, _ := r.app.DB.Takeover(id)
	if tr.Status != "failed" || !strings.Contains(tr.Error, "settings") {
		t.Fatalf("error not recorded: %+v", tr)
	}
	if !tmuxAlive("=" + att.TmuxSession) {
		t.Fatal("preflight failure killed original run")
	}
	os.WriteFile(settingsPath, settings, 0644)
	code, raw = r.do("POST", path, map[string]any{})
	if code != 202 {
		t.Fatalf("retry: %d %s", code, raw)
	}
	r.app.Sched.ProcessTakeovers(context.Background())
	tr = waitTakeover(t, r, id)
	sess, _ := r.app.DB.Session(*tr.SessionID)
	r.waitForLog(sess.Workdir, "argv:", 5*time.Second)
	raw, err = os.ReadFile(filepath.Join(sess.Workdir, ".lectern", "interactive-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	json.Unmarshal(raw, &config)
	if _, ok := config["hooks"]; ok {
		t.Fatal("cancelled attempt's approval hooks copied to interactive agent")
	}
}

func TestTakeoverWithoutConversationIDCarriesHandoff(t *testing.T) {
	r := newRealRig(t)
	script := strings.Replace(takeoverAgent, `{"type":"system","subtype":"init","session_id":"takeover-conversation-123"}`, `{"type":"assistant","message":{"content":[{"type":"text","text":"Earlier reasoning"}]}}`, 1)
	os.WriteFile(r.app.Cfg.ClaudeBin, []byte(script), 0755)
	id := r.dispatch("handoff fallback", "preserve the original instructions")
	r.waitStatus(id, "running")
	code, raw := r.do("POST", fmt.Sprintf("/api/tasks/%d/takeover", id), map[string]any{})
	if code != 202 {
		t.Fatalf("takeover: %d %s", code, raw)
	}
	tr := waitTakeover(t, r, id)
	sess, _ := r.app.DB.Session(*tr.SessionID)
	log := r.waitForLog(sess.Workdir, "argv:", 5*time.Second)
	if strings.Contains(log, "--resume") || strings.Contains(log, "--continue") || !strings.Contains(log, "takeover.md") {
		t.Fatalf("wrong handoff: %s", log)
	}
	handoff, err := os.ReadFile(filepath.Join(sess.Workdir, ".lectern", "takeover.md"))
	if err != nil || !strings.Contains(string(handoff), "preserve the original instructions") || !strings.Contains(string(handoff), "events.jsonl") {
		t.Fatalf("missing context: %s %v", handoff, err)
	}
}
