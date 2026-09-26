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
	if len(a.DeferredRuns) >= 16 {
		return false
	}
	previous := a.State
	a.DeferredRuns = append(a.DeferredRuns, previous)
	j.Status = "deferred"
	j.Summary = "Not started: prerequisite unavailable: " + j.Recovery.Reason
	a.State = nil
	autoNewCycle(a, now)
	a.State.Backlog = append([]autonomy.Proposal(nil), previous.Backlog...)
	return true
}

func autoResumeRecovered(a *autoRecord) bool {
	for i, state := range a.DeferredRuns {
		ids := state.ActiveTaskIDs()
		if len(ids) != 1 {
			continue
		}
		job := autoFindJob(a, ids[0])
		if job == nil || job.Status != "deferred" || job.Recovery == nil || job.Recovery.State != "verified" {
			continue
		}
		if a.State != nil {
			a.Runs = append(a.Runs, a.State)
		}
		a.State = state
		a.DeferredRuns = append(a.DeferredRuns[:i], a.DeferredRuns[i+1:]...)
		job.Status = "prepared"
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
	// Give an active download its heartbeat before probing cold blockers.
	var running int64
	for _, state := range a.DeferredRuns {
		for _, id := range state.ActiveTaskIDs() {
			if j := autoFindJob(a, id); j != nil && j.Recovery != nil && j.Recovery.State == "recovering" {
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
			if j == nil || j.Status != "deferred" || j.Recovery == nil || j.Recovery.State == "verified" || now.Before(j.RecoveryCheckAt) {
				continue
			}
			j.RecoveryCheckAt = now.Add(5 * time.Minute)
			if err := s.ensureAutoBridges(j); err != nil {
				j.Recovery.Reason = err.Error()
				return
			}
			raw, err := s.runAutoCommand(ctx, "dependencies", "--job", j.ID)
			if err != nil {
				j.Recovery.Reason = err.Error()
				return
			}
			var receipt autoRecoveryReceipt
			if json.Unmarshal(raw, &receipt) != nil {
				j.Recovery.Reason = "Invalid prerequisite receipt"
				return
			}
			if _, err = autoRecoveryReady(receipt); err != nil {
				j.Recovery.Reason = err.Error()
				return
			}
			j.Recovery = &receipt
			if receipt.State == "recovering" {
				j.RecoveryCheckAt = now.Add(20 * time.Second)
			} else {
				s.closeAutoBridge(j.ID)
			}
			return
		}
	}
}
