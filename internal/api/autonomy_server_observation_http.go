package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"
)

type autoServerTargetCatalog struct {
	SchemaVersion int    `json:"schema_version"`
	RegistrySHA   string `json:"registry_sha256"`
	Targets       []struct {
		ID          string                        `json:"id"`
		Maintenance []autoServerMaintenancePolicy `json:"maintenance"`
	} `json:"targets"`
}

func (s *Server) autoServerTargets(ctx context.Context) ([]byte, autoServerTargetCatalog, error) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.runAutoCommand(c, "server-targets")
	var catalog autoServerTargetCatalog
	if err != nil {
		return nil, catalog, err
	}
	if len(raw) > 256<<10 || json.Unmarshal(raw, &catalog) != nil || catalog.SchemaVersion != 1 || !autoHash256(catalog.RegistrySHA) || len(catalog.Targets) > 16 {
		return nil, catalog, errors.New("invalid server registry observation")
	}
	seen := map[string]bool{}
	for _, target := range catalog.Targets {
		if !autoServerResourceID.MatchString(target.ID) || seen[target.ID] {
			return nil, catalog, errors.New("invalid registered target")
		}
		seen[target.ID] = true
	}
	return raw, catalog, nil
}

func (s *Server) autoServerObservationBridge(jobID string, w http.ResponseWriter, r *http.Request) {
	s.autoServerObservationBridgeAt(autoRoot, jobID, w, r)
}

func (s *Server) autoServerObservationBridgeAt(root, jobID string, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/server-targets" {
		if r.Method != http.MethodGet {
			http.Error(w, "read only", 405)
			return
		}
		if r.URL.RawQuery != "" {
			http.Error(w, "no target selectors accepted", 400)
			return
		}
		s.autoMu.Lock()
		a, err := s.loadAuto()
		if err == nil {
			_, err = autoServerObservationOwner(a, jobID, false)
		}
		s.autoMu.Unlock()
		if err != nil {
			http.Error(w, "unknown observation owner", 403)
			return
		}
		raw, _, err := s.autoServerTargets(r.Context())
		if err != nil {
			http.Error(w, "registered server observation unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		return
	}
	if r.Method == http.MethodGet {
		s.autoServerObservationRead(root, jobID, w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "observation input belongs in JSON", 400)
		return
	}
	var input struct {
		TargetID string `json:"target_id"`
	}
	inputRaw, readErr := io.ReadAll(io.LimitReader(r.Body, 4097))
	if readErr != nil || len(inputRaw) > 4096 {
		http.Error(w, "observation request exceeds limit", 400)
		return
	}
	d := json.NewDecoder(bytes.NewReader(inputRaw))
	open, openErr := d.Token()
	key, keyErr := d.Token()
	if openErr != nil || open != json.Delim('{') || keyErr != nil || key != "target_id" || d.Decode(&input.TargetID) != nil || !autoServerResourceID.MatchString(input.TargetID) {
		http.Error(w, "one registered target_id required", 400)
		return
	}
	close, closeErr := d.Token()
	if closeErr != nil || close != json.Delim('}') {
		http.Error(w, "exactly one target_id required", 400)
		return
	}
	if err := d.Decode(new(any)); err != io.EOF {
		http.Error(w, "unexpected observation input", 400)
		return
	}
	_, catalog, err := s.autoServerTargets(r.Context())
	if err != nil {
		http.Error(w, "server registry unavailable", 503)
		return
	}
	found := false
	for _, target := range catalog.Targets {
		if target.ID == input.TargetID {
			found = true
		}
	}
	if !found {
		http.Error(w, "target is not registered", 404)
		return
	}
	s.autoMu.Lock()
	a, err := s.loadAuto()
	var j *autoJob
	if err == nil {
		j, err = autoServerObservationOwner(a, jobID, true)
	}
	var request autoServerObservationRequest
	var raw []byte
	if err == nil {
		request, err = autoNewServerObservation(j, input.TargetID, catalog.RegistrySHA, time.Now())
	}
	if err == nil {
		raw, err = autoWriteServerObservation(root, request)
	}
	if err != nil {
		s.autoMu.Unlock()
		http.Error(w, "observation could not be reserved: "+err.Error(), 409)
		return
	}
	// Serialize the bounded launch handshake with OFF. The runner's job stop
	// cancels/tombstones all reserved observations before releasing ownership.
	defer s.autoMu.Unlock()
	s.autoServerObservationCall(w, r, request, raw, "server-observe")
}

func (s *Server) autoServerObservationRead(root, jobID string, w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["id"]) != 1 || !autoHash256(query.Get("id")) {
		http.Error(w, "one observation id required", 400)
		return
	}
	s.autoMu.Lock()
	a, err := s.loadAuto()
	var j *autoJob
	if err == nil {
		j, err = autoServerObservationOwner(a, jobID, false)
	}
	s.autoMu.Unlock()
	if err != nil {
		http.Error(w, "observation owner unavailable", 403)
		return
	}
	raw, err := autoReadRegular(filepath.Join(root, jobID, "server-observations", query.Get("id"), "request.json"), 4096)
	var request autoServerObservationRequest
	if err != nil || json.Unmarshal(raw, &request) != nil || request.RequestID != query.Get("id") || request.OwnerJob != jobID || request.OwnerTask != j.TaskID {
		http.Error(w, "observation not found for this assignment", 404)
		return
	}
	if _, err := autoServerObservationPath(root, request); err != nil {
		http.Error(w, "invalid stored request", 409)
		return
	}
	s.autoServerObservationCall(w, r, request, raw, "server-observe-status")
}

