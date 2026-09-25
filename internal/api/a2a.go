package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/a2a"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// A2A (Agent2Agent, protocol v1.0, JSON-RPC 2.0 binding) is the door other
// orchestrators come in through: a public Agent Card at
// /.well-known/agent-card.json, and one JSON-RPC endpoint at /a2a/v1 that
// files and dispatches Lectern tasks.
//
// It is deliberately a thin adapter. Everything it does — validating a
// create, queueing an attempt, stopping one — goes through the same functions
// the REST handlers call (buildTask, queueTask, cancelTaskRecord in
// internal/api/tasks.go), so a task that arrives over A2A is an ordinary task
// in every respect: same board card, same worktree, same review flow.

// a2aOwner is the created_by value every task filed over the protocol carries.
// It is how ListTasks finds its own work, and how the board and the storage
// layer tell a protocol-created task from a person's.
const a2aOwner = "a2a"

// A2A task ids are Lectern's task ids as decimal strings, so a client can
// cross-reference the board and the REST API.
func a2aTaskID(id int64) string { return strconv.FormatInt(id, 10) }

// ---- routes --------------------------------------------------------------

// a2aAgentCard serves the Agent Card. It is public metadata — registered by
// the mux outside withAuth's gate — and carries nothing about this install
// beyond its own name, version and the URL the operator configured.
func (s *Server) a2aAgentCard(w http.ResponseWriter, r *http.Request) {
	base := ""
	if s.Cfg != nil {
		base = s.Cfg.BaseURL
	}
	writeJSON(w, 200, a2a.Card(base))
}

// a2aRPC is the JSON-RPC 2.0 entry point. Every reply is HTTP 200 carrying
// either a result or a JSON-RPC error object, which is what the binding
// expects; a transport-level status would be invisible to a JSON-RPC client.
func (s *Server) a2aRPC(w http.ResponseWriter, r *http.Request) {
	var req a2a.Request
	if err := decodeBody(r, &req); err != nil {
		a2aFail(w, nil, a2a.CodeInvalidRequest, "invalid JSON-RPC request: "+err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		a2aFail(w, req.ID, a2a.CodeInvalidRequest, `jsonrpc must be "2.0"`)
		return
	}
	if req.Method == "" {
		a2aFail(w, req.ID, a2a.CodeInvalidRequest, "method is required")
		return
	}
	switch req.Method {
	case a2a.MethodSendMessage:
		s.a2aSendMessage(w, r, req)
	case a2a.MethodGetTask:
		s.a2aGetTask(w, r, req)
	case a2a.MethodListTasks:
		s.a2aListTasks(w, r, req)
	case a2a.MethodCancelTask:
		s.a2aCancelTask(w, r, req)
	default:
		a2aFail(w, req.ID, a2a.CodeMethodNotFound, "unknown method "+req.Method,
			map[string]any{"methods": a2a.Methods})
	}
}

// ---- methods -------------------------------------------------------------

// a2aSendMessage files a task and dispatches it. The project comes from
// message metadata (a registered project name — the same string a task's
// contextId reports); metadata.agent, metadata.model, metadata.title and
// metadata.variants refine it.
func (s *Server) a2aSendMessage(w http.ResponseWriter, r *http.Request, req a2a.Request) {
	var params a2a.SendMessageParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		a2aFail(w, req.ID, a2a.CodeInvalidParams, "params.message must be an A2A message object")
		return
	}
	prompt := strings.TrimSpace(params.Message.Text())
	if prompt == "" {
		a2aFail(w, req.ID, a2a.CodeInvalidParams, "message.parts must include a non-empty text part")
		return
	}
	meta := params.Message.Metadata

	projects, err := s.DB.Projects()
	if err != nil {
		a2aFail(w, req.ID, a2a.CodeInternalError, err.Error())
		return
	}
	names := make([]string, 0, len(projects))
	var project *store.Project
	want := a2aMetaString(meta, "project")
	for _, p := range projects {
		names = append(names, p.Name)
		if want != "" && strings.EqualFold(p.Name, want) {
			project = p
		}
	}
	if project == nil {
		why := "metadata.project is required"
		if want != "" {
			why = fmt.Sprintf("metadata.project %q is not a registered project", want)
		}
		a2aFail(w, req.ID, a2a.CodeInvalidParams,
			why+" — it must be one of: "+strings.Join(names, ", "),
			map[string]any{"projects": names})
		return
	}

	variants, verr := a2aVariants(meta)
	if verr != nil {
		a2aFail(w, req.ID, a2a.CodeInvalidParams, verr.Error())
		return
	}
	// Validate the agents a variant names before anything is filed, so a bad
	// name cannot leave a card behind with nothing to run it.
	for i, v := range variants {
		if v.Agent != "" && !s.knownAgent(v.Agent) {
			a2aFail(w, req.ID, a2a.CodeInvalidParams,
				fmt.Sprintf("metadata.variants[%d].agent must be one of %v", i, s.knownAgentNames()))
			return
		}
	}

	in := taskIn{
		ProjectID: project.ID,
		Title:     a2aTitle(meta, prompt),
		Prompt:    prompt,
		Labels:    []string{a2aOwner},
		createdBy: a2aOwner,
	}
	if v := a2aMetaString(meta, "agent"); v != "" {
		in.Agent = &v
	}
	in.Model = a2aMetaString(meta, "model")
	if v := a2aMetaString(meta, "permission_mode"); v != "" {
		in.PermissionMode = &v
	}

	task, err := s.buildTask(in)
	if err != nil {
		a2aFailFromErr(w, req.ID, err)
		return
	}
	fresh, err := s.queueTask(task, dispatchIn{Variants: variants})
	if err != nil {
		// The card was filed but could not be queued. Cancel it rather than
		// leaving a backlog card nobody is waiting on: cancelled is the only
		// move the state machine allows from a fresh card, and the board then
		// shows what happened instead of a task that looks pending forever.
		s.DB.Update("tasks", task.ID, map[string]any{
			"status": "cancelled", "updated_at": store.Now()})
		a2aFailFromErr(w, req.ID, err)
		return
	}
	a2aWrite(w, req.ID, map[string]any{"task": s.a2aTask(fresh)})
}

