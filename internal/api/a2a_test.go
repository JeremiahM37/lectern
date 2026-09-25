package api_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// ---- helpers -------------------------------------------------------------

// a2aCall posts one JSON-RPC 2.0 request to the protocol endpoint and returns
// its result and error objects. Both may be nil.
func (h *harness) a2aCall(id any, method string, params any) (obj, obj) {
	h.t.Helper()
	body := obj{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		body["params"] = params
	}
	var resp obj
	h.decode("POST", "/a2a/v1", body, 200, &resp)
	if resp.str("jsonrpc") != "2.0" {
		h.t.Fatalf("%s: response is not JSON-RPC 2.0: %v", method, resp)
	}
	return resp.sub("result"), resp.sub("error")
}

// a2aSend files a task over the protocol and returns its Task object.
func (h *harness) a2aSend(project, text string, meta obj) obj {
	h.t.Helper()
	m := obj{"project": project}
	for k, v := range meta {
		m[k] = v
	}
	res, errObj := h.a2aCall(1, "SendMessage", obj{"message": obj{
		"messageId": "m-1", "role": "ROLE_USER",
		"parts": []obj{{"text": text}}, "metadata": m}})
	if errObj != nil {
		h.t.Fatalf("SendMessage failed: %v", errObj)
	}
	task := res.sub("task")
	if task == nil {
		h.t.Fatalf("SendMessage returned no task: %v", res)
	}
	return task
}

func (h *harness) a2aTask(taskID string) obj {
	h.t.Helper()
	res, errObj := h.a2aCall(2, "GetTask", obj{"id": taskID})
	if errObj != nil {
		h.t.Fatalf("GetTask %s failed: %v", taskID, errObj)
	}
	return res.sub("task")
}

func (h *harness) a2aState(taskID string) string {
	h.t.Helper()
	return h.a2aTask(taskID).sub("status").str("state")
}

// seededName is the seeded project's name, which is the A2A contextId.
func (h *harness) seededName() string {
	h.t.Helper()
	return h.getList("/api/projects")[0].str("name")
}

func a2aPartTexts(t *testing.T, list []obj) []string {
	t.Helper()
	out := make([]string, 0, len(list))
	for _, part := range list {
		out = append(out, part.str("text"))
	}
	return out
}

// ---- card ----------------------------------------------------------------

// The card is discovery metadata a stranger reads before it has any
// credential, so it must answer without one.
func TestA2AAgentCardIsPublic(t *testing.T) {
	h := newHarness(t)
	code, raw := h.request("GET", "/.well-known/agent-card.json", nil, nil)
	if code != 200 {
		t.Fatalf("card = %d: %s", code, raw)
	}
	var card obj
	if err := json.Unmarshal(raw, &card); err != nil {
		t.Fatalf("card is not JSON: %v", err)
	}
	if card.str("name") != "Lectern" || card.str("description") == "" || card.str("version") == "" {
		t.Fatalf("card identity: %v", card)
	}
	ifaces := card.list("supportedInterfaces")
	if len(ifaces) != 1 || ifaces[0].str("protocolBinding") != "JSONRPC" ||
		ifaces[0].str("protocolVersion") != "1.0" {
		t.Fatalf("interfaces: %v", ifaces)
	}
	if want := h.URL + "/a2a/v1"; ifaces[0].str("url") != want {
		t.Errorf("interface url = %q, want %q", ifaces[0].str("url"), want)
	}
	skills := card.list("skills")
	if len(skills) != 3 {
		t.Fatalf("skills: %v", skills)
	}
	for i, want := range []string{"dispatch-task", "best-of-n", "project-status"} {
		if skills[i].str("id") != want {
			t.Errorf("skill %d = %q, want %q", i, skills[i].str("id"), want)
		}
	}
	// Nothing about this install beyond the configured URL — in particular
	// not the seeded project's name.
	if strings.Contains(string(raw), h.seededName()) {
		t.Errorf("card leaked a project name: %s", raw)
	}
}

// ---- SendMessage ---------------------------------------------------------

