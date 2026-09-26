package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const autoIntegrationsKey = "autonomy_private_integrations_v1"

type autoIntegrationInput struct {
	TaskID          int64    `json:"task_id"`
	JobID           string   `json:"job_id"`
	ProjectID       int64    `json:"project_id"`
	Revision        string   `json:"revision"`
	ReportSHA256    string   `json:"report_sha256"`
	ScopeType       string   `json:"scope_type"`
	IntegratedScope string   `json:"integrated_scope"`
	RemainingScope  string   `json:"remaining_scope"`
	Evidence        []string `json:"evidence"`
}

type autoIntegration struct {
	autoIntegrationInput
	ID              string `json:"id"`
	RecordedAt      string `json:"recorded_at"`
	Actor           string `json:"actor"`
	ActorLogin      string `json:"actor_login,omitempty"`
	CanonicalHead   string `json:"canonical_head_at_recording"`
	Verification    string `json:"verification_method"`
	CurrentPresence string `json:"current_presence"`
}

func (s *Server) autoIntegrations() ([]autoIntegration, error) {
	rows := []autoIntegration{}
	if raw := s.DB.Setting(autoIntegrationsKey); raw != "" {
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			return nil, errors.New("integration ledger unreadable")
		}
	}
	return rows, nil
}

func (s *Server) getAutoIntegrations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.autoIntegrations()
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, rows)
}

// A receipt is a scoped attestation of private integration, not artifact approval,
// publication consent, or proof that a later revert has not removed the change.
func (s *Server) validateAutoIntegration(ctx context.Context, a *autoRecord, in autoIntegrationInput, report []byte) (autoIntegration, error) {
	bad := func(msg string) (autoIntegration, error) { return autoIntegration{}, errors.New(msg) }
	j := autoIntegrationJob(a, in)
	if j == nil {
		return bad("integration must identify a completed builder job and task")
	}
	task, err := s.DB.Task(in.TaskID)
	if err != nil || task.ProjectID != in.ProjectID {
		return bad("integration project does not match the source task")
	}
	sum := sha256.Sum256(report)
	if in.ReportSHA256 != hex.EncodeToString(sum[:]) {
		return bad("source report identity mismatch")
	}
	if in.ScopeType != "adapted" && in.ScopeType != "partial" {
		return bad("scope_type must be adapted or partial; byte equivalence is not inferred")
	}
	if strings.TrimSpace(in.IntegratedScope) == "" || len(in.IntegratedScope) > 4000 || len(in.RemainingScope) > 4000 || (in.ScopeType == "partial" && strings.TrimSpace(in.RemainingScope) == "") {
		return bad("supply concrete integrated scope and remaining scope for partial work")
	}
	if len(in.Evidence) == 0 || len(in.Evidence) > 12 {
		return bad("supply 1 to 12 private validation evidence references")
	}
	for _, e := range in.Evidence {
		if strings.TrimSpace(e) == "" || len(e) > 1000 {
			return bad("invalid validation evidence reference")
		}
	}
	if !autoSourceHash(in.Revision) {
		return bad("revision must be a full commit hash")
	}
	p, err := s.autoSourceProject(in.ProjectID)
	if err != nil {
		return bad(err.Error())
	}
	revision, err := autoSourceRevision(ctx, p.RepoPath, in.Revision)
	if err != nil {
		return bad(err.Error())
	}
	head, err := autoSourceRevision(ctx, p.RepoPath, "HEAD")
	if err != nil {
		return bad(err.Error())
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if autoSourceCommand(c, p.RepoPath, "merge-base", "--is-ancestor", revision, head).Run() != nil {
		return bad("integration commit is not reachable from canonical HEAD")
	}
	raw, _ := json.Marshal(in)
	identity := sha256.Sum256(raw)
	return autoIntegration{autoIntegrationInput: in, ID: hex.EncodeToString(identity[:]), CanonicalHead: head, Verification: "trusted local integrator attestation; source report hash and canonical commit reachability verified; evidence references are not independently executed", CurrentPresence: "unknown: receipt records historical integration, not absence of later reverts"}, nil
}

func autoIntegrationJob(a *autoRecord, in autoIntegrationInput) *autoJob {
	for _, j := range a.Jobs {
		if j.ID == in.JobID && j.TaskID == in.TaskID && j.Role == "builder" && j.Status == "done" {
			return j
		}
	}
	return nil
}

func (s *Server) postAutoIntegration(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.FromContext(r.Context())
	if !ok || (principal.Kind != auth.KindLocal && (s.Auth == nil || !s.Auth.CanDecide(principal))) {
		httpError(w, 403, "integration receipts require a trusted local integrator or authorized human")
		return
	}
	var in autoIntegrationInput
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 400, "invalid integration receipt")
		return
	}
	a, err := s.loadAuto()
	if err != nil {
		respondErr(w, err)
		return
	}
	j := autoIntegrationJob(a, in)
	if j == nil {
		httpError(w, 400, "completed builder job not found")
		return
	}
	report, err := s.runAutoCommand(r.Context(), "report", "--job", j.ID)
	if err != nil {
		httpError(w, 409, "original source report unavailable")
		return
	}
	receipt, err := s.validateAutoIntegration(r.Context(), a, in, report)
	if err != nil {
		httpError(w, 400, "%s", err)
		return
	}
	receipt.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	receipt.Actor = principal.Kind
	receipt.ActorLogin = principal.Login
	row, created, err := s.recordAutoIntegration(receipt)
	if err != nil {
		respondErr(w, err)
		return
	}
	code := 200
	if created {
		code = 201
	}
	writeJSON(w, code, row)
}

func (s *Server) recordAutoIntegration(receipt autoIntegration) (autoIntegration, bool, error) {
	// Serialize only the append; slow Git/report I/O never blocks the controller.
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	rows, err := s.autoIntegrations()
	if err != nil {
		return autoIntegration{}, false, err
	}
	for _, row := range rows {
		if row.ID == receipt.ID {
			return row, false, nil
		}
	}
	rows = append(rows, receipt)
	if err = s.DB.SetSetting(autoIntegrationsKey, store.J(rows)); err != nil {
		return autoIntegration{}, false, err
	}
	return receipt, true, nil
}

func autoIntegrationsForTask(rows []autoIntegration, taskID int64) []autoIntegration {
	selected := []autoIntegration{}
	for _, r := range rows {
		if r.TaskID == taskID {
			selected = append(selected, r)
		}
	}
	return selected
}