// a2aGetTask reports one task. Unlike ListTasks it is not limited to tasks
// filed over the protocol — an orchestrator may hold a task id from anywhere
// on the board, and the auth gate is what protects it.
func (s *Server) a2aGetTask(w http.ResponseWriter, r *http.Request, req a2a.Request) {
	task, ok := s.a2aTaskParam(w, req)
	if !ok {
		return
	}
	a2aWrite(w, req.ID, map[string]any{"task": s.a2aTask(task)})
}

// a2aListTasks lists the tasks this protocol has filed, newest first,
// optionally scoped to one project by contextId. It is the read side of the
// "project-status" skill: a project's board as A2A sees it.
func (s *Server) a2aListTasks(w http.ResponseWriter, r *http.Request, req a2a.Request) {
	var params a2a.ListTasksParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			a2aFail(w, req.ID, a2a.CodeInvalidParams, "params must be an object")
			return
		}
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	// The contextId filter is a project name resolved per task, so it is
	// applied in Go: read a bounded recent window, then narrow and cap it.
	rows, err := s.DB.TasksWhere(
		"created_by=? ORDER BY updated_at DESC LIMIT 200", a2aOwner)
	if err != nil {
		a2aFail(w, req.ID, a2a.CodeInternalError, err.Error())
		return
	}
	out := []a2a.Task{}
	for _, t := range rows {
		if params.ContextID != "" && !strings.EqualFold(s.a2aContextID(t), params.ContextID) {
			continue
		}
		out = append(out, *s.a2aTask(t))
		if len(out) == limit {
			break
		}
	}
	a2aWrite(w, req.ID, map[string]any{"tasks": out})
}

// a2aCancelTask stops a task through the same scheduler path the REST cancel
// does. A task already past being cancelable (completed, failed, already
// cancelled, or waiting for review) is returned as it is rather than with an
// error: its state is the answer, and the caller can see it.
func (s *Server) a2aCancelTask(w http.ResponseWriter, r *http.Request, req a2a.Request) {
	task, ok := s.a2aTaskParam(w, req)
	if !ok {
		return
	}
	if err := s.a2aWritable(task); err != nil {
		a2aFail(w, req.ID, a2a.CodeInvalidParams, err.Error())
		return
	}
	if task.Status != "queued" && task.Status != "running" {
		a2aWrite(w, req.ID, map[string]any{"task": s.a2aTask(task)})
		return
	}
	fresh, err := s.cancelTaskRecord(r.Context(), task)
	if err != nil {
		a2aFailFromErr(w, req.ID, err)
		return
	}
	a2aWrite(w, req.ID, map[string]any{"task": s.a2aTask(fresh)})
}

// a2aTaskParam resolves params.id to a task, or reports -32001.
func (s *Server) a2aTaskParam(w http.ResponseWriter, req a2a.Request) (*store.Task, bool) {
	var params a2a.GetTaskParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			a2aFail(w, req.ID, a2a.CodeInvalidParams, "params must be an object")
			return nil, false
		}
	}
	if strings.TrimSpace(params.ID) == "" {
		a2aFail(w, req.ID, a2a.CodeInvalidParams, "params.id is required")
		return nil, false
	}
	id, ok := a2a.TaskID(strings.TrimSpace(params.ID))
	if !ok {
		// An id that names nothing is the same answer either way: there is no
		// such task.
		a2aFail(w, req.ID, a2a.CodeTaskNotFound, "no task with id "+params.ID)
		return nil, false
	}
	task, err := s.DB.Task(id)
	if err != nil {
		a2aFail(w, req.ID, a2a.CodeTaskNotFound, "no task with id "+params.ID)
		return nil, false
	}
	return task, true
}

