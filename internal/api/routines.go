package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/routines"
	"github.com/JeremiahM37/lectern/v2/internal/scheduler"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// A routine is a job you keep asking for, saved so it is one button — and, if
// you want, one that presses itself on a schedule. "Go through every open PR on
// librarr, sglang and gamarr, test it end to end, fix what is wrong, and merge
// once CI is green" is one routine across three projects, not three prompts
// retyped every week.

type routineIn struct {
	Name           string  `json:"name"`
	Prompt         string  `json:"prompt"`
	Title          string  `json:"title"`
	ProjectIDs     []int64 `json:"project_ids"`
	Agent          string  `json:"agent"`
	Model          string  `json:"model"`
	PermissionMode string  `json:"permission_mode"`
	// Schedule is a pointer so an absent field is distinguishable from an empty
	// one: absent means "leave it alone", empty means "make this manual only".
	// Without that, PATCHing a routine's name silently unscheduled it.
	Schedule *string `json:"schedule"`
	Enabled  *bool   `json:"enabled"`
	Dispatch *bool   `json:"dispatch"`
}

func (s *Server) listRoutines(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Routines()
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) createRoutine(w http.ResponseWriter, r *http.Request) {
	var in routineIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		httpError(w, 400, "a routine needs a name")
		return
	}
	if strings.TrimSpace(in.Prompt) == "" {
		httpError(w, 400, "a routine needs a prompt — it is the whole point of saving one")
		return
	}
	if len(in.ProjectIDs) == 0 {
		httpError(w, 400, "a routine needs at least one project to run against")
		return
	}
	if err := s.validateRoutine(in); err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}

	row := &store.Routine{
		Name: strings.TrimSpace(in.Name), Prompt: in.Prompt, Title: in.Title,
		ProjectIDs: in.ProjectIDs, Agent: in.Agent, Model: in.Model,
		PermissionMode: in.PermissionMode, Schedule: strPtr(in.Schedule),
		Enabled:  in.Enabled == nil || *in.Enabled,
		Dispatch: in.Dispatch == nil || *in.Dispatch,
	}
	if next, ok := s.nextRun(row); ok {
		row.NextRunAt = &next
	}
	saved, err := s.DB.InsertRoutine(row)
	if err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "routine_saved", map[string]any{"id": saved.ID, "name": saved.Name})
	writeJSON(w, 201, saved)
}

func (s *Server) patchRoutine(w http.ResponseWriter, r *http.Request) {
	row, ok := s.routineParam(w, r)
	if !ok {
		return
	}
	var in routineIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	fields := map[string]any{}
	if in.Name != "" {
		fields["name"] = strings.TrimSpace(in.Name)
		row.Name = fields["name"].(string)
	}
	if in.Prompt != "" {
		fields["prompt"] = in.Prompt
	}
	if in.Title != "" {
		fields["title"] = in.Title
	}
	if len(in.ProjectIDs) > 0 {
		if err := s.validateProjects(in.ProjectIDs); err != nil {
			httpError(w, 400, "%s", err.Error())
			return
		}
		raw, _ := json.Marshal(in.ProjectIDs)
		fields["project_ids"] = string(raw)
	}
	// the same validation the create path does — an agent that does not exist
	// saves happily and then fails at dispatch, hours later, on a schedule
	if in.Agent != "" {
		if _, ok := s.taskAgent(in.Agent); !ok {
			httpError(w, 400, "agent %q has no non-interactive task definition", in.Agent)
			return
		}
	}
	projectIDs := row.ProjectIDs
	if len(in.ProjectIDs) > 0 {
		projectIDs = in.ProjectIDs
	}
	permissionMode := row.PermissionMode
	if in.PermissionMode != "" {
		permissionMode = in.PermissionMode
	}
	agent := row.Agent
	if in.Agent != "" {
		agent = in.Agent
	}
	if err := s.validateRoutinePermission(agent, permissionMode, projectIDs); err != nil {
		httpError(w, 400, "%s", err)
		return
	}
	for key, val := range map[string]string{
		"agent": in.Agent, "model": in.Model, "permission_mode": in.PermissionMode,
	} {
		if val != "" {
			fields[key] = val
		}
	}
	if in.Schedule != nil {
		spec := strings.TrimSpace(*in.Schedule)
		if err := routines.Valid(spec); err != nil {
			httpError(w, 400, "%s", err.Error())
			return
		}
		fields["schedule"] = spec
		row.Schedule = spec
		if next, ok := s.nextRun(row); ok {
			fields["next_run_at"] = next
		} else {
			fields["next_run_at"] = nil
		}
	}
	if in.Enabled != nil {
		fields["enabled"] = boolToInt(*in.Enabled)
		row.Enabled = *in.Enabled
		// re-enabling something scheduled should not fire it for every run it
		// missed while it was off
		if *in.Enabled {
			if next, ok := s.nextRun(row); ok {
				fields["next_run_at"] = next
			}
		}
	}
	if in.Dispatch != nil {
		fields["dispatch"] = boolToInt(*in.Dispatch)
	}
	if len(fields) > 0 {
		if err := s.DB.Update("routines", row.ID, fields); err != nil {
			respondErr(w, err)
			return
		}
	}
	fresh, err := s.DB.Routine(row.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, fresh)
}

