// Triggers let a project pick up work on its own — a labelled GitHub issue,
// an @mention, a Slack message/slash command, or a labelled Linear issue —
// instead of a human always pressing dispatch. See internal/triggers for the
// polling/socket engines and docs/triggers.md for setup and the security
// model. This file is the HTTP surface plus the one adapter
// (CreateTriggerTask) that turns a matched event into a real task through
// the same path a human's "New task" + dispatch always used.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/triggers"
)

// triggerSourceView is what the API returns for one source: everything in
// store.TriggerSource (which already omits secrets_json via its own json
// tags) plus a redacted per-field "is this secret set" map, so the settings
// UI can show "configured"/"not configured" without the value ever reaching
// a browser — the same pattern project.MCPJSON already uses.
type triggerSourceView struct {
	*store.TriggerSource
	Config  map[string]any  `json:"config"`
	Secrets map[string]bool `json:"secrets"`
}

func (s *Server) triggerView(src *store.TriggerSource) triggerSourceView {
	return triggerSourceView{TriggerSource: src, Config: store.UnjObj(src.ConfigJSON), Secrets: triggers.RedactSecrets(src.SecretsJSON)}
}

func (s *Server) listTriggerSources(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	if _, err := s.DB.Project(pid); err != nil {
		httpError(w, 404, "no such project")
		return
	}
	rows, err := s.DB.TriggerSources(pid)
	if err != nil {
		respondErr(w, err)
		return
	}
	out := make([]triggerSourceView, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.triggerView(r))
	}
	writeJSON(w, 200, out)
}

type triggerSourceIn struct {
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Enabled   *bool           `json:"enabled"`
	Config    json.RawMessage `json:"config"`
	Secrets   json.RawMessage `json:"secrets"`
	IntervalS *int            `json:"interval_s"`
}

// triggerAgent is decoded out of a kind-specific config purely to validate
// its "agent" field the same way a task's own agent is validated — every
// config shape in internal/triggers names that field the same way.
type triggerAgent struct {
	Agent string `json:"agent"`
}

func (s *Server) validateTriggerAgent(configJSON string) error {
	var a triggerAgent
	if configJSON == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(configJSON), &a); err != nil || a.Agent == "" {
		return nil
	}
	if _, ok := s.taskAgent(a.Agent); !ok {
		return fmt.Errorf("agent %q has no non-interactive task definition", a.Agent)
	}
	return nil
}

func (s *Server) createTriggerSource(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	if _, err := s.DB.Project(pid); err != nil {
		httpError(w, 404, "no such project")
		return
	}
	var in triggerSourceIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	kind := triggers.Kind(in.Kind)
	configJSON, secretsJSON := jsonOrEmpty(in.Config), jsonOrEmpty(in.Secrets)
	if err := triggers.ValidateConfig(kind, configJSON, secretsJSON); err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	if err := s.validateTriggerAgent(configJSON); err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	interval := 300
	if in.IntervalS != nil && *in.IntervalS > 0 {
		interval = *in.IntervalS
	}
	src := &store.TriggerSource{
		ProjectID: pid, Kind: string(kind), Name: strings.TrimSpace(in.Name),
		Enabled:    in.Enabled == nil || *in.Enabled,
		ConfigJSON: configJSON, SecretsJSON: secretsJSON, IntervalS: interval,
		// A freshly configured GitHub/Linear source starts its cursor at
		// "now": without this, the first poll would treat every historical
		// labelled issue as brand new and fire a task for each one.
		CursorJSON: seedTriggerCursor(kind),
	}
	saved, err := s.DB.InsertTriggerSource(src)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, s.triggerView(saved))
}

func seedTriggerCursor(kind triggers.Kind) string {
	now := time.Now().UTC().Format(time.RFC3339)
	switch kind {
	case triggers.KindGitHub:
		return store.J(map[string]string{"issues_since": now, "comments_since": now})
	case triggers.KindLinear:
		return store.J(map[string]string{"since": now})
	default:
		return "{}"
	}
}

func jsonOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func (s *Server) triggerSourceParam(w http.ResponseWriter, r *http.Request) (*store.TriggerSource, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such trigger source")
		return nil, false
	}
	src, err := s.DB.TriggerSource(id)
	if err != nil {
		httpError(w, 404, "no such trigger source")
		return nil, false
	}
	return src, true
}