// a2aWritable applies the same ownership rules the REST surface enforces
// (taskParam in internal/api/tasks.go) to a protocol call: an autonomous
// workshop task belongs to its isolated worker, and a task being taken over
// belongs to the session receiving it. Neither is something an outside
// orchestrator may act on, whatever surface it arrives through.
func (s *Server) a2aWritable(task *store.Task) error {
	if task.CreatedBy == autoOwner {
		return fmt.Errorf("task %d is a workshop task; manage it from Autonomous workshop", task.ID)
	}
	if tr, _ := s.DB.Takeover(task.ID); tr != nil {
		return fmt.Errorf("task %d is moving to an interactive session; open its session to continue", task.ID)
	}
	return nil
}

// ---- projection ----------------------------------------------------------

// a2aTask renders one Lectern task as the protocol's Task: the board column as
// a TASK_STATE_*, the project name as the contextId, the attempt's report and
// diff summary as an artifact, and the prompt (plus the agent's report, once
// there is one) as history.
func (s *Server) a2aTask(task *store.Task) *a2a.Task {
	att, report := s.latestAttemptReport(task.ID)
	out := &a2a.Task{
		ID:        a2aTaskID(task.ID),
		ContextID: s.a2aContextID(task),
		Status: a2a.TaskStatus{
			State:     a2a.TaskState(task.Status),
			Timestamp: a2a.Timestamp(task.UpdatedAt),
		},
		Artifacts: []a2a.Artifact{},
		History: []a2a.Message{{
			MessageID: fmt.Sprintf("task-%d-prompt", task.ID),
			Role:      a2a.RoleUser,
			Parts:     []a2a.Part{a2a.TextPart(task.Prompt)},
		}},
	}
	if att != nil {
		if art, ok := a2aArtifact(att, report); ok {
			out.Artifacts = append(out.Artifacts, art)
		}
		if strings.TrimSpace(report) != "" {
			out.History = append(out.History, a2a.Message{
				MessageID: fmt.Sprintf("task-%d-attempt-%d", task.ID, att.N),
				Role:      a2a.RoleAgent,
				Parts:     []a2a.Part{a2a.TextPart(report)},
			})
		}
	}
	out.Status.Message = a2aStatusMessage(task, att, report)
	return out
}

// a2aContextID is the group a task belongs to: its project's name, which is
// also what ListTasks filters on. A task whose project row is gone (deleted
// mid-flight) still gets a stable, non-empty id rather than an empty string.
func (s *Server) a2aContextID(task *store.Task) string {
	if proj, err := s.DB.Project(task.ProjectID); err == nil {
		return proj.Name
	}
	return fmt.Sprintf("project-%d", task.ProjectID)
}

// a2aStatusMessage carries the "why" that belongs next to a state: what the
// agent is waiting for at review, and what went wrong on a failure. Every
// other state speaks for itself.
func a2aStatusMessage(task *store.Task, att *store.Attempt, report string) *a2a.Message {
	switch task.Status {
	case "review":
		text := "The attempt finished and is waiting for a human decision on the Lectern " +
			"board. Read the artifact for its report and diff summary; the decision accepts " +
			"it or sends it back for another attempt."
		return &a2a.Message{Role: a2a.RoleAgent, Parts: []a2a.Part{a2a.TextPart(text)}}
	case "failed":
		text := strings.TrimSpace(report)
		if text == "" {
			text = "The attempt failed."
			if att != nil && att.ExitCode != nil {
				text = fmt.Sprintf("The attempt failed with exit code %d.", *att.ExitCode)
			}
		}
		return &a2a.Message{Role: a2a.RoleAgent, Parts: []a2a.Part{a2a.TextPart(text)}}
	}
	return nil
}

// a2aArtifact is an attempt's report and diff stat as text. It is omitted
// entirely when the attempt has said nothing yet and changed nothing — an
// empty artifact would read as "produced nothing" rather than "has not got
// there yet".
func a2aArtifact(att *store.Attempt, report string) (a2a.Artifact, bool) {
	parts := []a2a.Part{}
	if strings.TrimSpace(report) != "" {
		parts = append(parts, a2a.TextPart(report))
	}
	if summary := a2aDiffSummary(store.UnjList(att.DiffStatJSON)); summary != "" {
		parts = append(parts, a2a.TextPart(summary))
	}
	if len(parts) == 0 {
		return a2a.Artifact{}, false
	}
	return a2a.Artifact{
		ArtifactID: fmt.Sprintf("attempt-%d", att.N),
		Name:       fmt.Sprintf("Attempt %d", att.N),
		Parts:      parts,
	}, true
}

