package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// An operation is recorded before request publication or runner invocation. Its
// immutable request survives restart; cancellation revokes its generation.
type autoPrivateOperation struct {
	IntegrationID   string          `json:"integration_id"`
	Phase           string          `json:"phase"`
	Request         json.RawMessage `json:"request"`
	RequestSHA      string          `json:"request_sha256"`
	Generation      int             `json:"generation"`
	CancelRequested bool            `json:"cancel_requested,omitempty"`
	StopConfirmed   bool            `json:"stop_confirmed,omitempty"`
	Done            bool            `json:"done,omitempty"`
	Receipt         json.RawMessage `json:"receipt,omitempty"`
}

func autoWritePrivateRequest(jobID, integrationID, phase string, raw []byte) error {
	return autoWritePrivateRequestAt(autoRoot, jobID, integrationID, phase, raw)
}
func autoWritePrivateRequestAt(root, jobID, integrationID, phase string, raw []byte) error {
	limit := 256 << 10
	if strings.HasPrefix(phase, "check-") {
		limit = 1 << 20
	}
	if !autoExpertJobID.MatchString(jobID) || !autoHash256(integrationID) || len(raw) == 0 || len(raw) > limit || !json.Valid(raw) {
		return errors.New("invalid private request identity or size")
	}
	switch phase {
	case "audit", "prepare", "seal", "review", "publish", "consume", "rollback":
	default:
		if !strings.HasPrefix(phase, "check-") || !autoHash256(strings.TrimPrefix(phase, "check-")) {
			return errors.New("invalid private request phase")
		}
	}
	// The request must name exactly the controller-selected owner and operation.
	var binding struct {
		OwnerJob      string `json:"owner_job"`
		IntegrationID string `json:"integration_id"`
		Schema        int    `json:"schema_version"`
	}
	if json.Unmarshal(raw, &binding) != nil || binding.OwnerJob != jobID || binding.IntegrationID != integrationID || binding.Schema != 1 {
		return errors.New("private request envelope mismatch")
	}
	if err := autoMkdirAll(root, filepath.Join(root, jobID)); err != nil {
		return err
	}
	dir := filepath.Join(root, jobID, "integration-requests")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe private request directory")
	}
	f, err := os.CreateTemp(dir, ".request-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	name := filepath.Join(dir, integrationID+"."+phase+".json")
	if err = os.Link(tmp, name); os.IsExist(err) {
		old, e := autoReadRegular(name, int64(limit))
		if e != nil {
			return e
		}
		if !bytes.Equal(old, raw) {
			return errors.New("immutable private request changed")
		}
		return nil
	} else if err != nil {
		return err
	}
	if err = os.Remove(tmp); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func autoPrivateFind(a *autoRecord, id string) *autoPrivateIntegrationAttempt {
	if a.PrivateIntegration != nil {
		for _, r := range a.PrivateIntegration.Runs {
			for _, v := range r.Attempts {
				if v.ID == id {
					return v
				}
			}
		}
	}
	return nil
}
func autoPrivateCurrentBuilder(a *autoRecord) *autoJob {
	for i := len(a.State.Assignments) - 1; i >= 0; i-- {
		as := a.State.Assignments[i]
		if as.Role == "builder" && as.Item == a.State.Item && as.Step == a.State.Step && as.Round == a.State.Revision {
			return autoFindJob(a, as.TaskID)
		}
	}
	return nil
}
func (s *Server) reserveAutoPrivate(ctx context.Context, a *autoRecord, role, id string, expert *autoExpertRecoveryAttempt) (string, *autoPrivateIntegrationAttempt, error) {
	if role == "reviewer" {
		if b := autoPrivateCurrentBuilder(a); b != nil && b.PrivateIntegrationAttempt > 0 {
			v, e := autoPrivateAttempt(a, b)
			if e != nil {
				return "", nil, e
			}
			if v.ReviewerJob == "" {
				v.ReviewerJob = id
			}
			return v.ReviewerJob, v, s.saveAuto(a)
		}
	}
	if role == "builder" && expert != nil && a.State.Item < len(a.State.Items) {
		p := a.State.Items[a.State.Item]
		pin, e := autoExpertPin(a, p)
		if e != nil {
			return "", nil, e
		}
		if pin.PrivateIntegration != nil {
			v, e := autoReserveExpertPrivateIntegration(a, p, expert)
			if e != nil {
				return "", nil, e
			}
			return v.JobID, v, s.saveAuto(a)
		}
	}
	if role != "builder" || a.State.Item >= len(a.State.Items) || a.State.Items[a.State.Item].IntegrationTaskID == 0 {
		return id, nil, nil
	}
	p := a.State.Items[a.State.Item]
	pin, e := autoPrivatePin(a, p)
	if e != nil {
		return "", nil, e
	}
	if r := autoPrivateLedger(a).Runs[pin.RootID]; r != nil {
		for _, v := range r.Attempts {
			if v.Cycle == a.State.Cycle && v.Revision == a.State.Revision && v.Item == a.State.Item && store.J(v.Proposal) == store.J(p) && !autoPrivateAttemptTerminal(v.Status) {
				id = v.JobID
				break
			}
		}
	}
	provider, _, e := s.autoRoute(a, role, time.Now())
	if e != nil {
		return "", nil, e
	}
	v, e := autoReservePrivateIntegration(a, p, id, []string{provider}, time.Now())
	if e != nil {
		return "", nil, e
	}
	return id, v, s.saveAuto(a)
}

func autoPrivateEnvelope(pin *autoPrivateIntegrationPin, id string, j *autoJob) map[string]any {
	return map[string]any{"schema_version": 1, "integration_id": id, "pin_key": pin.Key, "root_id": pin.RootID, "owner_job": j.ID, "owner_task": j.TaskID, "project_id": pin.ProjectID}
}
func (s *Server) autoPrivatePrepareEnvelope(pin *autoPrivateIntegrationPin, id string, j *autoJob) (map[string]any, error) {
	project, e := s.autoSourceProject(pin.ProjectID)
	if e != nil {
		return nil, e
	}
	m := autoPrivateEnvelope(pin, id, j)
	m["destination"] = map[string]any{"repo_path": project.RepoPath, "commit": pin.BaseRevision, "tree_oid": pin.BaseTree, "expected_managed_commit": pin.ExpectedManagedCommit, "base_kind": pin.BaseKind, "base_publication_id": pin.BasePublicationID, "base_publication_receipt_sha256": pin.BasePublicationReceiptSHA, "canonical_commit": pin.CanonicalRevision, "canonical_tree_oid": pin.CanonicalTree}
	m["source"] = map[string]any{"job": pin.SourceJob, "archive_kind": pin.SourceArchiveKind, "archive_sha256": pin.SourceSHA, "raw_archive_sha256": pin.RawSourceSHA, "report_sha256": pin.SourceReportSHA, "review_job": pin.ReviewJob, "review_archive_sha256": pin.ReviewSHA, "review_report_sha256": pin.ReviewReportSHA}
	m["paths"] = pin.Paths
	return m, nil
}
func autoQueuePrivate(j *autoJob, id, phase string, generation int, request map[string]any) error {
	raw, e := json.Marshal(request)
	if e != nil {
		return e
	}
	sha := autoSHA(raw)
	for _, op := range j.PrivateOperations {
		if op.IntegrationID == id && op.Phase == phase {
			if op.RequestSHA != sha {
				return errors.New("private operation request changed")
			}
			return nil
		}
	}
	j.PrivateOperations = append(j.PrivateOperations, &autoPrivateOperation{IntegrationID: id, Phase: phase, Generation: generation, Request: raw, RequestSHA: sha})
	return nil
}
func (s *Server) bindAutoPrivate(a *autoRecord, j *autoJob, v *autoPrivateIntegrationAttempt) error {
	if v != nil {
		if e := autoPrivatePriorCopies(j, &v.Authority.Pin); e != nil {
			return e
		}
		j.PrivateIntegrationRoot = v.RootID
		j.PrivateIntegrationAttempt = v.Number
		if j.Role == "builder" {
			v.TaskID = j.TaskID
		} else {
			v.ReviewerTaskID = j.TaskID
		}
		var request map[string]any
		var e error
		phase := "prepare"
		if j.Role == "reviewer" {
			if v.Candidate == nil {
				return errors.New("private reviewer requires sealed candidate")
			}
			phase = "review"
			request = autoPrivateEnvelope(&v.Authority.Pin, v.ID, j)
			request["candidate_receipt_sha256"] = v.Candidate.ReceiptSHA
			request["candidate_tree_sha256"] = v.Candidate.TreeSHA
		} else {
			request, e = s.autoPrivatePrepareEnvelope(&v.Authority.Pin, v.ID, j)
		}
		if e != nil {
			return e
		}
		return autoQueuePrivate(j, v.ID, phase, v.Generation, request)
	}
	if j.Role == "auditor_a" || j.Role == "auditor_b" {
		for _, p := range a.State.Items {
			if p.IntegrationTaskID == 0 && p.ExpertRecoveryTaskID == 0 {
				continue
			}
			pin, e := autoPrivateProposalPin(a, p)
			if e != nil {
				return e
			}
			if pin == nil {
				continue
			}
			if e = autoPrivatePriorCopies(j, pin); e != nil {
				return e
			}
			m, e := s.autoPrivatePrepareEnvelope(pin, pin.Key, j)
			if e != nil {
				return e
			}
			if e = autoQueuePrivate(j, pin.Key, "audit", 1, m); e != nil {
				return e
			}
		}
	}
	if j.Role == "builder" && a.State.Item < len(a.State.Items) {
		p := a.State.Items[a.State.Item]
		if prior := autoFindJob(a, p.ContinueTaskID); prior != nil {
			j.PrivateIntegrationRoot = prior.PrivateIntegrationRoot
		}
		if p.SourceIntegrationID != "" {
			pub, e := autoPrivateIntegrationSource(a, p.SourceIntegrationID, p.ProjectID)
			if e != nil {
				return e
			}
			v := autoPrivateFind(a, pub.ID)
			if v == nil {
				return errors.New("private source attempt missing")
			}
			j.PrivateIntegrationRoot = v.RootID
			m := autoPrivateEnvelope(&v.Authority.Pin, v.ID, j)
			m["publication_receipt_sha256"] = pub.ReceiptSHA
			m["commit"] = pub.Commit
			m["tree_oid"] = pub.GitTree
			m["tree_sha256"] = pub.CandidateSHA
			return autoQueuePrivate(j, v.ID, "consume", v.Generation, m)
		}
	}
	return nil
}

func autoPrivatePrompt(a *autoRecord, role string) string {
	out := ""
	for _, p := range a.State.Items {
		if p.IntegrationTaskID > 0 || p.ExpertRecoveryTaskID > 0 {
			if pin, e := autoPrivateProposalPin(a, p); e == nil && pin != nil {
				out += fmt.Sprintf("Private adaptation authority: %s. Both auditors inspect the exact baseline and source/reviewer evidence at /work/.lectern-review/integration-%s. Builder adapts the approved output onto the pinned combined base in /work, modifying only integration_paths. Historical source is evidence, not an automatically applicable Git delta. Preserve all unselected paths. No canonical or public write. An independent reviewer receives the sealed candidate, not subsequent mutable builder files.\n", store.J(pin), pin.Key)
			}
		}
	}
	if role == "reviewer" {
		if b := autoPrivateCurrentBuilder(a); b != nil && b.PrivateIntegrationAttempt > 0 {
			v, _ := autoPrivateAttempt(a, b)
			if v != nil && v.Candidate != nil {
				out += fmt.Sprintf("Sealed candidate_tree_sha256=%s; pass this exact field in /integration-tests requests.\n", v.Candidate.TreeSHA)
			}
			out += "Private integration review requires at least one actual successful independent /integration-tests execution against the sealed candidate and meaningful verification of every acceptance criterion. GET /integration-tests lists your retained checks; POST bounded fixed Python script/fixtures/argv/profile integration600 to execute in the isolated candidate environment, then poll by id. Report exact receipt hashes and observed results in reason/evidence. Shell tests in mutable /work alone cannot authorize private publication. Reject inadequate work honestly; a test receipt alone does not establish semantic adequacy.\n"
		}
	}
	return out
}

var autoPrivateCommands = map[string]string{"audit": "integration-audit-copy", "prepare": "integration-prepare", "seal": "integration-seal", "review": "integration-review-copy", "publish": "integration-publish", "consume": "private-source-copy"}
var autoPrivateReady = map[string]string{"audit": "copied", "prepare": "prepared", "seal": "sealed", "review": "copied", "publish": "published_private", "consume": "copied"}

func (s *Server) pollAutoPrivateOperation(ctx context.Context, a *autoRecord, j *autoJob, op *autoPrivateOperation) (bool, error) {
	if op.Done {
		return true, nil
	}
	command, ok := autoPrivateCommands[op.Phase]
	if !ok {
		return false, errors.New("unknown private operation")
	}
	if !a.Config.Enabled {
		return false, nil
	}
	if v := autoPrivateFind(a, op.IntegrationID); v != nil {
		if v.StopRequested && !v.StopConfirmed {
			if err := s.stopAutoPrivate(ctx, a); err != nil {
				return false, err
			}
			if !v.StopConfirmed {
				return false, nil
			}
		}
		if v.StopRequested {
			v.Generation++
			v.StopRequested = false
			v.StopConfirmed = false
		}
		op.Generation = v.Generation
	} else if op.CancelRequested {
		if !op.StopConfirmed {
			if err := s.stopAutoPrivate(ctx, a); err != nil {
				return false, err
			}
			if !op.StopConfirmed {
				return false, nil
			}
		}
		op.Generation++
		op.CancelRequested = false
		op.StopConfirmed = false
	}
	if op.Generation < 1 {
		return false, errors.New("private operation generation missing")
	}
	if err := s.saveAuto(a); err != nil {
		return false, err
	}
	if autoSHA(op.Request) != op.RequestSHA {
		return false, errors.New("private operation request integrity")
	}
	if err := autoWritePrivateRequest(j.ID, op.IntegrationID, op.Phase, op.Request); err != nil {
		return false, err
	}
	raw, err := s.runAutoCommand(ctx, command, "--job", j.ID, "--integration-id", op.IntegrationID, "--generation", fmt.Sprint(op.Generation))
	if err != nil {
		return false, err
	}
	var r struct {
		Schema     int    `json:"schema_version"`
		Pin        string `json:"pin_key"`
		Root       string `json:"root_id"`
		Project    int64  `json:"project_id"`
		ID         string `json:"integration_id"`
		Owner      string `json:"owner_job"`
		Task       int64  `json:"owner_task"`
		Phase      string `json:"phase"`
		Request    string `json:"request_sha256"`
		Generation int    `json:"generation"`
		State      string `json:"state"`
		Reason     string `json:"reason"`
		SHA        string `json:"receipt_sha256"`
	}
	var expected struct {
		Pin     string `json:"pin_key"`
		Root    string `json:"root_id"`
		Project int64  `json:"project_id"`
	}
	if json.Unmarshal(op.Request, &expected) != nil {
		return false, errors.New("private persisted request invalid")
	}
	if json.Unmarshal(raw, &r) != nil || r.Schema != 1 || r.Pin != expected.Pin || r.Root != expected.Root || r.Project != expected.Project || r.ID != op.IntegrationID || r.Owner != j.ID || r.Task != j.TaskID || r.Phase != op.Phase || r.Request != op.RequestSHA || r.Generation != op.Generation {
		return false, errors.New("private operation receipt binding mismatch")
	}
	if op.Phase == "publish" && r.State == "base_advanced" {
		if !autoHash256(r.SHA) {
			return false, errors.New("private conflict receipt checksum absent")
		}
		op.Done = true
		op.Receipt = append(json.RawMessage(nil), raw...)
		if v := autoPrivateFind(a, op.IntegrationID); v != nil {
			v.Status = "base_advanced"
			v.Reason = r.Reason + "; retain candidate and independently reviewed evidence, propose fresh audited adaptation against current managed tip"
		}
		return false, s.saveAuto(a)
	}
	if r.State == "rejected" || r.State == "failed" || r.State == "unavailable" {
		if !autoHash256(r.SHA) {
			return false, errors.New("private terminal failure checksum absent")
		}
		op.Done = true
		op.Receipt = append(json.RawMessage(nil), raw...)
		reason := fmt.Sprintf("private %s: %s", op.Phase, r.Reason)
		if v := autoPrivateFind(a, op.IntegrationID); v != nil && v.Publication == nil && op.Phase != "consume" {
			v.Status = "unavailable"
			v.Reason = reason
		}
		if err := s.saveAuto(a); err != nil {
			return false, err
		}
		return false, &autoPrivateOperationError{reason}
	}
	if r.State != autoPrivateReady[op.Phase] {
		return false, nil
	}
	if !autoHash256(r.SHA) {
		return false, errors.New("private terminal receipt missing checksum")
	}
	if err := autoValidatePrivateTerminal(op, raw); err != nil {
		return false, err
	}
	op.Done = true
	op.Receipt = append(json.RawMessage(nil), raw...)
	if err = s.saveAuto(a); err != nil {
		return false, err
	}
	return true, nil
}
func (s *Server) prepareAutoPrivate(ctx context.Context, a *autoRecord, j *autoJob) (bool, error) {
	if j.PrivateIntegrationAttempt > 0 {
		v, e := autoPrivateAttempt(a, j)
		if e != nil {
			return false, e
		}
		if v.StopRequested && !v.StopConfirmed {
			if e = s.stopAutoPrivate(ctx, a); e != nil {
				return false, e
			}
			if !v.StopConfirmed {
				return false, nil
			}
		}
		if v.StopRequested {
			v.Generation++
			v.StopRequested = false
			v.StopConfirmed = false
			if e = s.saveAuto(a); e != nil {
				return false, e
			}
		}
	}
	for _, op := range j.PrivateOperations {
		if op.Phase != "prepare" && op.Phase != "review" && op.Phase != "consume" && op.Phase != "audit" {
			continue
		}
		ready, e := s.pollAutoPrivateOperation(ctx, a, j, op)
		if e != nil || !ready {
			return ready, e
		}
	}
	if len(j.PrivateOperations) > 0 {
		task, e := s.DB.Task(j.TaskID)
		if e != nil {
			return false, e
		}
		if e = os.WriteFile(filepath.Join(autoRoot, j.ID, "prompt.txt"), autoResumePrompt(task.Prompt, j.ReportError), 0600); e != nil {
			return false, e
		}
	}
	return true, nil
}
func autoPrivatePending(a *autoRecord) bool {
	if a.PrivateIntegration != nil {
		for _, r := range a.PrivateIntegration.Runs {
			for _, v := range r.Attempts {
				if v.StopRequested && !v.StopConfirmed {
					return true
				}
				for _, l := range v.TestLeases {
					if l.Receipt == nil && !l.StopConfirmed {
						return true
					}
				}
			}
		}
	}
	for _, j := range a.Jobs {
		for _, op := range j.PrivateOperations {
			if !op.Done {
				return true
			}
		}
	}
	return false
}
func (s *Server) stopAutoPrivate(ctx context.Context, a *autoRecord) error {
	ids := map[string]int{}
	if a.PrivateIntegration != nil {
		for _, r := range a.PrivateIntegration.Runs {
			for _, v := range r.Attempts {
				active := !autoPrivateAttemptTerminal(v.Status)
				for _, l := range v.TestLeases {
					if !l.StopConfirmed {
						active = true
					}
				}
				if active && !v.StopConfirmed {
					v.StopRequested = true
					ids[v.ID] = v.Generation
				}
			}
		}
	}
	for _, j := range a.Jobs {
		for _, op := range j.PrivateOperations {
			if op.Done || op.StopConfirmed {
				continue
			}
			op.CancelRequested = true
			if v := autoPrivateFind(a, op.IntegrationID); v != nil {
				v.StopRequested = true
			}
			if op.Generation > ids[op.IntegrationID] {
				ids[op.IntegrationID] = op.Generation
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if err := s.saveAuto(a); err != nil {
		return err
	}
	// Bounded stop RPCs; any unconfirmed intent remains for the disabled tick.
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for id, g := range ids {
		raw, e := s.runAutoCommand(c, "integration-stop", "--integration-id", id, "--generation", fmt.Sprint(g))
		if e != nil {
			return e
		}
		var r struct {
			State      string `json:"state"`
			Generation int    `json:"generation"`
			ID         string `json:"integration_id"`
		}
		if json.Unmarshal(raw, &r) != nil || r.ID != id || r.Generation < g {
			return errors.New("private stop receipt mismatch")
		}
		if r.State != "stopped" {
			continue
		}
		if v := autoPrivateFind(a, id); v != nil {
			v.StopConfirmed = true
		}
		for _, j := range a.Jobs {
			for _, op := range j.PrivateOperations {
				if op.IntegrationID == id && op.Generation <= g {
					op.StopConfirmed = true
				}
			}
		}
	}
	return s.saveAuto(a)
}

func autoValidatePrivateReport(a *autoRecord, j *autoJob, raw []byte) error {
	if j.PrivateIntegrationAttempt == 0 || j.Role != "reviewer" {
		return nil
	}
	v, e := autoPrivateAttempt(a, j)
	if e != nil {
		return e
	}
	var verdict autonomy.Verdict
	if e = json.Unmarshal(raw, &verdict); e != nil {
		return e
	}
	if !verdict.AcceptsWork() {
		return nil
	}
	for _, t := range v.Tests {
		if t.State == "exited" && t.Executed && t.ExitCode != nil && *t.ExitCode == 0 && !t.Truncated && autoPrivateCheckMatchesRuntime(j, t) && strings.Contains(string(raw), t.ReceiptSHA) {
			return nil
		}
	}
	return errors.New("private integration approval requires an executed successful independent /integration-tests receipt for this retained runtime and its exact receipt_sha256 in reason")
}
func (s *Server) finishAutoPrivate(ctx context.Context, a *autoRecord, j *autoJob, report []byte) error {
	if j.PrivateIntegrationAttempt == 0 {
		return nil
	}
	v, e := autoPrivateAttempt(a, j)
	if e != nil {
		return e
	}
	if j.Role == "builder" {
		if v.Candidate != nil {
			return nil
		}
		sha, e := s.autoArchiveIdentity(ctx, j)
		if e != nil {
			return e
		}
		var prepare *autoPrivateOperation
		for _, old := range a.Jobs {
			if old.TaskID == j.TaskID {
				for _, op := range old.PrivateOperations {
					if op.Phase == "prepare" && op.Done && op.IntegrationID == v.ID {
						prepare = op
						break
					}
				}
			}
		}
		if prepare == nil {
			return errors.New("private prepare receipt missing")
		}
		var prepared struct {
			Base string `json:"base_tree_sha256"`
		}
		if json.Unmarshal(prepare.Receipt, &prepared) != nil || !autoHash256(prepared.Base) {
			return errors.New("private prepared base identity missing")
		}
		m := autoPrivateEnvelope(&v.Authority.Pin, v.ID, j)
		m["prepare_request_sha256"] = prepare.RequestSHA
		m["builder_archive_sha256"] = sha
		m["expected_base_tree_sha256"] = prepared.Base
		if e = autoQueuePrivate(j, v.ID, "seal", v.Generation, m); e != nil {
			return e
		}
		var op *autoPrivateOperation
		for _, o := range j.PrivateOperations {
			if o.Phase == "seal" {
				op = o
			}
		}
		ready, e := s.pollAutoPrivateOperation(ctx, a, j, op)
		if e != nil {
			return e
		}
		if !ready {
			return errAutoArtifactPending
		}
		var r struct {
			NoChanges   *bool  `json:"no_changes"`
			Tree        string `json:"candidate_tree_sha256"`
			Archive     string `json:"candidate_archive_sha256"`
			Commit      string `json:"candidate_commit"`
			GitTree     string `json:"candidate_tree_oid"`
			GitTreeAlt  string `json:"git_tree"`
			Manifest    string `json:"candidate_manifest_sha256"`
			ManifestAlt string `json:"changed_manifest_sha256"`
			SHA         string `json:"receipt_sha256"`
		}
		if e = json.Unmarshal(op.Receipt, &r); e != nil {
			return e
		}
		if r.NoChanges == nil {
			return errors.New("private candidate lacks explicit change projection")
		}
		if r.GitTree == "" {
			r.GitTree = r.GitTreeAlt
		}
		if r.Manifest == "" {
			r.Manifest = r.ManifestAlt
		}
		return autoRecordPrivateCandidate(v, autoPrivateCandidate{NoChanges: *r.NoChanges, IntegrationID: v.ID, PinKey: v.PinKey, BuilderJob: j.ID, ArchiveSHA: r.Archive, TreeSHA: r.Tree, CandidateCommit: r.Commit, GitTree: r.GitTree, ManifestSHA: r.Manifest, BaseTree: v.Authority.Pin.BaseTree, BaseTreeSHA: prepared.Base, BuilderArchiveSHA: sha, PrepareRequestSHA: prepare.RequestSHA, ReceiptSHA: r.SHA})
	}
	if j.Role == "reviewer" {
		var verdict autonomy.Verdict
		if e = json.Unmarshal(report, &verdict); e != nil {
			return e
		}
		sha, e := s.autoArchiveIdentity(ctx, j)
		if e != nil {
			return e
		}
		refs := []string{}
		for _, t := range v.Tests {
			if strings.Contains(string(report), t.ReceiptSHA) {
				refs = append(refs, t.ReceiptSHA)
			}
		}
		return autoRecordPrivateReview(v, autoPrivateReview{JobID: j.ID, TaskID: j.TaskID, ArchiveSHA: sha, ReportSHA: autoSHA(report), CandidateSHA: v.Candidate.TreeSHA, TestReceiptSHAs: refs, Approved: verdict.AcceptsWork()})
	}
	return nil
}
func (s *Server) pollAutoPrivatePublications(ctx context.Context, a *autoRecord) error {
	if a.PrivateIntegration == nil {
		return nil
	}
	for _, run := range a.PrivateIntegration.Runs {
		for _, v := range run.Attempts {
			if v.Status != "publication_prepared" {
				continue
			}
			j := autoFindJob(a, v.ReviewerTaskID)
			if j == nil || j.ID != v.ReviewerJob || j.Status != "done" {
				continue
			}
			m := autoPrivateEnvelope(&v.Authority.Pin, v.ID, j)
			m["candidate_receipt_sha256"] = v.Candidate.ReceiptSHA
			m["candidate_tree_sha256"] = v.Candidate.TreeSHA
			m["candidate_commit"] = v.Candidate.CandidateCommit
			m["expected_managed_commit"] = v.Authority.Pin.ExpectedManagedCommit
			m["review"] = map[string]any{"job": v.Review.JobID, "archive_sha256": v.Review.ArchiveSHA, "report_sha256": v.Review.ReportSHA}
			checks := []map[string]string{}
			for _, t := range v.Tests {
				for _, ref := range v.Review.TestReceiptSHAs {
					if ref == t.ReceiptSHA && t.State == "exited" && t.Executed && t.ExitCode != nil && *t.ExitCode == 0 && !t.Truncated && autoPrivateCheckMatchesRuntime(j, t) {
						checks = append(checks, map[string]string{"check_id": t.CheckID, "receipt_sha256": t.ReceiptSHA})
					}
				}
			}
			m["checks"] = checks
			m["authorization_sha256"] = autoSHA([]byte(store.J(v.Review)))
			if e := autoQueuePrivate(j, v.ID, "publish", v.Generation, m); e != nil {
				return e
			}
			var op *autoPrivateOperation
			for _, o := range j.PrivateOperations {
				if o.Phase == "publish" {
					op = o
				}
			}
			ready, e := s.pollAutoPrivateOperation(ctx, a, j, op)
			if e != nil {
				return e
			}
			if !ready {
				continue
			}
			var p autoPrivatePublication
			if e = json.Unmarshal(op.Receipt, &p); e != nil {
				return e
			}
			p.ID = v.ID
			if e = autoRecordPrivatePublication(v, p); e != nil {
				return e
			}
		}
	}
	return nil
}
func autoPrivatePublishPending(a *autoRecord) bool {
	if a.PrivateIntegration != nil {
		for _, r := range a.PrivateIntegration.Runs {
			for _, v := range r.Attempts {
				if v.Status == "publication_prepared" {
					return true
				}
			}
		}
	}
	return false
}

func autoPrivateCharge(a *autoRecord, j *autoJob, raw []byte) error {
	if j.PrivateIntegrationAttempt == 0 {
		return nil
	}
	v, e := autoPrivateAttempt(a, j)
	if e != nil {
		return e
	}
	var status struct {
		Elapsed *int64 `json:"elapsed_milliseconds"`
	}
	if json.Unmarshal(raw, &status) != nil || status.Elapsed == nil {
		return errors.New("private execution accounting unavailable")
	}
	return autoChargePrivateIntegration(a.PrivateIntegration.Runs[v.RootID], v, j.ID, j.Role == "reviewer", *status.Elapsed)
}
func (s *Server) endAutoPrivateBudget(ctx context.Context, a *autoRecord, j *autoJob) (bool, error) {
	if j.PrivateIntegrationAttempt == 0 {
		return false, nil
	}
	v, e := autoPrivateAttempt(a, j)
	if e != nil {
		return false, e
	}
	left, e := autoProgressRemaining(a, j)
	if e != nil {
		return false, e
	}
	if left >= time.Second {
		return false, nil
	}
	if j.Status == "running" || j.Status == "starting" {
		if _, e = s.runAutoCommand(ctx, "stop", "--job", j.ID); e != nil {
			return true, e
		}
	}
	if e = s.snapshotAutoJob(ctx, j); e != nil {
		return true, e
	}
	v.Status = "budget_exhausted"
	if j.ExpertRecoveryAttempt > 0 {
		ev, err := autoExpertAttempt(a, j)
		if err != nil {
			return true, err
		}
		if err = autoFinishExpertRecovery(ev, "budget_exhausted", 0, "Private expert execution budget exhausted; no reviewer outcome", time.Now()); err != nil {
			return true, err
		}
	}
	j.Status = "failed"
	j.Summary = "Private integration cumulative " + j.Role + " runtime exhausted; evidence retained, no review or publication invented"
	for i := range a.State.Assignments {
		if a.State.Assignments[i].TaskID == j.TaskID {
			a.State.Assignments[i].Completed = true
		}
	}
	a.State.Phase = autonomy.Complete
	a.State.Reason = j.Summary
	a.Reason = j.Summary
	s.closeAutoBridge(j.ID)
	_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": "failed"})
	return true, s.saveAuto(a)
}
func autoQueuePrivateResume(a *autoRecord, old *autoJob, id, sha string) (*autoJob, error) {
	if !autoExpertJobID.MatchString(id) || !autoHash256(sha) {
		return nil, errors.New("invalid private retry identity")
	}
	var v *autoPrivateIntegrationAttempt
	owner := old.ID
	if old.PrivateIntegrationAttempt > 0 {
		var e error
		v, e = autoPrivateAttempt(a, old)
		if e != nil {
			return nil, e
		}
		owner = v.JobID
		if old.Role == "reviewer" {
			owner = v.ReviewerJob
		}
	} else if current := autoFindJob(a, old.TaskID); current != nil {
		owner = current.ID
	}
	if owner != old.ID {
		for _, j := range a.Jobs {
			if j.ID == owner && j.TaskID == old.TaskID {
				return j, nil
			}
		}
		return nil, errors.New("private retry owner missing")
	}
	if v != nil && old.Role == "builder" && v.Candidate != nil {
		return nil, errors.New("sealed private candidate cannot be modified by operational retry")
	}
	j := *old
	j.ID = id
	j.Status = "prepared"
	j.ArtifactPath = filepath.Join(autoRoot, id, "work")
	j.StartedAt = time.Now()
	j.LaunchRetryPaid = false
	j.DocumentationStopped = false
	j.PrivateOperations = nil
	j.PythonTestNeedsResume = false
	j.PythonTestRecovery = nil
	autoPreparePythonResume(old, &j)
	autoPrepareNodeResume(old, &j)
	j.DocumentationCopies = []autoDocumentationCopy{{Command: "copy-archive-resume", SourceJob: old.ID, SHA: sha, Generation: 1}}
	if old.ReportError != "" {
		j.ReportRepairs++
		j.ReportRetryAt = time.Time{}
	}
	if old.Admission != nil {
		receipt := *old.Admission
		receipt.JobID = id
		j.Admission = &receipt
	}
	if v != nil {
		if old.Role == "reviewer" {
			v.ReviewerJob = id
		} else {
			v.JobID = id
		}
	}
	if old.ExpertRecoveryAttempt > 0 {
		ev, err := autoExpertAttempt(a, old)
		if err != nil {
			return nil, err
		}
		if old.Role == "reviewer" {
			ev.ReviewerJob = id
		} else {
			ev.JobID = id
		}
	}
	a.Jobs = append(a.Jobs, &j)
	return &j, nil
}
func (s *Server) resumeAutoPrivate(ctx context.Context, a *autoRecord, old *autoJob) error {
	if e := s.snapshotAutoJob(ctx, old); e != nil {
		return fmt.Errorf("%w: private retry preservation: %v", errAutoArtifactPending, e)
	}
	sha, e := s.autoArchiveIdentity(ctx, old)
	if e != nil {
		return e
	}
	j, e := autoQueuePrivateResume(a, old, autoUUID(), sha)
	if e != nil {
		return e
	}
	if e = s.saveAuto(a); e != nil {
		return e
	}
	return s.launchAutoJob(ctx, a, j)
}
func autoPrivateProposalPin(a *autoRecord, p autonomy.Proposal) (*autoPrivateIntegrationPin, error) {
	if p.IntegrationTaskID > 0 {
		return autoPrivatePin(a, p)
	}
	if p.ExpertRecoveryTaskID > 0 {
		pin, e := autoExpertPin(a, p)
		if e != nil {
			return nil, e
		}
		return pin.PrivateIntegration, nil
	}
	return nil, nil
}

// The runner authenticates filesystem bytes; the controller checks that its
// receipt describes the exact frozen inputs before exposing them to a worker.
func autoValidatePrivateTerminal(op *autoPrivateOperation, raw []byte) error {
	var request, receipt map[string]json.RawMessage
	if json.Unmarshal(op.Request, &request) != nil || json.Unmarshal(raw, &receipt) != nil {
		return errors.New("private receipt JSON invalid")
	}
	eq := func(out, in string, source map[string]json.RawMessage) error {
		var x, y string
		if json.Unmarshal(receipt[out], &x) != nil || json.Unmarshal(source[in], &y) != nil || x == "" || x != y {
			return fmt.Errorf("private %s receipt %s differs from pinned input", op.Phase, out)
		}
		return nil
	}
	switch op.Phase {
	case "prepare", "audit":
		var destination, source map[string]json.RawMessage
		if json.Unmarshal(request["destination"], &destination) != nil || json.Unmarshal(request["source"], &source) != nil {
			return errors.New("private input descriptor missing")
		}
		for _, pair := range [][2]string{{"base_commit", "commit"}, {"base_tree_oid", "tree_oid"}} {
			if e := eq(pair[0], pair[1], destination); e != nil {
				return e
			}
		}
		for _, pair := range [][2]string{{"source_archive_sha256", "archive_sha256"}, {"source_review_archive_sha256", "review_archive_sha256"}} {
			if e := eq(pair[0], pair[1], source); e != nil {
				return e
			}
		}
		var digest string
		if json.Unmarshal(receipt["base_tree_sha256"], &digest) != nil || !autoHash256(digest) {
			return errors.New("private raw base digest missing")
		}
	case "review":
		for _, key := range []string{"candidate_receipt_sha256", "candidate_tree_sha256"} {
			if e := eq(key, key, request); e != nil {
				return e
			}
		}
	case "consume":
		for _, key := range []string{"publication_receipt_sha256", "commit", "tree_oid", "tree_sha256"} {
			if e := eq(key, key, request); e != nil {
				return e
			}
		}
	case "seal":
		for _, key := range []string{"prepare_request_sha256", "builder_archive_sha256"} {
			if e := eq(key, key, request); e != nil {
				return e
			}
		}
		if e := eq("base_tree_sha256", "expected_base_tree_sha256", request); e != nil {
			return e
		}
	}
	return nil
}

type autoPrivateOperationError struct{ reason string }

func (e *autoPrivateOperationError) Error() string { return e.reason }
func (s *Server) endAutoPrivateOperation(ctx context.Context, a *autoRecord, j *autoJob, reason string) error {
	// Preserve the runner's immutable rejection rather than retrying an identical
	// terminal request forever or inventing a model/reviewer rejection.
	if j.ExpertRecoveryAttempt > 0 {
		v, e := autoExpertAttempt(a, j)
		if e != nil {
			return e
		}
		if e = autoFinishExpertRecovery(v, "unavailable", 0, reason, time.Now()); e != nil {
			return e
		}
	}
	j.Status = "failed"
	j.Summary = reason
	s.closeAutoBridge(j.ID)
	if j.Role == "auditor_a" || j.Role == "auditor_b" {
		s.stopAutoJobs(ctx, a, reason)
	}
	for i := range a.State.Assignments {
		if a.State.Assignments[i].TaskID == j.TaskID {
			a.State.Assignments[i].Completed = true
		}
	}
	a.State.Phase = autonomy.Complete
	a.State.Reason = reason
	a.Reason = reason
	_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": "failed"})
	return s.saveAuto(a)
}
func autoPrivateCheckMatchesRuntime(j *autoJob, t autoPrivateTestReceipt) bool {
	var goRuntime autoExpertProbeRuntime
	if autoSelectGoTestRuntime(j, &goRuntime) != nil || t.GoDependencyKey != goRuntime.GoDependency || t.GoBundleDigest != goRuntime.GoBundle || t.GoToolchainDigest != goRuntime.GoToolchain {
		return false
	}
	var node autoExpertProbeRuntime
	if autoSelectNodeTestRuntime(j, &node) != nil || t.NodeBundleKey != node.NodeBundle || t.NodeInputKey != node.NodeInput || t.NodeLockSHA != node.NodeLock || t.NodeRuntimeDigest != node.NodeRuntime {
		return false
	}
	if j.PythonUsedBundle == "" && j.PythonExpectedTestKey != "" && t.PythonTestKey != j.PythonExpectedTestKey {
		return false
	}
	if t.PythonBundleKey != j.PythonUsedBundle {
		return false
	}
	browser := ""
	if j.PythonUsedBundle != "" {
		if j.PythonRecovery == nil || j.PythonRecovery.State != "verified" || j.PythonRecovery.BundleKey != j.PythonUsedBundle {
			return false
		}
		if t.PythonInputKey != j.PythonRecovery.InputKey {
			return false
		}
		browser = j.PythonRecovery.BrowserKey
	}
	return t.BrowserKey == browser
}
func autoPrivatePriorCopies(j *autoJob, pin *autoPrivateIntegrationPin) error {
	for _, pair := range [][2]string{{pin.PriorBuilderJob, pin.PriorBuilderSHA}, {pin.PriorReviewJob, pin.PriorReviewSHA}} {
		if pair[0] == "" && pair[1] == "" {
			continue
		}
		if !autoExpertJobID.MatchString(pair[0]) || !autoHash256(pair[1]) {
			return errors.New("private prior evidence identity unavailable")
		}
		found := false
		for _, copy := range j.DocumentationCopies {
			if copy.Command == "copy-archive-review" && copy.SourceJob == pair[0] {
				if copy.SHA != pair[1] {
					return errors.New("private prior evidence archive changed")
				}
				found = true
			}
		}
		if !found {
			j.DocumentationCopies = append(j.DocumentationCopies, autoDocumentationCopy{Command: "copy-archive-review", SourceJob: pair[0], SHA: pair[1]})
		}
	}
	return nil
}