func (s *Server) patchTriggerSource(w http.ResponseWriter, r *http.Request) {
	src, ok := s.triggerSourceParam(w, r)
	if !ok {
		return
	}
	var in triggerSourceIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	fields := map[string]any{}
	if in.Name != "" {
		fields["name"] = strings.TrimSpace(in.Name)
	}
	if in.Enabled != nil {
		fields["enabled"] = boolInt(*in.Enabled)
	}
	if in.IntervalS != nil && *in.IntervalS > 0 {
		fields["interval_s"] = *in.IntervalS
	}
	configJSON := src.ConfigJSON
	if len(in.Config) > 0 {
		configJSON = string(in.Config)
	}
	secretsJSON := src.SecretsJSON
	if len(in.Secrets) > 0 {
		secretsJSON = string(in.Secrets)
	}
	if len(in.Config) > 0 || len(in.Secrets) > 0 {
		kind := triggers.Kind(src.Kind)
		if err := triggers.ValidateConfig(kind, configJSON, secretsJSON); err != nil {
			httpError(w, 400, "%s", err.Error())
			return
		}
		if err := s.validateTriggerAgent(configJSON); err != nil {
			httpError(w, 400, "%s", err.Error())
			return
		}
		fields["config_json"] = configJSON
		fields["secrets_json"] = secretsJSON
	}
	if len(fields) > 0 {
		fields["updated_at"] = store.Now()
		if err := s.DB.Update("trigger_sources", src.ID, fields); err != nil {
			respondErr(w, err)
			return
		}
	}
	fresh, err := s.DB.TriggerSource(src.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, s.triggerView(fresh))
}

func (s *Server) deleteTriggerSource(w http.ResponseWriter, r *http.Request) {
	src, ok := s.triggerSourceParam(w, r)
	if !ok {
		return
	}
	if err := s.DB.DeleteTriggerSource(src.ID); err != nil {
		respondErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) testTriggerSource(w http.ResponseWriter, r *http.Request) {
	src, ok := s.triggerSourceParam(w, r)
	if !ok {
		return
	}
	if s.Triggers == nil {
		httpError(w, 503, "triggers are not configured on this server")
		return
	}
	msg, err := s.Triggers.TestConnection(r.Context(), src)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

// triggerEventView adds the task's current title/status alongside the raw
// ledger row, so the settings UI's "recent events" list needs no second
// round trip per row to be useful.
type triggerEventView struct {
	*store.TriggerEvent
	TaskTitle  string `json:"task_title,omitempty"`
	TaskStatus string `json:"task_status,omitempty"`
}

func (s *Server) listTriggerEvents(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	rows, err := s.DB.RecentTriggerEvents(pid, limit)
	if err != nil {
		respondErr(w, err)
		return
	}
	out := make([]triggerEventView, 0, len(rows))
	for _, ev := range rows {
		v := triggerEventView{TriggerEvent: ev}
		if ev.TaskID != nil {
			if t, err := s.DB.Task(*ev.TaskID); err == nil {
				v.TaskTitle, v.TaskStatus = t.Title, t.Status
			}
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

// CreateTriggerTask is internal/triggers.Manager.CreateTask's implementation:
// the one path a matched event reaches the board through, identical to a
// human's "New task" + dispatch except for two things that are the whole
// safety story in requirement 4 — the permission mode is always the
// project's own default (never anything a trigger source's config could
// elevate), and the task is queued and dispatched immediately, since a
// trigger with nobody watching the board is the point.
func (s *Server) CreateTriggerTask(project *store.Project, spec triggers.NewTaskSpec) (*store.Task, error) {
	agent := firstNonEmptyStr(spec.Agent, project.DefaultAgent, "claude")
	if !s.knownAgent(agent) {
		return nil, fmt.Errorf("agent %q is not configured", agent)
	}
	def, ok := s.taskAgent(agent)
	if !ok {
		return nil, fmt.Errorf("agent %q has no non-interactive task definition", agent)
	}
	mode := firstNonEmptyStr(project.DefaultPermissionMode, "acceptEdits")
	if err := taskPermissionError(def, mode); err != nil {
		return nil, err
	}
	title := spec.Title
	if title == "" {
		title = "Triggered task"
	}
	task, err := s.DB.InsertTask(&store.Task{
		ProjectID: project.ID, Title: title, Prompt: spec.Prompt, Status: "backlog",
		Priority: 2, LabelsJSON: store.J(spec.Labels), Agent: agent, Model: spec.Model,
		PermissionMode: mode, BaseBranch: spec.BaseBranch, CreatedBy: spec.CreatedBy,
	})
	if err != nil {
		return nil, err
	}
	s.Bus.Publish("board", "task", task)
	// Queue through the same body as REST and A2A dispatch, so a triggered
	// task is held to budgets (a stop-mode limit refuses it) and every other
	// dispatch check, instead of a second, laxer path.
	fresh, err := s.queueTask(task, dispatchIn{})
	if err != nil {
		return task, fmt.Errorf("created task #%d but dispatch failed: %w", task.ID, err)
	}
	return fresh, nil
}
