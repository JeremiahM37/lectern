package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"path"
	"strings"
	"unicode/utf8"
)

const autoExpertInputLimit = 1 << 20

type autoExpertProbeFixture struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type autoExpertProbeInput struct {
	ProgressKey string                   `json:"progress_key"`
	Profile     string                   `json:"profile"`
	Script      string                   `json:"script"`
	Fixtures    []autoExpertProbeFixture `json:"fixtures"`
	Argv        []string                 `json:"argv"`
}

// Workers supply only executable input in an isolated environment. Source,
// owner, runtime and audit authority are supplied separately by the controller.
func autoDecodeExpertProbeInput(reader io.Reader) (*autoExpertProbeInput, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, autoExpertInputLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > autoExpertInputLimit || !utf8.Valid(raw) {
		return nil, errors.New("probe input exceeds size limit or is not UTF-8")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var input autoExpertProbeInput
	if err = dec.Decode(&input); err != nil {
		return nil, err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("expected one probe request")
	}
	if !autoHash256(input.ProgressKey) {
		return nil, errors.New("expected pinned progress key")
	}
	if input.Profile == "" {
		input.Profile = "ordinary180"
	}
	if input.Profile != "ordinary180" {
		return nil, errors.New("profile is not admitted by the current probe policy")
	}
	if strings.TrimSpace(input.Script) == "" || len(input.Script) > 128<<10 || strings.ContainsRune(input.Script, 0) || !utf8.ValidString(input.Script) {
		return nil, errors.New("expected bounded UTF-8 Python script")
	}
	if len(input.Argv) > 32 || len(input.Fixtures) > 64 {
		return nil, errors.New("too many probe arguments or fixtures")
	}
	argumentBytes := 0
	for _, arg := range input.Argv {
		argumentBytes += len(arg)
		if len(arg) > 2048 || strings.ContainsRune(arg, 0) || !utf8.ValidString(arg) {
			return nil, errors.New("invalid probe argument")
		}
	}
	if argumentBytes > 16<<10 {
		return nil, errors.New("probe arguments exceed total byte limit")
	}
	seen := map[string]bool{"main.py": true}
	total := 0
	for _, fixture := range input.Fixtures {
		name := fixture.Path
		if name == "" || len(name) > 256 || name == "." || name == ".." || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00\r\n") || !utf8.ValidString(name) {
			return nil, errors.New("fixture requires a relative normalized file path")
		}
		if seen[name] {
			return nil, errors.New("duplicate fixture path")
		}
		seen[name] = true
		data, e := base64.StdEncoding.Strict().DecodeString(fixture.Content)
		if e != nil || base64.StdEncoding.EncodeToString(data) != fixture.Content {
			return nil, errors.New("fixture content must be canonical base64")
		}
		total += len(data)
		if total > 512<<10 {
			return nil, errors.New("fixture bytes exceed probe limit")
		}
	}
	for name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return nil, errors.New("fixture file/directory collision")
			}
		}
	}
	return &input, nil
}

func autoExpertRecoveryPrompt(a *autoRecord, role string) string {
	return fmt.Sprintf("Expert recovery investigation (%s): GET /expert-recovery discovers controller-pinned exhausted checkpoints; the active planner obtains a missing pin using POST /expert-recovery with project_id and source_task_id and immutable attempt history. Use expert_recovery_task_id and expert_progress_key together, excluding other source selectors. A new key or expired cooldown is not evidence of progress. During an admitted expert plan audit, POST /expert-probes with progress_key, script (Python), optional fixtures [{path,content:base64}], argv and profile ordinary180. Identity, source and runtime come from your assigned socket, never request fields. GET /expert-probes lists your current or inherited same-assignment probe IDs (paginate with after=next_after); use this after report correction before creating more probes. Poll GET /expert-probes?id=<returned id>. Probes execute offline with read-only source and separate bounded scratch. Go probes automatically prepare an exact module/toolchain snapshot when historical delivery evidence is absent; a pending runtime response is not a probe reservation, so retry submission after preflight. This is explicitly a new experiment environment, never a claim about the old worker. Use the selected offline Go toolchain and copy source into scratch for tests that generate go.sum. Receipts prove mounted inputs and execution, not which code your script actually exercised or causal correctness. Independently inspect source/import controls, original acceptance, genuine baseline behavior and prior failed attempts. A mutant-only result, hardcoded output, cosmetic script change or unrelated dependency is insufficient. Final audit must include expert_recovery:[{source_task_id:NUMBER,progress_key:PIN_KEY,probe_ids:[YOUR_PROBE_ID],failure_family:EXACT_CATALOG_FAILURE_FAMILY,prior_attempt:EXACT_CATALOG_PRIOR_ATTEMPT,material_change:true,causal_explanation:TEXT,different_strategy:TEXT,stop_criterion:TEXT}] for each expert item. Read paginated pin details and prior attempts; these are fields within your normal approve/reason verdict, not a separate approval. Set approve false if a meaningful new strategy was not demonstrated; do not approve while receipts are missing, cancelled, timed out, output-limited or for another auditor. Both independent audits and remaining quota are required before implementation.\n", role)
}