// SendMessage is the whole point: a message becomes a real Lectern task in a
// worktree of the named project, and GetTask follows it from queued to done.
func TestA2ASendMessageFilesDispatchesAndReports(t *testing.T) {
	h := newHarness(t)
	project := h.seededName()

	task := h.a2aSend(project, "Fix the failing pagination test.", obj{"agent": "claude"})
	id := task.str("id")
	if id == "" || task.str("contextId") != project {
		t.Fatalf("task = %v", task)
	}
	switch state := task.sub("status").str("state"); state {
	case "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING":
	default:
		t.Fatalf("a freshly dispatched task is %q, want submitted or working", state)
	}

	// The task is an ordinary board card: the REST view sees it, with the
	// agent it was given and the label that marks where it came from.
	rest := h.get("/api/tasks/" + id)
	if rest.str("status") != "queued" && rest.str("status") != "running" &&
		rest.str("status") != "review" {
		t.Fatalf("REST view of an A2A task = %v", rest)
	}
	if rest.str("agent") != "claude" {
		t.Errorf("agent = %q, want claude", rest.str("agent"))
	}
	if rest.str("created_by") != "a2a" {
		t.Errorf("created_by = %q, want a2a", rest.str("created_by"))
	}

	h.waitUntil("the A2A task to reach review", func() bool {
		return h.a2aState(id) == "TASK_STATE_INPUT_REQUIRED"
	})
	done := h.a2aTask(id)
	status := done.sub("status")
	if status.str("state") != "TASK_STATE_INPUT_REQUIRED" {
		t.Fatalf("state = %v", status)
	}
	// review is the protocol's "a human owes this a decision", so the state
	// carries that message rather than leaving it to the artifact.
	if msg := status.sub("message"); msg.str("role") != "ROLE_AGENT" ||
		!strings.Contains(msg.list("parts")[0].str("text"), "human decision") {
		t.Errorf("review status message = %v", msg)
	}
	if got := a2aPartTexts(t, done.list("artifacts")[0].list("parts")); len(got) == 0 ||
		got[0] == "" {
		t.Errorf("artifact parts = %v", got)
	}
	if h := len(done.list("history")); h < 2 {
		t.Errorf("history has %d messages, want the prompt and the report", h)
	}
}

func TestA2ASendMessageDefaultsToTheProjectsAgent(t *testing.T) {
	h := newHarness(t)
	// A project that names its own default agent must dispatch with it.
	p := h.project("a2a-defaults", obj{"default_agent": "codex"})
	task := h.a2aSend(p.str("name"), "Document the endpoint.", nil)
	if got := h.get("/api/tasks/" + task.str("id")).str("agent"); got != "codex" {
		t.Errorf("agent = %q, want the project default codex", got)
	}
}

// variants is the best-of-N knob: named agents become parallel attempts in
// their own worktrees.
func TestA2ASendMessageBestOfN(t *testing.T) {
	h := newHarness(t)
	task := h.a2aSend(h.seededName(), "Refactor the cache layer.",
		obj{"variants": []any{"claude", "codex"}})
	view := h.waitStatus(mustID(t, task.str("id")), "review")
	attempts := view.list("attempts")
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d: %v", len(attempts), attempts)
	}
	if attempts[0].str("agent") != "claude" || attempts[1].str("agent") != "codex" {
		t.Errorf("attempt agents = %v / %v", attempts[0].str("agent"), attempts[1].str("agent"))
	}

	// The integer form asks for N attempts of the task's own variant.
	repeat := h.a2aSend(h.seededName(), "Try the migration three times.",
		obj{"variants": 3})
	if n := len(h.get("/api/tasks/" + repeat.str("id")).list("attempts")); n != 3 {
		t.Errorf("variants:3 produced %d attempts, want 3", n)
	}
}

// ---- GetTask -------------------------------------------------------------

