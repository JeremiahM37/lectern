package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This bridge is mounted only into the fixed dependency provisioner. Normal
// workers retain their existing network policy. Never forward request headers,
// credentials, methods, bodies or user-selected hosts to a registry.
func autoDependencyURL(path string) (*url.URL, error) {
	if len(path) > 2048 || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00\r\n?#%") || strings.Contains(path, "..") || strings.HasPrefix(path, "//") {
		return nil, fmt.Errorf("invalid dependency path")
	}
	if strings.HasPrefix(path, "/sumdb/") {
		rest := strings.TrimPrefix(path, "/sumdb/sum.golang.org/")
		if rest == path || !(rest == "supported" || rest == "latest" || strings.HasPrefix(rest, "lookup/") || strings.HasPrefix(rest, "tile/")) {
			return nil, fmt.Errorf("unsupported checksum path")
		}
		return &url.URL{Scheme: "https", Host: "sum.golang.org", Path: "/" + rest}, nil
	} else if !strings.Contains(path, "/@v/") && !strings.HasSuffix(path, "/@latest") {
		return nil, fmt.Errorf("module protocol path required")
	}
	return &url.URL{Scheme: "https", Host: "proxy.golang.org", Path: path}, nil
}

func autoDependencyRedirect(req *http.Request, via []*http.Request) error {
	// The module proxy redirects zip downloads to Google's object storage. Pin
	// every destination through autoPublicDial; never reflect the signed URL.
	if len(via) > 2 || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Port() != "" || req.URL.Host != "storage.googleapis.com" {
		return fmt.Errorf("dependency redirect refused")
	}
	return nil
}

func autoDependencyFetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" {
		http.Error(w, "read-only dependency protocol required", 403)
		return
	}
	if r.URL.Path == "/sumdb/sum.golang.org/supported" {
		w.WriteHeader(200)
		return
	}
	u, err := autoDependencyURL(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	tr := &http.Transport{DialContext: autoPublicDial, ResponseHeaderTimeout: 20 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: autoDependencyRedirect}
	res, err := client.Do(req)
	if err != nil {
		http.Error(w, "dependency source unavailable", 502)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		http.Error(w, "dependency unavailable", res.StatusCode)
		return
	}
	const limit = 256 << 20
	if res.ContentLength > limit {
		http.Error(w, "dependency exceeds size limit", 413)
		return
	}
	// Abort truncated or oversized streams instead of turning transport failure
	// into a successful chunked response. Go additionally verifies its hash.
	w.Header().Set("Content-Type", "application/octet-stream")
	if res.ContentLength >= 0 {
		w.Header().Set("Content-Length", fmt.Sprint(res.ContentLength))
	}
	_, err = io.Copy(w, io.LimitReader(res.Body, limit))
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	var extra [1]byte
	if n, readErr := res.Body.Read(extra[:]); n > 0 || (readErr != nil && readErr != io.EOF) {
		panic(http.ErrAbortHandler)
	}
}

type autoRecoveryReceipt struct {
	State       string  `json:"state"`
	Capability  string  `json:"capability"`
	Key         string  `json:"key,omitempty"`
	Requirement string  `json:"requirement,omitempty"`
	Reason      string  `json:"reason,omitempty"`
	Attempts    int     `json:"attempts,omitempty"`
	VerifiedAt  float64 `json:"verified_at,omitempty"`
}

func autoRecoveryReady(r autoRecoveryReceipt) (bool, error) {
	if r.Capability != "go_modules" {
		return false, fmt.Errorf("invalid recovery capability")
	}
	if r.State == "not_applicable" {
		return true, nil
	}
	if len(r.Key) != 64 {
		return false, fmt.Errorf("invalid recovery input identity")
	}
	if _, err := hex.DecodeString(r.Key); err != nil {
		return false, fmt.Errorf("invalid recovery input identity")
	}
	switch r.State {
	case "verified":
		return true, nil
	case "recovering", "waiting", "failed":
		return false, nil
	// The controller records a deferred cycle instead of starting model work
	// against a prerequisite that has not changed.
	case "unavailable":
		return false, nil
	default:
		return false, fmt.Errorf("invalid recovery state")
	}
}

func (s *Server) recoverAutoPrerequisites(ctx context.Context, a *autoRecord, j *autoJob) (bool, error) {
	if j.Status != "prepared" || (j.Role != "builder" && j.Role != "reviewer" && !strings.HasPrefix(j.Role, "decision_")) {
		return true, nil
	}
	// Persist recovery intent before a service can be started. OFF and quota
	// cancellation must still find it if the controller dies before the reply.
	if j.Recovery == nil {
		j.Recovery = &autoRecoveryReceipt{State: "checking", Capability: "go_modules"}
		if err := s.saveAuto(a); err != nil {
			return false, err
		}
	}
	raw, err := s.runAutoCommand(ctx, "dependencies", "--job", j.ID)
	if err != nil {
		return false, err
	}
	var receipt autoRecoveryReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return false, fmt.Errorf("invalid prerequisite recovery receipt")
	}
	ready, err := autoRecoveryReady(receipt)
	if err != nil {
		return false, err
	}
	j.Recovery = &receipt
	if receipt.State == "unavailable" && autoDeferPrerequisite(a, j, time.Now()) {
		s.closeAutoBridge(j.ID)
	} else if !ready {
		a.Status = "recovering_prerequisite"
		a.Reason = "Preparing verified offline dependencies before starting the worker"
	}
	if err = s.saveAuto(a); err != nil {
		return false, err
	}
	return ready, nil
}

