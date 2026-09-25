package a2a

import (
	"encoding/json"
	"testing"
)

// TaskState is the whole mapping between Lectern's board and the protocol;
// every column has to land somewhere a driving orchestrator can act on.
func TestTaskStateMapping(t *testing.T) {
	for _, tc := range []struct{ lectern, want string }{
		{"queued", StateSubmitted},
		{"backlog", StateSubmitted},
		{"running", StateWorking},
		{"review", StateInputRequired},
		{"done", StateCompleted},
		{"failed", StateFailed},
		{"cancelled", StateCanceled},
		{"something-new", StateSubmitted},
	} {
		if got := TaskState(tc.lectern); got != tc.want {
			t.Errorf("TaskState(%q) = %q, want %q", tc.lectern, got, tc.want)
		}
	}
}

// Part member presence is the discriminator the binding uses to tell one part
// type from another, so what is on the wire is the contract.
func TestPartMemberPresence(t *testing.T) {
	raw, err := json.Marshal(TextPart("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"text":"hello"}` {
		t.Errorf("text part = %s", raw)
	}

	empty := ""
	raw, err = json.Marshal(Part{Text: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"text":""}` {
		t.Errorf("present-but-empty text part = %s", raw)
	}

	raw, err = json.Marshal(Part{})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{}` {
		t.Errorf("absent text member = %s", raw)
	}

	var back Part
	if err := json.Unmarshal([]byte(`{"text":"hi"}`), &back); err != nil {
		t.Fatal(err)
	}
	if v, ok := back.Value(); !ok || v != "hi" {
		t.Errorf("round trip = %q %v", v, ok)
	}
}

func TestMessageText(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"messageId":"m1","role":"ROLE_USER",
		"parts":[{"text":"first"},{"text":"second"}]}`), &m); err != nil {
		t.Fatal(err)
	}
	if got := m.Text(); got != "first\nsecond" {
		t.Errorf("Text() = %q", got)
	}
	if m.Role != RoleUser {
		t.Errorf("role = %q", m.Role)
	}
}

func TestTaskIDAndTimestamp(t *testing.T) {
	if n, ok := TaskID("42"); !ok || n != 42 {
		t.Errorf("TaskID(42) = %d %v", n, ok)
	}
	for _, bad := range []string{"", "abc", "0", "-3", "4.5"} {
		if n, ok := TaskID(bad); ok {
			t.Errorf("TaskID(%q) accepted as %d", bad, n)
		}
	}
	if got := Timestamp(1758660000); got != "2025-09-23T20:40:00Z" {
		t.Errorf("Timestamp = %q", got)
	}
}

// The envelope must omit whichever half of the response is not in use: a
// result with an "error":null next to it is valid JSON-RPC but tells a client
// to look for a failure.
func TestResponseOmissions(t *testing.T) {
	raw, err := json.Marshal(Response{JSONRPC: "2.0", ID: json.RawMessage("1"),
		Result: map[string]any{"tasks": []Task{}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"jsonrpc":"2.0","id":1,"result":{"tasks":[]}}` {
		t.Errorf("result response = %s", got)
	}

	raw, err = json.Marshal(Response{JSONRPC: "2.0", ID: json.RawMessage("1"),
		Error: &Error{Code: CodeTaskNotFound, Message: "no such task"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"no such task"}}` {
		t.Errorf("error response = %s", got)
	}
}