func TestA2AGetTaskStatesAndArtifacts(t *testing.T) {
	h := newHarness(t)

	// A failing run has to be visible as FAILED, with the agent's own last
	// words attached, because that is all an orchestrator gets to act on.
	task := h.a2aSend(h.seededName(), "[mock:fail] Break the build.", nil)
	h.waitUntil("the failing task to finish", func() bool {
		switch h.a2aState(task.str("id")) {
		case "TASK_STATE_FAILED", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_COMPLETED":
			return true
		}
		return false
	})
	got := h.a2aTask(task.str("id"))
	if state := got.sub("status").str("state"); state != "TASK_STATE_FAILED" {
		t.Fatalf("state = %q, want failed: %v", state, got)
	}
	msg := got.sub("status").sub("message")
	if msg.str("role") != "ROLE_AGENT" || msg.list("parts")[0].str("text") == "" {
		t.Errorf("failure status message = %v", msg)
	}

	// A task id that does not exist, and one that is not an id at all.
	if _, errObj := h.a2aCall(3, "GetTask", obj{"id": "999999"}); errObj.num("code") != -32001 {
		t.Errorf("unknown id error = %v, want -32001", errObj)
	}
	if _, errObj := h.a2aCall(3, "GetTask", obj{"id": "not-a-task"}); errObj.num("code") != -32001 {
		t.Errorf("malformed id error = %v, want -32001", errObj)
	}
	if _, errObj := h.a2aCall(3, "GetTask", obj{}); errObj.num("code") != -32602 {
		t.Errorf("missing id error = %v, want -32602", errObj)
	}
	if _, errObj := h.a2aCall(3, "GetTask", nil); errObj.num("code") != -32602 {
		t.Errorf("missing params error = %v, want -32602", errObj)
	}
	if _, errObj := h.a2aCall(3, "CancelTask", obj{"id": "424242"}); errObj.num("code") != -32001 {
		t.Errorf("cancel of an unknown task = %v, want -32001", errObj)
	}
}

// ---- CancelTask ----------------------------------------------------------

func TestA2ACancelTask(t *testing.T) {
	h := newHarness(t)
	task := h.a2aSend(h.seededName(), "[mock:slow] Rewrite the parser.", nil)
	id := task.str("id")

	res, errObj := h.a2aCall(4, "CancelTask", obj{"id": id})
	if errObj != nil {
		t.Fatalf("CancelTask: %v", errObj)
	}
	if state := res.sub("task").sub("status").str("state"); state != "TASK_STATE_CANCELED" {
		t.Fatalf("state after cancel = %q: %v", state, res)
	}
	// The REST view agrees — it is one task, stopped one way.
	if got := h.get("/api/tasks/" + id).str("status"); got != "cancelled" {
		t.Errorf("REST status = %q, want cancelled", got)
	}
	// Cancelling again is not an error: the task is already where the caller
	// wanted it, so it is returned as it is.
	res, errObj = h.a2aCall(5, "CancelTask", obj{"id": id})
	if errObj != nil {
		t.Fatalf("second CancelTask: %v", errObj)
	}
	if state := res.sub("task").sub("status").str("state"); state != "TASK_STATE_CANCELED" {
		t.Errorf("second cancel state = %q", state)
	}
	if _, errObj := h.a2aCall(6, "CancelTask", obj{"id": "424242"}); errObj.num("code") != -32001 {
		t.Errorf("cancel of an unknown task = %v, want -32001", errObj)
	}
}

// ---- ListTasks -----------------------------------------------------------

func TestA2AListTasks(t *testing.T) {
	h := newHarness(t)
	project := h.seededName()
	mine := h.a2aSend(project, "Add the health endpoint.", nil)

	// A task a person filed on the board is not this protocol's business.
	person := h.task(h.seededProjectID(), "filed by hand", "do something", nil)

	res, errObj := h.a2aCall(7, "ListTasks", nil)
	if errObj != nil {
		t.Fatalf("ListTasks: %v", errObj)
	}
	tasks := res.list("tasks")
	if len(tasks) != 1 || tasks[0].str("id") != mine.str("id") {
		t.Fatalf("ListTasks = %v", tasks)
	}
	for _, task := range tasks {
		if task.str("id") == fmt.Sprint(person.id()) {
			t.Errorf("ListTasks included a task that was not filed over A2A")
		}
	}

	// contextId scopes it to one project (the project-status skill).
	if _, errObj := h.a2aCall(8, "ListTasks", obj{"contextId": project}); errObj != nil {
		t.Fatalf("ListTasks by contextId: %v", errObj)
	}
	res, _ = h.a2aCall(9, "ListTasks", obj{"contextId": "no-such-project"})
	if n := len(res.list("tasks")); n != 0 {
		t.Errorf("ListTasks for an unknown project returned %d tasks", n)
	}
	if _, errObj := h.a2aCall(10, "ListTasks", obj{"limit": "lots"}); errObj.num("code") != -32602 {
		t.Errorf("a non-numeric limit = %v, want -32602", errObj)
	}
}

// ---- errors --------------------------------------------------------------