func (s *Server) autoServerObservationCall(w http.ResponseWriter, r *http.Request, request autoServerObservationRequest, raw []byte, command string) {
	c, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := s.runAutoCommand(c, command, "--job", request.OwnerJob, "--observation-id", request.RequestID)
	if err != nil {
		writeJSON(w, 202, map[string]any{"request_id": request.RequestID, "state": "pending", "reason": "Observation unavailable; poll the same id before submitting another observation"})
		return
	}
	body, status, err := autoDecodeServerObservation(result, request, raw, time.Now())
	if err != nil {
		http.Error(w, err.Error()+"; retain id "+request.RequestID, 502)
		return
	}
	writeJSON(w, status, body)
}

// Input comes only from the trusted runner, never a worker's saved JSON. The
// runner verifies its sealed receipt hash; this decoder verifies socket/request
// bindings and completeness before either a reader or the planner consumes it.
func autoDecodeServerObservation(result []byte, request autoServerObservationRequest, raw []byte, now time.Time) (map[string]any, int, error) {
	var body map[string]any
	if len(result) > 512<<10 || json.Unmarshal(result, &body) != nil || body["schema_version"] != float64(1) || body["request_id"] != request.RequestID || body["owner_job"] != request.OwnerJob || body["owner_task"] != float64(request.OwnerTask) || body["target_id"] != request.TargetID || body["registry_sha256"] != request.RegistrySHA || body["request_sha256"] != autoSHA(raw) || body["mutation_performed"] != false {
		return nil, 0, errors.New("server observation binding invalid")
	}
	status := 200
	switch body["state"] {
	case "observing", "waiting", "pending":
		status = 202
	case "observed":
		captured, _ := body["captured_at"].(string)
		completed, _ := body["completed_at"].(string)
		start, startErr := time.Parse(time.RFC3339Nano, captured)
		end, endErr := time.Parse(time.RFC3339Nano, completed)
		if startErr != nil || endErr != nil || end.Before(start) || end.After(now.Add(time.Minute)) {
			return nil, 0, errors.New("observation capture timestamps invalid")
		}
		if _, ok := body["facts"].(map[string]any); !ok {
			return nil, 0, errors.New("observation facts missing")
		}
		if _, ok := body["configuration_complete"].(bool); !ok {
			return nil, 0, errors.New("configuration completeness missing")
		}
		for _, name := range []string{"helper_sha256", "facts_sha256", "configuration_sha256", "receipt_sha256"} {
			value, _ := body[name].(string)
			if !autoHash256(value) {
				return nil, 0, errors.New("incomplete observation receipt")
			}
		}
	case "unavailable", "failed", "cancelled", "timeout", "interrupted":
	default:
		return nil, 0, errors.New("unknown server observation state")
	}
	return body, status, nil
}

const autoServerObservationPrompt = "\nLive server facts: GET /server-targets lists registered read-only targets. POST /server-observations with {\"target_id\":\"REGISTERED_ID\"}, then poll GET /server-observations?id=RETURNED_REQUEST_ID until terminal. Preserve the returned ID across timeouts; repeated status reads never restart collection. Observations are timestamped, bounded facts, not mutation authority. Individual unavailable facts are not healthy results. Backup service success is not proof of a recoverable snapshot. These endpoints grant no host shell, restart, deployment, publication or credential access.\n"