func (s *Server) deleteRoutine(w http.ResponseWriter, r *http.Request) {
	row, ok := s.routineParam(w, r)
	if !ok {
		return
	}
	// only the saved job goes; the tasks it already created are real work and
	// stay on the board
	if _, err := s.DB.Exec(`DELETE FROM routines WHERE id=?`, row.ID); err != nil {
		respondErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// runRoutine is the button. It creates one task per project and dispatches them.
func (s *Server) runRoutine(w http.ResponseWriter, r *http.Request) {
	row, ok := s.routineParam(w, r)
	if !ok {
		return
	}
	created, failed := s.fireRoutine(row, "user")
	writeJSON(w, 200, map[string]any{
		"routine_id": row.ID, "tasks": created, "failed": failed})
}

// fireRoutine creates and dispatches this routine's tasks, returning the task
// ids it made and a note for every project it could not run against.
//
// One bad project must not stop the rest: a routine over five repositories where
// one has been deleted should still run against the other four and say what it
// skipped, rather than failing whole.
func (s *Server) fireRoutine(row *store.Routine, by string) ([]int64, []string) {
	created := []int64{}
	failed := []string{}
	for _, pid := range row.ProjectIDs {
		proj, err := s.DB.Project(pid)
		if err != nil {
			failed = append(failed, fmt.Sprintf("project %d is gone", pid))
			continue
		}
		title := strings.TrimSpace(row.Title)
		if title == "" {
			title = row.Name
		}
		task, err := s.DB.InsertTask(&store.Task{
			ProjectID: proj.ID, Title: title, Prompt: row.Prompt,
			Status: "queued", Agent: row.Agent, Model: row.Model,
			PermissionMode: row.PermissionMode, CreatedBy: "routine:" + by,
		})
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %s", proj.Name, err))
			continue
		}
		created = append(created, task.ID)
		if row.Dispatch {
			// same path the dispatch button takes, so a routine's task is an
			// ordinary task in every respect
			if _, err := s.Sched.CreateAttempt(task, scheduler.AttemptOpts{}); err != nil {
				failed = append(failed, fmt.Sprintf("%s: %s", proj.Name, err))
			}
		}
	}
	now := store.Now()
	fields := map[string]any{"last_run_at": now}
	if next, ok := s.nextRun(row); ok {
		fields["next_run_at"] = next
	}
	s.DB.Update("routines", row.ID, fields)
	s.Bus.Publish("board", "routine_ran", map[string]any{
		"id": row.ID, "name": row.Name, "tasks": len(created), "failed": len(failed)})
	s.Log.Info("routine ran", "routine", row.Name, "by", by,
		"tasks", len(created), "failed", len(failed))
	return created, failed
}

// nextRun is when a routine should fire next, or false when it is manual only.
func (s *Server) nextRun(row *store.Routine) (float64, bool) {
	if !row.Enabled || strings.TrimSpace(row.Schedule) == "" {
		return 0, false
	}
	next, err := routines.ParseSchedule(row.Schedule, time.Now())
	if err != nil {
		return 0, false
	}
	return float64(next.Unix()), true
}

func (s *Server) validateRoutine(in routineIn) error {
	if err := routines.Valid(strPtr(in.Schedule)); err != nil {
		return err
	}
	if in.Agent != "" {
		if _, ok := s.taskAgent(in.Agent); !ok {
			return fmt.Errorf("agent %q has no non-interactive task definition", in.Agent)
		}
	}
	if err := s.validateProjects(in.ProjectIDs); err != nil {
		return err
	}
	if err := s.validateRoutinePermission(in.Agent, in.PermissionMode, in.ProjectIDs); err != nil {
		return err
	}
	return nil
}

func (s *Server) validateRoutinePermission(agent, mode string, projectIDs []int64) error {
	if agent != "" {
		spec, ok := s.taskAgent(agent)
		if !ok {
			return fmt.Errorf("agent %q has no non-interactive task definition", agent)
		}
		if mode == "" {
			return nil
		}
		return taskPermissionError(spec, mode)
	}
	for _, id := range projectIDs {
		project, err := s.DB.Project(id)
		if err != nil {
			return err
		}
		name := firstNonEmptyStr(project.DefaultAgent, "claude")
		spec, ok := s.taskAgent(name)
		if !ok {
			return fmt.Errorf("project %q default agent %q has no non-interactive task definition", project.Name, name)
		}
		if mode != "" {
			if err := taskPermissionError(spec, mode); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) validateProjects(ids []int64) error {
	for _, id := range ids {
		if _, err := s.DB.Project(id); err != nil {
			return fmt.Errorf("no project %d", id)
		}
	}
	return nil
}

func (s *Server) routineParam(w http.ResponseWriter, r *http.Request) (*store.Routine, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such routine")
		return nil, false
	}
	row, err := s.DB.Routine(id)
	if err != nil {
		httpError(w, 404, "no such routine")
		return nil, false
	}
	return row, true
}

// strPtr reads an optional string field, treating absent as empty.
func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// RunDueRoutines fires every scheduled routine whose time has come. The
// scheduler calls this on its tick.
//
// A routine that cannot be scheduled any more (its schedule was edited to
// something unreadable) is disabled rather than retried forever, and says so in
// the log — a job silently firing every two seconds is a much worse failure than
// one that stops.
func (s *Server) RunDueRoutines(ctx context.Context) {
	due, err := s.DB.DueRoutines(store.Now())
	if err != nil {
		s.Log.Error("listing due routines failed", "err", err)
		return
	}
	for _, row := range due {
		if _, err := routines.ParseSchedule(row.Schedule, time.Now()); err != nil {
			s.Log.Warn("disabling a routine with an unreadable schedule",
				"routine", row.Name, "schedule", row.Schedule, "err", err)
			s.DB.Update("routines", row.ID, map[string]any{
				"enabled": 0, "next_run_at": nil})
			continue
		}
		s.fireRoutine(row, "schedule")
	}
}
