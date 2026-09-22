package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scheduler"
)

// What one agent learns should not die with its worktree.
func TestAgentLeavesNoteAndNextPromptIncludesIt(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	h.run(pid, "noter", "do work [mock:note]", nil)

	notes := h.getList(fmt.Sprintf("/api/projects/%d/notes", pid))
	if len(notes) != 1 || !strings.Contains(notes[0].str("note"), "bcrypt") {
		t.Fatalf("notes: %v", notes)
	}

	// a second task's staged prompt must carry the memory prefix
	h.run(pid, "reader", "second job", nil)
	var prompt string
	for path, data := range h.mock().Files() {
		if strings.HasSuffix(path, "/prompt.md") && strings.Contains(string(data), "second job") {
			prompt = string(data)
		}
	}
	if prompt == "" {
		t.Fatal("the second task's prompt was never staged")
	}
	if !strings.Contains(prompt, "Project memory") || !strings.Contains(prompt, "bcrypt") {
		t.Fatalf("the note did not reach the next agent:\n%s", prompt)
	}

	if code := h.status("DELETE",
		fmt.Sprintf("/api/projects/%d/notes/%d", pid, notes[0].id()), nil); code != 204 {
		t.Errorf("deleting a note: %d", code)
	}
	if got := h.getList(fmt.Sprintf("/api/projects/%d/notes", pid)); len(got) != 0 {
		t.Errorf("the note survived deletion: %v", got)
	}
}

func TestNoteHookAuthAndValidation(t *testing.T) {
	h := newHarness(t)
	if code := h.status("POST", "/api/hook/notes", obj{"token": "bogus", "note": "x"}); code != 403 {
		t.Fatalf("an unknown token must be refused, got %d", code)
	}
	task := h.task(h.seededProjectID(), "note host", "x [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "running")
	token := h.attemptToken(task.id())
	if code := h.status("POST", "/api/hook/notes", obj{"token": token, "note": "   "}); code != 400 {
		t.Errorf("an empty note must be rejected, got %d", code)
	}
}

func TestNotesPrefixBuilder(t *testing.T) {
	p := scheduler.BuildNotesPrefix([]string{"newest", "older"})
	if strings.Index(p, "older") > strings.Index(p, "newest") {
		t.Error("notes must read chronologically, oldest first")
	}
	if !strings.HasPrefix(p, "## Project memory") {
		t.Errorf("prefix: %q", p)
	}
}

func TestGatedModeRejectedForNonClaude(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	code, body := h.request("POST", "/api/tasks",
		obj{"project_id": pid, "title": "x", "agent": "gemini",
			"permission_mode": "default"}, nil)
	if code != 400 || !strings.Contains(string(body), "gated") {
		t.Fatalf("gated mode on a non-claude agent: %d %s", code, body)
	}
	// an unknown agent is a schema error, not a policy one
	if code := h.status("POST", "/api/tasks",
		obj{"project_id": pid, "title": "x", "agent": "cursor"}); code != 422 {
		t.Errorf("unknown agent: %d", code)
	}
}