// Preserve the entire admitted state, including audits, repair reservations and
// a completed builder awaiting review. Other work can proceed without turning
// an unavailable prerequisite into either approval or a permanent dead end.
func autoDeferPrerequisite(a *autoRecord, j *autoJob, now time.Time) bool {
	j.Summary = "Not started: prerequisite unavailable: " + j.Recovery.Reason
	autoDeferRequirements(a, j, now)
	return true
}

func autoResumeRecovered(a *autoRecord) bool {
	// Held overflow is durable history, not an active polling slot. Resume only
	// independently released/verified work; never discard it in rotating Runs.
	for i, state := range a.HeldRuns {
		ids := state.ActiveTaskIDs()
		if len(ids) != 1 {
			continue
		}
		j := autoFindJob(a, ids[0])
		if j != nil && j.Status == "deferred" && autoDeferredReady(j) {
			if a.State != nil {
				a.Runs = append(a.Runs, a.State)
			}
			a.State = state
			a.HeldRuns = append(a.HeldRuns[:i], a.HeldRuns[i+1:]...)
			j.Status = "prepared"
			if j.PendingPythonRequest != nil || j.PendingNodeRequest != nil || j.PythonTestNeedsResume || j.NodeNeedsResume {
				j.Status = "stopped"
			}
			a.NextCycleScheduled = false
			a.NextCycleAt = time.Time{}
			a.RetryAt = time.Time{}
			a.Status = string(state.Phase)
			a.Reason = "Retained overflow prerequisite is ready for same-assignment recovery"
			return true
		}
	}
	for i, state := range a.DeferredRuns {
		ids := state.ActiveTaskIDs()
		if len(ids) != 1 {
			continue
		}
		job := autoFindJob(a, ids[0])
		if job == nil || job.Status != "deferred" || !autoDeferredReady(job) {
			continue
		}
		if a.State != nil {
			a.Runs = append(a.Runs, a.State)
		}
		a.State = state
		a.DeferredRuns = append(a.DeferredRuns[:i], a.DeferredRuns[i+1:]...)
		job.Status = "prepared"
		if job.PendingPythonRequest != nil || job.PendingNodeRequest != nil || job.PythonTestNeedsResume || job.NodeNeedsResume {
			job.Status = "stopped"
		}
		a.NextCycleScheduled = false
		a.NextCycleAt = time.Time{}
		a.RetryAt = time.Time{}
		a.Status = string(state.Phase)
		a.Reason = "Prerequisite verified; resuming retained assignment"
		return true
	}
	return false
}