var autoExpertPinSlots = make(chan struct{}, 4)

func autoExpertPlannerOwner(a *autoRecord, jobID string) (*autoJob, error) {
	if a.State == nil || !a.Config.Enabled || (a.State.Phase != autonomy.Plan && a.State.Phase != autonomy.Revise) {
		return nil, errors.New("pin requests require active planning")
	}
	for _, assignment := range a.State.Assignments {
		if assignment.Role != "planner" || assignment.Completed || assignment.Round != a.State.Revision {
			continue
		}
		j := autoFindJob(a, assignment.TaskID)
		if j != nil && j.ID == jobID && j.Role == "planner" && j.Status == "running" {
			return j, nil
		}
	}
	return nil, errors.New("request does not belong to current planner")
}

// Pin creation reads immutable archive identities outside the controller mutex.
// It grants investigation authority only; execution remains separately audited.
func (s *Server) autoExpertCatalogBridge(jobID string, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.URL.RawQuery != "" {
		http.Error(w, "pin request cannot include query selectors", 400)
		return
	}
	if r.Method == http.MethodGet {
		a, e := s.loadAuto()
		if e != nil {
			http.Error(w, "recovery catalog unavailable", 503)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			http.Error(w, "invalid catalog query", 400)
			return
		}
		body, status := autoExpertDiscovery(a, query)
		writeJSON(w, status, body)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var input struct {
		ProjectID    int64 `json:"project_id"`
		SourceTaskID int64 `json:"source_task_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&input); e != nil || input.ProjectID <= 0 || input.SourceTaskID <= 0 {
		http.Error(w, "expected project_id and source_task_id", 400)
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		http.Error(w, "expected one pin request", 400)
		return
	}
	select {
	case autoExpertPinSlots <- struct{}{}:
		defer func() { <-autoExpertPinSlots }()
	default:
		http.Error(w, "pin inspection busy; retry shortly", 503)
		return
	}
	s.autoMu.Lock()
	a, e := s.loadAuto()
	if e == nil {
		_, e = autoExpertPlannerOwner(a, jobID)
	}
	if e != nil {
		s.autoMu.Unlock()
		http.Error(w, "pin request is not owned by active planner", 409)
		return
	}
	cycle, revision := a.State.Cycle, a.State.Revision
	s.autoMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	pin, e := s.pinAutoExpertRecovery(ctx, a, input.ProjectID, input.SourceTaskID)
	if e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	current, e := s.loadAuto()
	if e == nil {
		_, e = autoExpertPlannerOwner(current, jobID)
	}
	if e != nil || current.State.Cycle != cycle || current.State.Revision != revision {
		http.Error(w, "planning changed during inspection", 409)
		return
	}
	ledger := autoExpertLedger(current)
	ledger.Pins[pin.Key] = pin
	if _, e = autoExpertPin(current, autonomy.Proposal{ProjectID: input.ProjectID, ExpertRecoveryTaskID: input.SourceTaskID, ExpertProgressKey: pin.Key}); e != nil {
		http.Error(w, "source state changed during inspection", 409)
		return
	}
	if e = s.saveAuto(current); e != nil {
		http.Error(w, "pin could not be persisted", 503)
		return
	}
	writeJSON(w, 200, map[string]any{"pin": pin, "authority": "Frozen investigation inputs only; independent executed evidence and dual audits still required"})
}

func autoExpertDiscovery(a *autoRecord, q url.Values) (any, int) {
	bad := func() (any, int) {
		return map[string]string{"error": "Use after for index pages, or key and optional after_attempt for one pinned history"}, 400
	}
	for k, v := range q {
		if len(v) != 1 || (k != "key" && k != "after" && k != "after_attempt") {
			return bad()
		}
	}
	key, after := q.Get("key"), q.Get("after")
	if (key != "" && !autoHash256(key)) || (after != "" && !autoHash256(after)) || (key != "" && after != "") {
		return bad()
	}
	if key != "" {
		cursor := 0
		if v, ok := q["after_attempt"]; ok {
			n, e := strconv.Atoi(v[0])
			if e != nil || n < 0 {
				return bad()
			}
			cursor = n
		}
		if a.ExpertRecovery == nil || a.ExpertRecovery.Pins[key] == nil {
			return map[string]string{"error": "pin not found"}, 404
		}
		pin := a.ExpertRecovery.Pins[key]
		history := a.ExpertRecovery.Attempts[pin.RootTaskID]
		page := []*autoExpertRecoveryAttempt{}
		next := 0
		prior := 0
		for _, attempt := range history {
			if attempt.Number > prior {
				prior = attempt.Number
			}
			if attempt.Number <= cursor {
				continue
			}
			if len(page) < 5 {
				page = append(page, attempt)
			} else if next == 0 {
				next = page[len(page)-1].Number
			}
		}
		return map[string]any{"pin": pin, "failure_family": pin.AcceptanceSHA, "prior_attempt": prior, "attempts": page, "next_after_attempt": next, "authority": "Mounted/executed evidence does not establish semantic correctness. Original acceptance and fresh independent audits remain binding."}, 200
	}
	if _, ok := q["after_attempt"]; ok {
		return bad()
	}
	keys := []string{}
	if a.ExpertRecovery != nil {
		for k := range a.ExpertRecovery.Pins {
			if k > after {
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	rows := []map[string]any{}
	next := ""
	for _, k := range keys {
		if len(rows) == 25 {
			next = rows[len(rows)-1]["expert_progress_key"].(string)
			break
		}
		pin := a.ExpertRecovery.Pins[k]
		prior := 0
		for _, v := range a.ExpertRecovery.Attempts[pin.RootTaskID] {
			if v.Number > prior {
				prior = v.Number
			}
		}
		rows = append(rows, map[string]any{"expert_progress_key": k, "expert_recovery_task_id": pin.SourceTaskID, "project_id": pin.ProjectID, "root_task_id": pin.RootTaskID, "prior_attempt": prior, "failure_family": pin.AcceptanceSHA, "details_uri": "/expert-recovery?key=" + k})
	}
	return map[string]any{"items": rows, "next_after": next, "pin_request": "POST /expert-recovery with project_id and source_task_id during current planning", "authority": "Pins permit investigation, not implementation or approval"}, 200
}

// The caller holds autoMu. Persist all cancellation intents before touching
// helpers. Each tick does bounded lightweight stop/poll work; uncertain status
// remains owned and is retried even after the auditor itself has finished.
func (s *Server) stopAutoExpertProbes(ctx context.Context, a *autoRecord) error {
	if a.ExpertRecovery == nil {
		return nil
	}
	pending := []string{}
	changed := false
	for id, lease := range a.ExpertRecovery.Probes {
		if lease.StopConfirmed {
			continue
		}
		if lease.StopRequestedAt.IsZero() {
			lease.StopRequestedAt = time.Now().UTC()
			changed = true
		}
		pending = append(pending, id)
	}
	if changed {
		if e := s.saveAuto(a); e != nil {
			return fmt.Errorf("persist probe cancellation: %w", e)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	sort.Slice(pending, func(i, j int) bool {
		left, right := a.ExpertRecovery.Probes[pending[i]], a.ExpertRecovery.Probes[pending[j]]
		if left.StopAttempts != right.StopAttempts {
			return left.StopAttempts < right.StopAttempts
		}
		leftTerminal, rightTerminal := left.Receipt != nil || left.Diagnostic != nil, right.Receipt != nil || right.Diagnostic != nil
		if leftTerminal != rightTerminal {
			return !leftTerminal
		}
		return pending[i] < pending[j]
	})
	// Bounded and fair across controller ticks: a failed stop cannot monopolize
	// the next batch, and a long history cannot indefinitely hold autoMu.
	if len(pending) > 4 {
		pending = pending[:4]
	}
	for _, id := range pending {
		a.ExpertRecovery.Probes[id].StopAttempts++
	}
	if e := s.saveAuto(a); e != nil {
		return fmt.Errorf("persist probe stop attempts: %w", e)
	}
	var failures []error
	for _, id := range pending {
		lease := a.ExpertRecovery.Probes[id]
		stopCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		raw, e := s.runAutoCommand(stopCtx, "expert-probe-stop", "--job", lease.OwnerJob, "--probe-id", id)
		cancel()
		var result struct {
			State string `json:"state"`
		}
		if e == nil {
			e = json.Unmarshal(raw, &result)
		}
		if e == nil && result.State != "stopped" {
			e = errors.New("probe stop not yet confirmed")
		}
		if e != nil {
			lease.StopError = e.Error()
			failures = append(failures, e)
		} else {
			lease.StopConfirmed = true
			lease.StopError = ""
		}
	}
	if e := s.saveAuto(a); e != nil {
		failures = append(failures, e)
	}
	return errors.Join(failures...)
}