func TestA2AErrors(t *testing.T) {
	h := newHarness(t)

	if _, errObj := h.a2aCall(1, "FlyTheShip", obj{}); errObj.num("code") != -32601 {
		t.Errorf("unknown method = %v, want -32601", errObj)
	}

	cases := []struct {
		name   string
		params any
	}{
		{"no message", obj{}},
		{"no parts", obj{"message": obj{"role": "ROLE_USER"}}},
		{"empty text", obj{"message": obj{"role": "ROLE_USER", "parts": []obj{{"text": "  "}}}}},
	}
	for _, tc := range cases {
		if _, errObj := h.a2aCall(1, "SendMessage", tc.params); errObj.num("code") != -32602 {
			t.Errorf("%s = %v, want -32602", tc.name, errObj)
		}
	}

	// The project name is required, and a wrong one lists the valid names —
	// a client cannot choose a project it cannot see.
	_, errObj := h.a2aCall(1, "SendMessage", obj{"message": obj{
		"role": "ROLE_USER", "parts": []obj{{"text": "do it"}}}})
	if errObj.num("code") != -32602 {
		t.Fatalf("missing project = %v, want -32602", errObj)
	}
	if !strings.Contains(errObj.str("message"), h.seededName()) {
		t.Errorf("error must list the valid project names: %v", errObj)
	}
	// Project names are plain strings, which obj.list (objects only) drops.
	if projects, _ := errObj.sub("data")["projects"].([]any); len(projects) == 0 {
		t.Errorf("error data should carry the project names: %v", errObj)
	}

	_, errObj = h.a2aCall(1, "SendMessage", obj{"message": obj{
		"role": "ROLE_USER", "parts": []obj{{"text": "do it"}},
		"metadata": obj{"project": "no-such-project"}}})
	if errObj.num("code") != -32602 || !strings.Contains(errObj.str("message"), "no-such-project") {
		t.Errorf("unknown project = %v", errObj)
	}

	// An agent that cannot run tasks is rejected by the same validation the
	// REST API applies.
	_, errObj = h.a2aCall(1, "SendMessage", obj{"message": obj{
		"role": "ROLE_USER", "parts": []obj{{"text": "do it"}},
		"metadata": obj{"project": h.seededName(), "agent": "nope"}}})
	if errObj.num("code") != -32602 || !strings.Contains(errObj.str("message"), "agent must be one of") {
		t.Errorf("unknown agent = %v", errObj)
	}

	// A malformed variants value is refused rather than quietly running once.
	_, errObj = h.a2aCall(1, "SendMessage", obj{"message": obj{
		"role": "ROLE_USER", "parts": []obj{{"text": "do it"}},
		"metadata": obj{"project": h.seededName(), "variants": 99}}})
	if errObj.num("code") != -32602 {
		t.Errorf("bad variants = %v, want -32602", errObj)
	}

	// A request that is not a JSON-RPC envelope at all.
	var resp obj
	h.decode("POST", "/a2a/v1", obj{"method": "GetTask"}, 200, &resp)
	if resp.sub("error").num("code") != -32600 {
		t.Errorf("missing jsonrpc = %v, want -32600", resp)
	}
}

// ---- auth ----------------------------------------------------------------

// The card is public; the method endpoint is gated like /api. Both matter:
// an orchestrator needs the first before it can authenticate at all.
func TestA2AAuthGate(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.AuthToken = "a2a-secret"
		c.Auth = "token"
	})
	body := obj{"jsonrpc": "2.0", "id": 1, "method": "ListTasks"}

	if code, _ := h.request("POST", "/a2a/v1", body, nil); code != 401 {
		t.Errorf("unauthenticated /a2a/v1 = %d, want 401", code)
	}
	code, raw := h.request("GET", "/.well-known/agent-card.json", nil, nil)
	if code != 200 {
		t.Errorf("the card must not need a token: %d %s", code, raw)
	}
	code, raw = h.request("POST", "/a2a/v1", body,
		map[string]string{"Authorization": "Bearer a2a-secret"})
	if code != 200 {
		t.Fatalf("authenticated /a2a/v1 = %d: %s", code, raw)
	}
	var resp obj
	if err := json.Unmarshal(raw, &resp); err != nil || resp.sub("result") == nil {
		t.Fatalf("authenticated response = %s (%v)", raw, err)
	}
}

func mustID(t *testing.T, id string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscan(id, &n); err != nil {
		t.Fatalf("task id %q is not a number: %v", id, err)
	}
	return n
}
