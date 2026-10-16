package api_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// postAskUser fires a Scribe AskUser hook (internal/scribe/lecternbridge
// askQuestion) in a goroutine, the same way postPermissionRequest does for
// PermissionRequest: the hook is one held request, so the test decides from
// the other side while it is in flight.
func postAskUser(h *harness, sessID int64, tok string, q obj) <-chan hookResult {
	body, _ := json.Marshal(q)
	out := make(chan hookResult, 1)
	go func() {
		code, respBody := h.rawRequest("POST",
			fmt.Sprintf("/api/hook/session/%d/AskUser", sessID), string(body), tok)
		out <- hookResult{code, respBody}
	}()
	return out
}

func askUserQuestion() obj {
	return obj{
		"kind":            "question",
		"question":        "Which database should the migration target?",
		"options":         []string{"postgres", "sqlite"},
		"body":            "The repo has both drivers.",
		"allow_free_text": true,
	}
}

func TestAskUserShowsQuestionAndReturnsAnswer(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SessionApprovalHold = 5 * time.Second })
	sess, tok := askSession(t, h, "ask-user-answer")
	results := postAskUser(h, sess.id(), tok, askUserQuestion())

	appr := waitPendingSessionApproval(t, h, sess.id())
	if appr.str("tool_name") != "AskUser" {
		t.Fatalf("pending item tool_name = %v", appr)
	}
	if in, _ := appr["input"].(map[string]any); in["question"] != "Which database should the migration target?" {
		t.Fatalf("pending item does not carry the question: %v", appr)
	}
	h.post(fmt.Sprintf("/api/approvals/%d/decision", appr.id()),
		obj{"decision": "approved", "note": "sqlite"}, 200)

	select {
	case r := <-results:
		if r.code != 200 {
			t.Fatalf("hook response code = %d: %s", r.code, r.body)
		}
		var out struct {
			HookSpecificOutput struct {
				HookEventName string `json:"hookEventName"`
				Answer        string `json:"answer"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(r.body, &out); err != nil {
			t.Fatalf("response not JSON: %s", r.body)
		}
		if out.HookSpecificOutput.HookEventName != "AskUser" || out.HookSpecificOutput.Answer != "sqlite" {
			t.Fatalf("hook response = %s", r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the held AskUser hook never returned after being answered")
	}
}

func TestAskUserDenyOrTimeoutGivesNoAnswer(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SessionApprovalHold = 150 * time.Millisecond })
	sess, tok := askSession(t, h, "ask-user-timeout")
	results := postAskUser(h, sess.id(), tok, askUserQuestion())
	waitPendingSessionApproval(t, h, sess.id())

	select {
	case r := <-results:
		if r.code != 200 || strings.TrimSpace(string(r.body)) != "{}" {
			t.Fatalf("unanswered AskUser = %d %s, want 200 {}", r.code, r.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the AskUser hold never timed out")
	}
}

func TestAskUserWithoutQuestionIsIgnored(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SessionApprovalHold = 5 * time.Second })
	sess, tok := askSession(t, h, "ask-user-empty")
	results := postAskUser(h, sess.id(), tok, obj{"kind": "question", "question": "  "})
	select {
	case r := <-results:
		if strings.TrimSpace(string(r.body)) != "{}" {
			t.Fatalf("empty question = %s, want {}", r.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an empty AskUser should not be held")
	}
}

// A plan review is an AskUser item too: the plan travels as the body, the four
// plan answers as options, and the web's Revise answer ("Revise <feedback>")
// comes back as the hook answer Scribe matches on.
func TestAskUserPlanReviewRoundTripsRevision(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SessionApprovalHold = 5 * time.Second })
	sess, tok := askSession(t, h, "ask-user-plan")
	plan := obj{
		"kind":            "plan_review",
		"question":        "Add retries",
		"options":         []string{"Execute", "Clear context and execute", "Revise", "Cancel"},
		"body":            "# Add retries\n\n1. wrap the call",
		"allow_free_text": true,
	}
	results := postAskUser(h, sess.id(), tok, plan)

	appr := waitPendingSessionApproval(t, h, sess.id())
	in, _ := appr["input"].(map[string]any)
	if in["kind"] != "plan_review" || in["body"] != "# Add retries\n\n1. wrap the call" {
		t.Fatalf("pending plan review does not carry the plan: %v", appr)
	}
	if opts, _ := in["options"].([]any); len(opts) != 4 {
		t.Fatalf("pending plan review options = %v, want the four plan answers", in["options"])
	}
	h.post(fmt.Sprintf("/api/approvals/%d/decision", appr.id()),
		obj{"decision": "approved", "note": "Revise add backoff"}, 200)

	select {
	case r := <-results:
		var out struct {
			HookSpecificOutput struct {
				Answer string `json:"answer"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(r.body, &out); err != nil {
			t.Fatalf("response not JSON: %s", r.body)
		}
		if out.HookSpecificOutput.Answer != "Revise add backoff" {
			t.Fatalf("plan revision answer = %q, want %q", out.HookSpecificOutput.Answer, "Revise add backoff")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the held plan review never returned after being answered")
	}
}
