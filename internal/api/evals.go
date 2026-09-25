package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/evals"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// ---- suites -------------------------------------------------------------

func (s *Server) listEvalSuites(w http.ResponseWriter, r *http.Request) {
	var projectID int64
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		fmt.Sscanf(pid, "%d", &projectID)
	}
	rows, err := s.DB.EvalSuites(projectID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

type evalSuiteIn struct {
	Name        string `json:"name"`
	ProjectID   int64  `json:"project_id"`
	Description string `json:"description"`
}

func (s *Server) createEvalSuite(w http.ResponseWriter, r *http.Request) {
	var in evalSuiteIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		httpError(w, 422, "name is required")
		return
	}
	if _, err := s.DB.Project(in.ProjectID); err != nil {
		httpError(w, 400, "no such project")
		return
	}
	suite, err := s.DB.InsertEvalSuite(&store.EvalSuite{
		Name: in.Name, ProjectID: in.ProjectID, Description: in.Description})
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, suite)
}

func (s *Server) evalSuiteParam(w http.ResponseWriter, r *http.Request) (*store.EvalSuite, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such suite")
		return nil, false
	}
	suite, err := s.DB.EvalSuite(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpError(w, 404, "no such suite")
		} else {
			respondErr(w, err)
		}
		return nil, false
	}
	return suite, true
}

func (s *Server) getEvalSuite(w http.ResponseWriter, r *http.Request) {
	suite, ok := s.evalSuiteParam(w, r)
	if !ok {
		return
	}
	cases, err := s.DB.EvalCases(suite.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"suite": suite, "cases": replayCaseViews(cases)})
}

func (s *Server) deleteEvalSuite(w http.ResponseWriter, r *http.Request) {
	suite, ok := s.evalSuiteParam(w, r)
	if !ok {
		return
	}
	if err := s.DB.DeleteEvalSuite(suite.ID); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": suite.ID})
}

// ---- cases ----------------------------------------------------------------

type evalCaseIn struct {
	Name         string `json:"name"`
	Prompt       string `json:"prompt"`
	BaseRef      string `json:"base_ref"`
	CheckCommand string `json:"check_command"`
	TimeoutS     int    `json:"timeout_s"`
	SetupCommand string `json:"setup_command"`
}

func (s *Server) createEvalCase(w http.ResponseWriter, r *http.Request) {
	suite, ok := s.evalSuiteParam(w, r)
	if !ok {
		return
	}
	var in evalCaseIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Prompt) == "" {
		httpError(w, 422, "name and prompt are required")
		return
	}
	c, err := s.DB.InsertEvalCase(&store.EvalCase{
		SuiteID: suite.ID, Name: in.Name, Prompt: in.Prompt, BaseRef: in.BaseRef,
		CheckCommand: in.CheckCommand, TimeoutS: in.TimeoutS, SetupCommand: in.SetupCommand,
	})
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, c)
}

func (s *Server) deleteEvalCase(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such case")
		return
	}
	if err := s.DB.DeleteEvalCase(id); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": id})
}

// ---- import from repo -------------------------------------------------------

type evalImportIn struct {
	ProjectID int64 `json:"project_id"`
}