// Poll retained blockers without spending model turns. This runs only after
// enabled/quota checks. Active recovery is refreshed each controller tick;
// failed/cooling-down capabilities are probed at most every five minutes.
func (s *Server) pollDeferredPrerequisites(ctx context.Context, a *autoRecord, now time.Time) {
	autoPromoteHeldPrerequisites(a)
	// Give an active download its heartbeat before probing cold blockers.
	var running int64
	for _, state := range a.DeferredRuns {
		for _, id := range state.ActiveTaskIDs() {
			if j := autoFindJob(a, id); j != nil && ((j.Recovery != nil && j.Recovery.State == "recovering") || (j.PythonRecovery != nil && j.PythonRecovery.State == "recovering") || (j.NodeRecovery != nil && j.NodeRecovery.State == "recovering")) {
				running = id
			}
		}
	}
	for _, state := range a.DeferredRuns {
		for _, id := range state.ActiveTaskIDs() {
			if running != 0 && running != id {
				continue
			}
			j := autoFindJob(a, id)
			if j != nil && !j.RequirementHold && j.Status == "deferred" && !autoPrivateToolingReady(j) && !now.Before(j.RecoveryCheckAt) {
				j.RecoveryCheckAt = now.Add(5 * time.Minute)
				if _, err := s.recoverAutoPrivateTooling(ctx, a, j); err != nil {
					a.Reason = "Selected test tooling pending: " + err.Error()
				}
				autoRotateColdPrerequisite(a, state, j)
				return
			}
			if j != nil && !j.RequirementHold && j.Status == "deferred" && j.PythonRequest != nil && (j.PythonRecovery == nil || j.PythonRecovery.State != "verified") && !now.Before(j.RecoveryCheckAt) {
				j.RecoveryCheckAt = now.Add(5 * time.Minute)
				_, err := s.recoverAutoPython(ctx, a, j)
				if err != nil {
					a.Reason = "Python prerequisite recovery: " + err.Error()
				}
				if j.PythonRecovery != nil && j.PythonRecovery.State == "recovering" {
					j.RecoveryCheckAt = now.Add(20 * time.Second)
				} else {
					autoRotateColdPrerequisite(a, state, j)
				}
				return
			}
			if j != nil && !j.RequirementHold && j.Status == "deferred" && j.NodeRequest != nil && (j.NodeRecovery == nil || j.NodeRecovery.State != "verified") && !now.Before(j.RecoveryCheckAt) {
				j.RecoveryCheckAt = now.Add(5 * time.Minute)
				if _, err := s.recoverAutoNode(ctx, a, j); err != nil {
					a.Reason = "Node prerequisite recovery: " + err.Error()
				}
				if j.NodeRecovery != nil && j.NodeRecovery.State == "recovering" {
					j.RecoveryCheckAt = now.Add(20 * time.Second)
				} else {
					autoRotateColdPrerequisite(a, state, j)
				}
				return
			}
			if j == nil || j.Status != "deferred" || j.Recovery == nil || j.Recovery.State == "verified" || now.Before(j.RecoveryCheckAt) {
				continue
			}
			j.RecoveryCheckAt = now.Add(5 * time.Minute)
			if err := s.ensureAutoBridges(j); err != nil {
				j.Recovery.Reason = err.Error()
				if j.Recovery.State != "recovering" {
					autoRotateColdPrerequisite(a, state, j)
				}
				return
			}
			raw, err := s.runAutoCommand(ctx, "dependencies", "--job", j.ID)
			if err != nil {
				j.Recovery.Reason = err.Error()
				if j.Recovery.State != "recovering" {
					autoRotateColdPrerequisite(a, state, j)
				}
				return
			}
			var receipt autoRecoveryReceipt
			if json.Unmarshal(raw, &receipt) != nil {
				j.Recovery.Reason = "Invalid prerequisite receipt"
				if j.Recovery.State != "recovering" {
					autoRotateColdPrerequisite(a, state, j)
				}
				return
			}
			if _, err = autoRecoveryReady(receipt); err != nil {
				j.Recovery.Reason = err.Error()
				if j.Recovery.State != "recovering" {
					autoRotateColdPrerequisite(a, state, j)
				}
				return
			}
			j.Recovery = &receipt
			if receipt.State == "recovering" {
				j.RecoveryCheckAt = now.Add(20 * time.Second)
			} else {
				s.closeAutoBridge(j.ID)
				autoRotateColdPrerequisite(a, state, j)
			}
			return
		}
	}
}

func autoDeferredReady(j *autoJob) bool {
	if !autoPrivateToolingReady(j) {
		return false
	}
	if j.RequirementHold {
		return false
	}
	if j.PendingPythonRequest != nil || j.PendingNodeRequest != nil {
		return true
	}
	if len(j.RequirementIDs) > 0 && j.PythonRequest == nil && j.NodeRequest == nil {
		return false
	}
	if j.PythonRequest != nil && (j.PythonRecovery == nil || j.PythonRecovery.State != "verified" || j.PythonNeedsChange) {
		return false
	}
	if j.NodeRequest != nil && (j.NodeRecovery == nil || j.NodeRecovery.State != "verified" || j.NodeNeedsChange) {
		return false
	}
	return j.Recovery != nil && (j.Recovery.State == "verified" || j.Recovery.State == "not_applicable")
}

func autoPromoteHeldPrerequisites(a *autoRecord) {
	// Unsupported needs do not consume an active polling slot. Diagnosis works
	// against the retained assignment wherever it is held.
	for i := 0; i < len(a.DeferredRuns); {
		state := a.DeferredRuns[i]
		ids := state.ActiveTaskIDs()
		if len(ids) == 1 {
			j := autoFindJob(a, ids[0])
			if j != nil && j.RequirementHold {
				a.HeldRuns = append(a.HeldRuns, state)
				a.DeferredRuns = append(a.DeferredRuns[:i], a.DeferredRuns[i+1:]...)
				continue
			}
		}
		i++
	}
	for i := 0; i < len(a.HeldRuns) && len(a.DeferredRuns) < 16; {
		state := a.HeldRuns[i]
		ids := state.ActiveTaskIDs()
		if len(ids) == 1 {
			j := autoFindJob(a, ids[0])
			if j != nil && j.Status == "deferred" && !j.RequirementHold {
				a.DeferredRuns = append(a.DeferredRuns, state)
				a.HeldRuns = append(a.HeldRuns[:i], a.HeldRuns[i+1:]...)
				continue
			}
		}
		i++
	}
}

func autoRotateColdPrerequisite(a *autoRecord, state *autonomy.State, j *autoJob) {
	if autoDeferredReady(j) {
		return
	}
	waiting := false
	for _, held := range a.HeldRuns {
		ids := held.ActiveTaskIDs()
		if len(ids) == 1 {
			other := autoFindJob(a, ids[0])
			if other != nil && !other.RequirementHold && other.Status == "deferred" {
				waiting = true
				break
			}
		}
	}
	if !waiting {
		return
	}
	for i, current := range a.DeferredRuns {
		if current == state {
			a.DeferredRuns = append(a.DeferredRuns[:i], a.DeferredRuns[i+1:]...)
			a.HeldRuns = append(a.HeldRuns, state)
			autoPromoteHeldPrerequisites(a)
			return
		}
	}
}
