package api

// The daily experiment is a separate controller, not a privileged agent loop.
// Its task rows are receipts; only the constrained OS runner executes them.
import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const autoKey = "autonomous_experiment_v1"
const autoOwner = "autonomous-experiment"
const autoRoot = "/mnt/bulk/lectern-autonomy/jobs"
const autoRunner = "/usr/local/libexec/lectern-autonomy-runner"

type autoJob struct {
	ReportError   string    `json:"report_error,omitempty"`
	ReportRepairs int       `json:"report_repairs,omitempty"`
	ReportRetryAt time.Time `json:"report_retry_at,omitempty"`
	ID            string    `json:"id"`
	TaskID        int64     `json:"task_id"`
	Role          string    `json:"role"`
	Model         string    `json:"model,omitempty"`
	Provider      string    `json:"provider"`
	Status        string    `json:"status"`
	ArtifactPath  string    `json:"artifact_path"`
	Summary       string    `json:"summary,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	Approved      bool      `json:"approved,omitempty"`
	ReviewTaskID  int64     `json:"review_task_id,omitempty"`
}
type autoRecord struct {
	Config             autonomy.Config   `json:"config"`
	State              *autonomy.State   `json:"state"`
	Runs               []*autonomy.State `json:"runs"`
	Jobs               []*autoJob        `json:"jobs"`
	Status             string            `json:"status"`
	Reason             string            `json:"reason"`
	Quota              autonomy.Usage    `json:"quota"`
	RequestedDay       string            `json:"requested_day,omitempty"`
	ProjectID          int64             `json:"project_id"`
	RememberedDay      string            `json:"remembered_day"`
	RetryCount         int               `json:"retry_count"`
	RetryDay           string            `json:"retry_day,omitempty"`
	RetryAt            time.Time         `json:"retry_at,omitempty"`
	NextCycleAt        time.Time         `json:"next_cycle_at,omitempty"`
	NextCycleScheduled bool              `json:"next_cycle_scheduled"`
	RememberedCycle    string            `json:"remembered_cycle,omitempty"`
	StrategyDay        string            `json:"strategy_day,omitempty"`
}

func (s *Server) loadAuto() (*autoRecord, error) {
	a := &autoRecord{Config: autonomy.DefaultConfig(), Status: "off", Runs: []*autonomy.State{}, Jobs: []*autoJob{}}
	if raw := s.DB.Setting(autoKey); raw != "" {
		if err := json.Unmarshal([]byte(raw), a); err != nil {
			return nil, fmt.Errorf("autonomy state unreadable: %w", err)
		}
	}
	return a, a.Config.Validate()
}
func (s *Server) saveAuto(a *autoRecord) error { return s.DB.SetSetting(autoKey, store.J(a)) }
func (s *Server) getAutonomy(w http.ResponseWriter, r *http.Request) {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	a, e := s.loadAuto()
	if e != nil {
		respondErr(w, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, a)
}
func (s *Server) autonomyHuman(w http.ResponseWriter, r *http.Request) bool {
	p, _ := auth.FromContext(r.Context())
	if s.Auth == nil || !s.Auth.CanDecide(p) {
		httpError(w, 403, "autonomous mode requires your signed-in identity; agents cannot enable it")
		return false
	}
	return true
}
func (s *Server) putAutonomy(w http.ResponseWriter, r *http.Request) {
	if !s.autonomyHuman(w, r) {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if e := decodeBody(r, &in); e != nil || in.Enabled == nil {
		httpError(w, 400, "enabled boolean required")
		return
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	a, e := s.loadAuto()
	if e != nil {
		respondErr(w, e)
		return
	}
	if *in.Enabled {
		if _, e = s.runAutoCommand(r.Context(), "probe"); e != nil {
			httpError(w, 503, "isolation preflight failed: %s", e)
			return
		}
		a.Config.Enabled = true
		a.Status = "waiting"
		a.Reason = "Continuous work enabled; independent review between milestones"
	} else {
		a.Config.Enabled = false
		if e = s.saveAuto(a); e != nil {
			respondErr(w, e)
			return
		}
		s.stopAutoJobs(r.Context(), a, "Turned off by you")
	}
	if e = s.saveAuto(a); e != nil {
		respondErr(w, e)
		return
	}
	writeJSON(w, 200, a)
}
func (s *Server) startAutonomy(w http.ResponseWriter, r *http.Request) {
	if !s.autonomyHuman(w, r) {
		return
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	a, e := s.loadAuto()
	if e != nil {
		respondErr(w, e)
		return
	}
	if !a.Config.Enabled {
		httpError(w, 409, "turn autonomous mode on first")
		return
	}
	loc, _ := time.LoadLocation(a.Config.Timezone)
	a.RequestedDay = time.Now().In(loc).Format("2006-01-02")
	if !a.Config.Continuous && a.State != nil && a.State.Date == a.RequestedDay && a.State.Phase == autonomy.Complete {
		httpError(w, 409, "today's cycle is complete; the next plan is tomorrow")
		return
	}
	if a.State != nil && a.State.Phase == autonomy.Paused {
		a.State.Reason = "Retry requested by you"
		for _, id := range a.State.ActiveTaskIDs() {
			if j := autoFindJob(a, id); j != nil && j.Status == "failed" {
				j.Status = "stopped"
			}
		}
	}
	if e = s.saveAuto(a); e != nil {
		respondErr(w, e)
		return
	}
	writeJSON(w, 200, a)
}
func (s *Server) stopAutonomy(w http.ResponseWriter, r *http.Request) {
	if !s.autonomyHuman(w, r) {
		return
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	a, e := s.loadAuto()
	if e != nil {
		respondErr(w, e)
		return
	}
	a.Config.Enabled = false
	if e = s.saveAuto(a); e != nil {
		respondErr(w, e)
		return
	}
	s.stopAutoJobs(r.Context(), a, "Stopped by you; artifacts retained")
	if e = s.saveAuto(a); e != nil {
		respondErr(w, e)
		return
	}
	writeJSON(w, 200, a)
}
func (s *Server) runAutoCommand(ctx context.Context, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Fixed root-owned runner; no model-controlled command or path is executed here.
	out, e := exec.CommandContext(c, "sudo", append([]string{"-n", autoRunner}, args...)...).CombinedOutput()
	if e != nil {
		return nil, fmt.Errorf("runner %s: %s", args[0], clipEnd(string(out), 600))
	}
	return out, nil
}
func (s *Server) stopAutoJobs(ctx context.Context, a *autoRecord, reason string) {
	for _, j := range a.Jobs {
		if j.Status == "running" || j.Status == "starting" {
			if _, e := s.runAutoCommand(ctx, "stop", "--job", j.ID); e != nil {
				a.Reason = "Stop needs attention: " + e.Error()
				a.Status = "error"
				return
			}
			s.closeAutoBridge(j.ID)
			j.Status = "stopped"
			if e := s.snapshotAutoJob(ctx, j); e != nil {
				j.Summary = "Artifact backup pending: " + e.Error()
			}
			_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": "backlog"})
		}
	}
	if a.State != nil {
		a.State.Pause(reason)
	}
	a.Reason = reason
	a.Status = "paused"
	if !a.Config.Enabled {
		a.Status = "off"
	}
}

// RunAutonomyTick is called by the scheduler. It owns only its persisted job
// UUIDs, never interactive sessions or arbitrary tmux/process IDs.
func (s *Server) RunAutonomyTick(ctx context.Context) {
	if !s.autoMu.TryLock() {
		return
	}
	defer s.autoMu.Unlock()
	if time.Since(s.autoChecked) < 20*time.Second {
		return
	}
	s.autoChecked = time.Now()
	a, e := s.loadAuto()
	if e != nil {
		s.Log.Error("autonomy state", "err", e)
		return
	}
	if !a.Config.Enabled { // Retry failed stops even while disabled.
		for _, j := range a.Jobs {
			if j.Status == "running" || j.Status == "starting" {
				s.stopAutoJobs(ctx, a, "Autonomous mode is off")
				_ = s.saveAuto(a)
				break
			}
		}
		return
	}
	defer func() {
		if e := s.saveAuto(a); e != nil {
			s.stopAutoJobs(ctx, a, "State persistence failed")
			s.Log.Error("autonomy persistence", "err", e)
		}
	}()
	now := time.Now()
	if a.State != nil && a.State.Phase == autonomy.Complete {
		s.rememberAuto(ctx, a)
	}
	if autoCycleDue(a, now) {
		if e = s.archiveAutoCycle(a); e != nil {
			a.Status = "paused"
			a.Reason = "Cycle archive unavailable: " + e.Error()
			return
		}
		autoNewCycle(a, now)
	}
	if a.State == nil || a.State.Phase == autonomy.Complete {
		return
	}
	usageURL := "http://127.0.0.1:9105/api/agent-usage"
	// A paused cycle does not need aggressive provider polling. Its existing
	// cache/backoff still refreshes, and the quota gate rejects stale samples.
	if a.State.Phase != autonomy.Paused {
		usageURL += "?autonomous=1"
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", usageURL, nil)
	client := &http.Client{Timeout: 25 * time.Second}
	resp, e := client.Do(req)
	if e == nil {
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			e = fmt.Errorf("usage returned %d", resp.StatusCode)
		} else {
			e = json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&a.Quota)
		}
	}
	requiredProvider := ""
	if e == nil {
		now = time.Now()
		requiredProvider, e = s.autoQuotaProvider(a, now)
	}
	if e != nil {
		s.stopAutoJobs(ctx, a, "Budget pause: "+e.Error())
		return
	}
	if a.State.Phase == autonomy.Paused {
		if s.recoverAutoReport(ctx, a, now) {
			return
		}
		if strings.HasPrefix(a.State.Reason, "Budget pause:") || strings.Contains(a.State.Reason, "by you") || a.State.Reason == "Autonomous mode is off" {
			if e = a.State.Resume(a.Config, a.Quota.Providers, []string{requiredProvider}, now); e != nil {
				return
			}
		} else {
			if !autoRetryReady(a, now) {
				return
			}
			if e = a.State.Resume(a.Config, a.Quota.Providers, []string{requiredProvider}, now); e != nil {
				return
			}
			for _, id := range a.State.ActiveTaskIDs() {
				if j := autoFindJob(a, id); j != nil && j.Status == "failed" {
					j.Status = "stopped"
				}
			}
		}
	}
	a.Status = string(a.State.Phase)
	a.Reason = ""
	for _, id := range a.State.ActiveTaskIDs() {
		j := autoFindJob(a, id)
		if j == nil {
			a.State.Pause("Missing job receipt; manual inspection required")
			return
		}
		if j.ReportRepairs > 0 && j.ReportError != "" {
			a.Status = "repairing_report"
			a.Reason = "Automatically correcting the report using retained work"
		}
		if j.Status == "stopped" {
			if e = s.resumeAutoJob(ctx, a, j); e != nil {
				a.State.Pause(e.Error())
				a.Reason = e.Error()
			}
			return
		}
		if j.Status == "prepared" || j.Status == "starting" {
			if e = s.launchAutoJob(ctx, a, j); e != nil {
				a.State.Pause(e.Error())
				a.Reason = e.Error()
			}
			return
		}
		if err := s.ensureAutoBridges(j); err != nil {
			s.stopAutoJobs(ctx, a, "Bridge unavailable")
			return
		}
		_ = os.Chtimes(filepath.Join(autoRoot, j.ID, "heartbeat"), time.Now(), time.Now())
		raw, err := s.runAutoCommand(ctx, "status", "--job", j.ID)
		if err != nil {
			s.stopAutoJobs(ctx, a, "Runner status unavailable: "+err.Error())
			return
		}
		var st struct {
			State    string `json:"state"`
			ExitCode *int   `json:"exit_code"`
		}
		if json.Unmarshal(raw, &st) != nil {
			s.stopAutoJobs(ctx, a, "Invalid runner status")
			return
		}
		if st.State == "running" {
			return
		}
		if st.State != "done" || st.ExitCode == nil || *st.ExitCode != 0 {
			j.Status = "failed"
			_ = s.snapshotAutoJob(ctx, j)
			a.State.Pause("Worker failed; artifacts retained for inspection")
			a.Reason = a.State.Reason
			_ = s.DB.Update("tasks", id, map[string]any{"status": "failed"})
			return
		}
		if e = s.finishAutoJob(ctx, a, j); e != nil {
			j.Status = "failed"
			_ = s.snapshotAutoJob(ctx, j)
			var reportErr *autoReportError
			if errors.As(e, &reportErr) && autoReportRepairable(reportErr.Error()) {
				j.ReportError = reportErr.Error()
			}
			a.State.Pause("Invalid worker report: " + e.Error())
			a.Reason = a.State.Reason
			return
		}
		return
	}
	roles := a.State.NeededRoles()
	if len(roles) == 0 {
		return
	}
	if e = s.prepareAutoJob(ctx, a, roles[0]); e != nil {
		a.State.Pause(e.Error())
		a.Reason = e.Error()
		a.Status = "paused"
	}
}
func autoFindJob(a *autoRecord, id int64) *autoJob {
	for i := len(a.Jobs) - 1; i >= 0; i-- {
		if a.Jobs[i].TaskID == id {
			return a.Jobs[i]
		}
	}
	return nil
}
func (s *Server) launchAutoJob(ctx context.Context, a *autoRecord, j *autoJob) error {
	if j.Status == "prepared" { // Also permit a safe provider change before any process exists.
		provider, model, err := s.autoRoute(a, j.Role, time.Now())
		if err != nil {
			return err
		}
		j.Provider = provider
		j.Model = model
	}
	// Persist intent before side effects. start is idempotent for this UUID.
	if e := s.ensureAutoBridges(j); e != nil {
		return e
	}
	j.Status = "starting"
	if e := s.saveAuto(a); e != nil {
		return e
	}

	model := j.Model
	if _, e := s.runAutoCommand(ctx, "start", "--job", j.ID, "--provider", j.Provider, "--model", model, "--prompt", filepath.Join(autoRoot, j.ID, "prompt.txt")); e != nil {
		return e
	}
	j.Status = "running"
	_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": "running", "agent": j.Provider, "model": j.Model})
	return nil
}
func (s *Server) finishAutoJob(ctx context.Context, a *autoRecord, j *autoJob) error {
	raw, e := autoReadRegular(filepath.Join(autoRoot, j.ID, "output.jsonl"), 32<<20)
	if e != nil {
		return e
	}
	if len(raw) > 32<<20 {
		return errors.New("worker log exceeds 32 MiB")
	}
	events, _ := agents.ParseStreamLines(j.Provider, string(raw)+"\n")
	report := ""
	for _, ev := range events {
		if ev.Type == "text" {
			if t, ok := ev.Payload["text"].(string); ok {
				report = t
			}
		}
	}
	// result.json is an explicitly requested strict report artifact, read without
	// following symlinks. Never run shell text, check commands or paths from it.
	if r, err := s.runAutoCommand(ctx, "report", "--job", j.ID); err == nil {
		report = string(r)
	}
	var next autonomy.State
	_ = json.Unmarshal([]byte(store.J(a.State)), &next)
	if e = next.ApplyReport(a.Config, j.TaskID, []byte(report)); e != nil {
		return &autoReportError{e}
	}
	if e = s.snapshotAutoJob(ctx, j); e != nil {
		return e
	}
	s.closeAutoBridge(j.ID)
	j.Status = "done"
	j.Summary = clipEnd(report, 3000)
	ended := store.Now()
	rc := 0
	att, e := s.DB.LatestAttempt(j.TaskID)
	if e != nil {
		att, e = s.DB.InsertAttempt(&store.Attempt{TaskID: j.TaskID, N: 1, Status: "done", Driver: "autonomy-isolated", Agent: j.Provider, WorktreePath: j.ArtifactPath, FinishedAt: &ended, ExitCode: &rc, ResultJSON: store.J(map[string]any{"summary": report, "isolated": true})})
	}
	if e != nil {
		return e
	}
	seq, _ := s.DB.MaxEventSeq(att.ID)
	if seq == 0 {
		for i, ev := range events {
			_ = s.DB.InsertEvent(att.ID, int64(i+1), ev.Type, store.J(ev.Payload))
		}
		_ = s.DB.InsertEvent(att.ID, int64(len(events)+1), "text", store.J(map[string]any{"text": report}))
	}
	if j.Role == "reviewer" {
		var verdict autonomy.Verdict
		if json.Unmarshal([]byte(report), &verdict) == nil && verdict.Approve != nil && *verdict.Approve {
			for i := len(a.State.Assignments) - 1; i >= 0; i-- {
				as := a.State.Assignments[i]
				if as.Role == "builder" && as.Item == a.State.Item && as.Step == a.State.Step && as.Completed {
					if builder := autoFindJob(a, as.TaskID); builder != nil {
						builder.Approved = true
						builder.ReviewTaskID = j.TaskID
					}
					break
				}
			}
		}
	}
	a.State = &next
	if j.Role == "planner" {
		loc, _ := time.LoadLocation(a.Config.Timezone)
		local := time.Now().In(loc)
		if local.Hour() >= a.Config.MorningHour {
			a.StrategyDay = local.Format("2006-01-02")
		}
	}
	status := "done"
	if j.Role == "builder" {
		status = "review"
	}
	_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": status})
	s.Bus.Publish("board", "autonomy", map[string]any{"task_id": j.TaskID})
	return nil
}
func (s *Server) rememberAuto(ctx context.Context, a *autoRecord) {
	key := fmt.Sprintf("%s-%d", a.State.Date, a.State.Cycle)
	if a.RememberedCycle == key || s.Memory == nil {
		return
	}
	// Only controller-authored identifiers/status enter shared memory. Agent
	// reports remain in isolated artifacts, avoiding accidental secret writes.
	text := fmt.Sprintf("Autonomous experiment %s cycle %d: phase=%s; %d proposals, %d role tasks. Reports and artifacts are on Lectern /autonomy.html. No production deployments or publishing.", a.State.Date, a.State.Cycle, a.State.Phase, len(a.State.Items), len(a.State.Assignments))
	if e := s.Memory.Remember(ctx, memory.Entry{Project: autoOwner, Topic: autoOwner, Session: "autonomy-" + key, Agent: "lectern", Category: "checkpoint", Text: text}); e == nil {
		a.RememberedDay = a.State.Date
		a.RememberedCycle = key
	} else {
		a.Reason = "Grimoire handoff pending: " + e.Error()
	}
}
func autoUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (s *Server) ScheduleAutonomyTick(ctx context.Context) {
	s.autoWG.Add(1)
	go func() { defer s.autoWG.Done(); s.RunAutonomyTick(ctx) }()
}