// importEvalSuites reads every `.lectern/evals/*.yaml` file in a project's
// repo ON THE TARGET (never the control plane's filesystem — a remote
// project's suite files live only on its own target) and creates one suite
// per file, replacing any existing suite of the same name so re-importing
// after an edit is idempotent rather than piling up duplicates.
func (s *Server) importEvalSuites(w http.ResponseWriter, r *http.Request) {
	var in evalImportIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	project, err := s.DB.Project(in.ProjectID)
	if err != nil {
		httpError(w, 400, "no such project")
		return
	}
	target, err := s.DB.Target(project.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	dir := shellq.Quote(project.RepoPath + "/.lectern/evals")
	listing, err := ex.Run(r.Context(), fmt.Sprintf("ls %s/*.yaml %s/*.yml 2>/dev/null", dir, dir),
		executor.RunOpts{Timeout: 20})
	if err != nil {
		respondErr(w, err)
		return
	}
	var files []string
	for _, line := range strings.Split(listing.Stdout, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	imported := []map[string]any{}
	failed := []map[string]any{}
	for _, path := range files {
		raw, rerr := ex.ReadFile(r.Context(), path, 0)
		if rerr != nil || len(raw) == 0 {
			failed = append(failed, map[string]any{"path": path, "error": "could not read file"})
			continue
		}
		parsed, perr := evals.ParseSuite(raw)
		if perr != nil {
			failed = append(failed, map[string]any{"path": path, "error": perr.Error()})
			continue
		}
		// idempotent re-import: drop any earlier suite of the same name in
		// this project before recreating it from the file
		existing, _ := s.DB.EvalSuites(project.ID)
		for _, e := range existing {
			if strings.EqualFold(e.Name, parsed.Name) {
				s.DB.DeleteEvalSuite(e.ID)
			}
		}
		suite, ierr := s.DB.InsertEvalSuite(&store.EvalSuite{
			Name: parsed.Name, ProjectID: project.ID, Description: parsed.Description})
		if ierr != nil {
			failed = append(failed, map[string]any{"path": path, "error": ierr.Error()})
			continue
		}
		for _, c := range parsed.Cases {
			if _, cerr := s.DB.InsertEvalCase(&store.EvalCase{
				SuiteID: suite.ID, Name: c.Name, Prompt: c.Prompt, BaseRef: c.BaseRef,
				CheckCommand: c.CheckCommand, TimeoutS: c.TimeoutS, SetupCommand: c.SetupCommand,
			}); cerr != nil {
				failed = append(failed, map[string]any{"path": path, "error": cerr.Error()})
			}
		}
		imported = append(imported, map[string]any{"path": path, "suite_id": suite.ID, "name": suite.Name,
			"cases": len(parsed.Cases)})
	}
	writeJSON(w, 200, map[string]any{"imported": imported, "failed": failed})
}

// ---- runs -------------------------------------------------------------------

type evalRunIn struct {
	Variants  []variantIn `json:"variants"`
	Repeats   int         `json:"repeats"`
	Notes     string      `json:"notes"`
	WithJudge bool        `json:"with_judge"`
}

const maxEvalRepeats = 10

// createEvalRun validates and queues a run; internal/api/evals_engine.go's
// tick does the actual dispatching, case x variant x repeat, respecting the
// concurrency setting — this handler only creates the row that tells it to.
func (s *Server) createEvalRun(w http.ResponseWriter, r *http.Request) {
	suite, ok := s.evalSuiteParam(w, r)
	if !ok {
		return
	}
	cases, err := s.DB.EvalCases(suite.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	if len(cases) == 0 {
		httpError(w, 409, "suite has no cases")
		return
	}
	var in evalRunIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if len(in.Variants) == 0 {
		httpError(w, 422, "at least one variant is required")
		return
	}
	if len(in.Variants) > maxDispatchVariants {
		httpError(w, 422, "at most %d variants per run", maxDispatchVariants)
		return
	}
	if in.Repeats <= 0 {
		in.Repeats = 1
	}
	if in.Repeats > maxEvalRepeats {
		httpError(w, 422, "at most %d repeats per run", maxEvalRepeats)
		return
	}
	project, err := s.DB.Project(suite.ProjectID)
	if err != nil {
		respondErr(w, err)
		return
	}
	resolved := make([]store.EvalVariant, 0, len(in.Variants))
	for _, v := range in.Variants {
		agent, model, permission, verr := s.resolveVariant(v,
			orDefault(project.DefaultAgent, "claude"), orDefault(project.DefaultPermissionMode, "acceptEdits"))
		if verr != nil {
			httpError(w, 422, "%s", verr.Error())
			return
		}
		resolved = append(resolved, store.EvalVariant{Agent: agent, Model: model, PermissionMode: permission})
	}
	run, err := s.DB.InsertEvalRun(&store.EvalRun{
		SuiteID: suite.ID, Status: "queued", VariantsJSON: store.J(resolved), Repeats: in.Repeats, Notes: in.Notes,
		WithJudge: in.WithJudge,
	})
	if err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "eval_run", run)
	writeJSON(w, 201, run)
}

func (s *Server) evalRunParam(w http.ResponseWriter, r *http.Request) (*store.EvalRun, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such run")
		return nil, false
	}
	run, err := s.DB.EvalRun(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpError(w, 404, "no such run")
		} else {
			respondErr(w, err)
		}
		return nil, false
	}
	return run, true
}

func (s *Server) listEvalRuns(w http.ResponseWriter, r *http.Request) {
	suite, ok := s.evalSuiteParam(w, r)
	if !ok {
		return
	}
	runs, err := s.DB.EvalRuns(suite.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, runs)
}

// evalRunView is a run plus everything its page renders: the matrix (raw
// results — the frontend groups them by case x variant), and the
// leaderboard math from internal/evals, computed fresh every read rather
// than cached, since a running run's numbers change under it.
func (s *Server) evalRunView(run *store.EvalRun) (map[string]any, error) {
	suite, err := s.DB.EvalSuite(run.SuiteID)
	if err != nil {
		return nil, err
	}
	cases, err := s.DB.EvalCases(run.SuiteID)
	if err != nil {
		return nil, err
	}
	results, err := s.DB.EvalResults(run.ID)
	if err != nil {
		return nil, err
	}
	var variants []store.EvalVariant
	_ = json.Unmarshal([]byte(run.VariantsJSON), &variants)
	return map[string]any{
		"run": run, "suite": suite, "cases": replayCaseViews(cases), "variants": variants,
		"results": results, "leaderboard": evals.Leaderboard(results),
	}, nil
}

func (s *Server) getEvalRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.evalRunParam(w, r)
	if !ok {
		return
	}
	view, err := s.evalRunView(run)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, view)
}

// cancelEvalRun stops a run: nothing new is dispatched from it, and every
// cell still queued or running (with a live task attempt) is cancelled. The
// engine tick (evals_engine.go) is what actually reaches into the scheduler;
// this just flips the status it watches for.
func (s *Server) cancelEvalRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.evalRunParam(w, r)
	if !ok {
		return
	}
	if run.Status == "done" || run.Status == "cancelled" {
		httpError(w, 409, "run already %s", run.Status)
		return
	}
	if err := s.cancelEvalRunNow(r.Context(), run); err != nil {
		respondErr(w, err)
		return
	}
	fresh, _ := s.DB.EvalRun(run.ID)
	writeJSON(w, 200, fresh)
}

func (s *Server) compareEvalRuns(w http.ResponseWriter, r *http.Request) {
	runA, ok := s.evalRunParam(w, r)
	if !ok {
		return
	}
	otherID, err := pathID(r, "other")
	if err != nil {
		httpError(w, 404, "no such run")
		return
	}
	runB, err := s.DB.EvalRun(otherID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpError(w, 404, "no such run")
		} else {
			respondErr(w, err)
		}
		return
	}
	resultsA, err := s.DB.EvalResults(runA.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	resultsB, err := s.DB.EvalResults(runB.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"run_a": runA, "run_b": runB,
		"leaderboard_a": evals.Leaderboard(resultsA), "leaderboard_b": evals.Leaderboard(resultsB),
		"cases": evals.CompareRuns(resultsA, resultsB),
	})
}
