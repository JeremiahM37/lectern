// Package a2a implements the server half of the Agent2Agent (A2A) protocol
// v1.0 (Linux Foundation) over its JSON-RPC 2.0 binding: the public Agent
// Card, the request/response envelope, the Task object, and the mapping from
// Lectern's board columns onto the protocol's task states.
//
// The HTTP wiring — which Lectern project a message names, which agent runs
// it, auth — lives in internal/api/a2a.go. This package stays transport-free
// so the card and the state mapping are testable on their own, and so nothing
// here can accidentally depend on the control plane's internals.
package a2a

import (
	"encoding/json"
	"strconv"
	"time"
)

// The two paths the protocol is served at, relative to the server root.
const (
	// CardPath is where the Agent Card is served. It is unauthenticated
	// public metadata; internal/api serves it outside the auth gate.
	CardPath = "/.well-known/agent-card.json"
	// InterfacePath is the JSON-RPC binding this card points at.
	InterfacePath = "/a2a/v1"
)

// Method names. A2A fixes these to PascalCase; they are not Lectern's choice,
// and a client that sends anything else gets CodeMethodNotFound.
const (
	MethodSendMessage = "SendMessage"
	MethodGetTask     = "GetTask"
	MethodListTasks   = "ListTasks"
	MethodCancelTask  = "CancelTask"
)

// Methods is the served set, in the order they are advertised.
var Methods = []string{MethodSendMessage, MethodGetTask, MethodListTasks, MethodCancelTask}

// JSON-RPC 2.0 error codes. The binding additionally fixes CodeTaskNotFound
// for "that task id does not exist".
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeTaskNotFound   = -32001
)

// Roles a message part can carry. Parts use member presence as their
// discriminator — a text part is {"text": "..."} — so this package models
// exactly one member: Text.
const (
	RoleUser  = "ROLE_USER"
	RoleAgent = "ROLE_AGENT"
)

// Task states. A Lectern board column is not one of these; TaskState maps the
// column onto the closest state the protocol defines.
const (
	StateSubmitted     = "TASK_STATE_SUBMITTED"
	StateWorking       = "TASK_STATE_WORKING"
	StateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	StateCompleted     = "TASK_STATE_COMPLETED"
	StateFailed        = "TASK_STATE_FAILED"
	StateCanceled      = "TASK_STATE_CANCELED"
	StateRejected      = "TASK_STATE_REJECTED"
)

// TaskState maps a Lectern task status (internal/state.Statuses: backlog,
// queued, running, review, done, failed, cancelled) onto an A2A task state.
//
//	queued    -> TASK_STATE_SUBMITTED   accepted, not started
//	running   -> TASK_STATE_WORKING
//	review    -> TASK_STATE_INPUT_REQUIRED  the agent stopped and a human owes
//	                                        it a decision (accept, or send it back)
//	done      -> TASK_STATE_COMPLETED
//	failed    -> TASK_STATE_FAILED
//	cancelled -> TASK_STATE_CANCELED
//
// "backlog" — a card that exists but was never queued — reads as SUBMITTED:
// from outside, it is accepted work that has not started. An unrecognised
// status (there is none today) also reads as SUBMITTED rather than inventing
// a state the protocol does not define.
func TaskState(status string) string {
	switch status {
	case "queued", "backlog":
		return StateSubmitted
	case "running":
		return StateWorking
	case "review":
		return StateInputRequired
	case "done":
		return StateCompleted
	case "failed":
		return StateFailed
	case "cancelled":
		return StateCanceled
	default:
		return StateSubmitted
	}
}

// Request is one JSON-RPC 2.0 call. ID is kept raw so a client's numeric or
// string id is echoed back byte-identical, as the spec requires.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Response is one JSON-RPC 2.0 result or error. Exactly one of Result and
// Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Part is one piece of a message. Member presence is the discriminator: this
// is a text part exactly when Text is present, at any length — hence the
// pointer, which distinguishes "absent" from "present and empty".
type Part struct {
	Text *string `json:"text,omitempty"`
}

// TextPart builds a text part.
func TextPart(s string) Part { return Part{Text: &s} }

// Value returns the part's text and whether it is a text part at all.
func (p Part) Value() (string, bool) {
	if p.Text == nil {
		return "", false
	}
	return *p.Text, true
}

// Message is one turn in a task's history, or the status message on a task.
type Message struct {
	MessageID string         `json:"messageId,omitempty"`
	Role      string         `json:"role"`
	Parts     []Part         `json:"parts"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Text concatenates a message's text parts, separated by newlines. Non-text
// parts (none exist in v1.0's text/plain default modes) are skipped.
func (m Message) Text() string {
	out := ""
	for _, p := range m.Parts {
		v, ok := p.Value()
		if !ok {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += v
	}
	return out
}

// TaskStatus is a task's state plus, optionally, the agent's own message about
// it — A2A's way of carrying "why" alongside "what".
type TaskStatus struct {
	State     string   `json:"state"`
	Message   *Message `json:"message,omitempty"`
	Timestamp string   `json:"timestamp"`
}

// Artifact is something a task produced. Lectern's artifacts are text: the
// attempt's report and its diff summary.
type Artifact struct {
	ArtifactID string `json:"artifactId"`
	Name       string `json:"name"`
	Parts      []Part `json:"parts"`
}

// Task is the protocol's unit of work. ID is Lectern's task id in decimal, so
// it can also be looked up over the REST API; ContextID is the project name.
type Task struct {
	ID        string     `json:"id"`
	ContextID string     `json:"contextId"`
	Status    TaskStatus `json:"status"`
	Artifacts []Artifact `json:"artifacts"`
	History   []Message  `json:"history,omitempty"`
}

// SendMessageParams is the SendMessage request body. Lectern reads the
// project, agent, model and best-of-N variants from the message metadata.
type SendMessageParams struct {
	Message Message `json:"message"`
}

// GetTaskParams and CancelTaskParams both name one task.
type GetTaskParams struct {
	ID string `json:"id"`
}

// CancelTaskParams names the task to cancel.
type CancelTaskParams struct {
	ID string `json:"id"`
}

// ListTasksParams narrows the list. ContextID is a project name — the same
// value GetTask reports as a task's contextId — and an empty ContextID lists
// every project.
type ListTasksParams struct {
	ContextID string `json:"contextId,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// TaskID parses a protocol task id (Lectern's decimal task id).
func TaskID(id string) (int64, bool) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// Timestamp renders a Lectern row time (unix seconds) as the RFC 3339 string
// the protocol's status.timestamp is.
func Timestamp(unixSeconds float64) string {
	return time.Unix(int64(unixSeconds), 0).UTC().Format(time.RFC3339)
}