// a2aDiffSummary renders an attempt's diff stat for a text-only client: the
// totals, then the files themselves.
func a2aDiffSummary(stats []any) string {
	lines := make([]string, 0, len(stats))
	files, added, deleted := 0, 0, 0
	for _, raw := range stats {
		st, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path, _ := st["path"].(string)
		a, d := a2aNum(st["additions"]), a2aNum(st["deletions"])
		files, added, deleted = files+1, added+a, deleted+d
		lines = append(lines, fmt.Sprintf("- %s (+%d/-%d)", path, a, d))
	}
	if files == 0 {
		return ""
	}
	return fmt.Sprintf("Diff: %d file(s), +%d/-%d\n%s",
		files, added, deleted, strings.Join(lines, "\n"))
}

// ---- request helpers -----------------------------------------------------

// a2aVariants reads metadata.variants — the best-of-N knob. It accepts an
// array of agent names (one attempt each, the task's own model), an array of
// {agent, model, permission_mode, launch_profile} objects, or an integer 2..8
// for that many attempts of the task's own single variant. A malformed value
// is an error rather than a silent single attempt: a caller that asked for
// five runs and got one cannot tell from the response alone.
func a2aVariants(meta map[string]any) ([]variantIn, error) {
	raw, ok := meta["variants"]
	if !ok || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case float64:
		n := int(v)
		if float64(n) != v || n < 2 || n > maxDispatchVariants {
			return nil, fmt.Errorf("metadata.variants must be an integer between 2 and %d",
				maxDispatchVariants)
		}
		out := make([]variantIn, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, variantIn{})
		}
		return out, nil
	case []any:
		if len(v) == 0 || len(v) > maxDispatchVariants {
			return nil, fmt.Errorf("metadata.variants must list between 1 and %d attempts",
				maxDispatchVariants)
		}
		out := make([]variantIn, 0, len(v))
		for i, item := range v {
			switch e := item.(type) {
			case string:
				if strings.TrimSpace(e) == "" {
					return nil, fmt.Errorf("metadata.variants[%d] must name an agent", i)
				}
				out = append(out, variantIn{Agent: strings.TrimSpace(e)})
			case map[string]any:
				vi := variantIn{
					Agent:          a2aMetaString(e, "agent"),
					Model:          a2aMetaString(e, "model"),
					PermissionMode: a2aMetaString(e, "permission_mode"),
					LaunchProfile:  a2aMetaString(e, "launch_profile"),
				}
				if vi.Agent == "" && vi.Model == "" && vi.LaunchProfile == "" {
					return nil, fmt.Errorf(
						"metadata.variants[%d] must name an agent, a model or a launch profile", i)
				}
				out = append(out, vi)
			default:
				return nil, fmt.Errorf(
					"metadata.variants[%d] must be an agent name or an object", i)
			}
		}
		return out, nil
	default:
		return nil, errors.New(
			"metadata.variants must be an integer or an array of agent names")
	}
}

// a2aTitle is the card title: metadata.title when the caller set one, else the
// first line of the request.
func a2aTitle(meta map[string]any, prompt string) string {
	if t := a2aMetaString(meta, "title"); t != "" {
		return clip(t, 120)
	}
	line := prompt
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return clip(strings.TrimSpace(line), 120)
}

// a2aMetaString reads a string out of message metadata. A value of any other
// type is treated as absent rather than stringified: guessing at
// {"project": {"name": "x"}} would dispatch the wrong task.
func a2aMetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	v, _ := meta[key].(string)
	return strings.TrimSpace(v)
}

// a2aNum reads a number that came back out of stored JSON.
func a2aNum(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

// ---- envelope ------------------------------------------------------------

func a2aWrite(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, 200, a2a.Response{JSONRPC: "2.0", ID: a2aID(id), Result: result})
}

func a2aFail(w http.ResponseWriter, id json.RawMessage, code int, msg string, data ...any) {
	e := &a2a.Error{Code: code, Message: msg}
	if len(data) > 0 {
		e.Data = data[0]
	}
	writeJSON(w, 200, a2a.Response{JSONRPC: "2.0", ID: a2aID(id), Error: e})
}

// a2aFailFromErr maps a shared-path failure onto JSON-RPC: a request error is
// the caller's to fix (an agent that has no task definition, a task in a state
// that forbids the call), anything else is ours.
func a2aFailFromErr(w http.ResponseWriter, id json.RawMessage, err error) {
	var re *taskRequestError
	if errors.As(err, &re) {
		a2aFail(w, id, a2a.CodeInvalidParams, re.msg)
		return
	}
	a2aFail(w, id, a2a.CodeInternalError, err.Error())
}

// a2aID echoes the caller's id back byte-identical, as JSON-RPC requires; a
// request that arrived without one gets null.
func a2aID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}
