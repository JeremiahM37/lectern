package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const autoMaintenanceExternalProfile = "registered_service_external_health_v1"

type autoMaintenanceExternalHealthReceipt struct {
	ConflictReceiptSHA string    `json:"conflict_receipt_sha256"`
	OwnershipChecked   bool      `json:"ownership_checked"`
	OwnedCandidate     bool      `json:"owned_candidate"`
	OperationID        string    `json:"operation_id"`
	ReceiptSHA         string    `json:"receipt_sha256"`
	RegistrySHA        string    `json:"registry_sha256"`
	CurrentStateSHA    string    `json:"current_state_sha256"`
	PostStateSHA       string    `json:"post_state_sha256"`
	InvocationID       string    `json:"invocation_id"`
	PostInvocationID   string    `json:"post_invocation_id"`
	ObservedAt         time.Time `json:"observed_at"`
	CompletedAt        time.Time `json:"completed_at"`
	Profile            string    `json:"profile"`
	NoMutation         bool      `json:"no_mutation"`
	Healthy            bool      `json:"healthy"`
}
type autoMaintenanceConflictInspection struct {
	ConflictReceiptSHA string                                `json:"conflict_receipt_sha256"`
	ID                 string                                `json:"id"`
	Number             int                                   `json:"number"`
	Request            string                                `json:"request"`
	Binding            autoMaintenanceExecutionBinding       `json:"binding"`
	State              string                                `json:"state"`
	LaunchRequested    bool                                  `json:"launch_requested,omitempty"`
	Started            bool                                  `json:"started"`
	Receipt            string                                `json:"receipt,omitempty"`
	Proof              *autoMaintenanceExternalHealthReceipt `json:"proof,omitempty"`
	Reason             string                                `json:"reason,omitempty"`
	StopConfirmed      bool                                  `json:"stop_confirmed,omitempty"`
}

func autoNewMaintenanceConflictInspection(t *autoMaintenanceTransaction) (*autoMaintenanceConflictInspection, error) {
	if t.State != "conflict" || len(t.Authority) == 0 || !autoHash256(t.Binding.AuthoritySHA) || autoSHA([]byte(t.Authority)) != t.Binding.AuthoritySHA {
		return nil, errors.New("conflict inspection requires frozen original mutation authority")
	}
	number := len(t.Inspections) + 1
	raw, authority, b, e := autoBuildMaintenanceExecution(&t.Candidate.Admission, *t.Candidate.Validation, t.Candidate.Review, *t.Backup, t.Generation+number)
	if e != nil {
		return nil, e
	}
	if string(authority) != t.Authority || b.SemanticRequestSHA != t.Binding.SemanticRequestSHA {
		return nil, errors.New("inspection changed original authority")
	}
	b.Phase = "inspect"
	id := autoSHA([]byte(fmt.Sprintf("maintenance-inspection-v1:%s:%d:%s", t.ID, number, b.RequestSHA)))
	conflictSHA, e := autoRetainedMaintenanceConflictSHA(t)
	if e != nil {
		return nil, e
	}
	return &autoMaintenanceConflictInspection{ConflictReceiptSHA: conflictSHA, ID: id, Number: number, Request: string(raw), Binding: b, State: "reserved"}, nil
}
func autoDecodeMaintenanceInspection(raw []byte, l *autoMaintenanceConflictInspection, now time.Time) (string, *autoMaintenanceExternalHealthReceipt, error) {
	fail := func() (string, *autoMaintenanceExternalHealthReceipt, error) {
		return "", nil, errors.New("maintenance external inspection binding invalid")
	}
	if l == nil || len(raw) > 256<<10 || autoVerifyMaintenanceSeal(raw) != nil || autoSHA([]byte(l.Request)) != l.Binding.RequestSHA {
		return fail()
	}
	b := l.Binding
	var out struct {
		Schema     int             `json:"schema_version"`
		State      string          `json:"state"`
		Operation  string          `json:"operation_id"`
		Owner      string          `json:"owner_job"`
		Task       int64           `json:"owner_task"`
		Pin        string          `json:"pin_sha256"`
		Phase      string          `json:"phase"`
		Inspection string          `json:"inspection_id"`
		Generation int             `json:"generation"`
		RequestSHA string          `json:"request_sha256"`
		Result     json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Schema != 1 || out.Operation != b.OperationID || out.Owner != b.OwnerJob || out.Task != b.OwnerTask || out.Pin != b.PinSHA || out.Phase != "inspect" || out.Inspection != l.ID || out.Generation != b.Generation || out.RequestSHA != b.RequestSHA {
		return fail()
	}
	switch out.State {
	case "running", "waiting", "unavailable", "conflict", "cancelled", "external_unhealthy":
		return out.State, nil, nil
	case "external_healthy":
	default:
		return fail()
	}
	if autoVerifyMaintenanceSeal(out.Result) != nil {
		return fail()
	}
	var r struct {
		ConflictReceiptSHA string  `json:"conflict_receipt_sha256"`
		Schema             int     `json:"schema_version"`
		State              string  `json:"state"`
		SHA                string  `json:"receipt_sha256"`
		Operation          string  `json:"operation_id"`
		Request            string  `json:"request_sha256"`
		Authority          string  `json:"authority_sha256"`
		Before             string  `json:"before_sha256"`
		Candidate          string  `json:"candidate_sha256"`
		Generation         int     `json:"generation"`
		Inspection         string  `json:"inspection_id"`
		Registry           string  `json:"registry_sha256"`
		Current            string  `json:"current_state_sha256"`
		Post               string  `json:"post_state_sha256"`
		Invocation         string  `json:"invocation_id"`
		PostInvocation     string  `json:"post_invocation_id"`
		Observed           float64 `json:"observed_at"`
		Completed          float64 `json:"completed_at"`
		Profile            string  `json:"profile"`
		OwnedCandidate     *bool   `json:"owned_candidate"`

		NoMutation   *bool `json:"no_mutation"`
		Observations []struct {
			Healthy  *bool   `json:"healthy"`
			At       float64 `json:"observed_at"`
			Response string  `json:"response_sha256"`
			Metrics  string  `json:"metrics_sha256"`
		} `json:"observations"`
	}
	if json.Unmarshal(out.Result, &r) != nil || !autoHash256(r.ConflictReceiptSHA) || r.ConflictReceiptSHA != l.ConflictReceiptSHA || r.Schema != 1 || r.State != out.State || r.Operation != b.OperationID || r.Request != b.SemanticRequestSHA || r.Authority != b.AuthoritySHA || r.Before != b.BeforeSHA || r.Candidate != b.CandidateSHA || r.Generation != b.Generation || r.Inspection != l.ID || r.Profile != autoMaintenanceExternalProfile || r.NoMutation == nil || !*r.NoMutation || r.OwnedCandidate == nil || *r.OwnedCandidate || !autoHash256(r.Registry) || !autoHash256(r.Current) || r.Post != r.Current || r.Invocation == "" || r.PostInvocation != r.Invocation {
		return fail()
	}
	var request struct {
		Registry string `json:"registry_sha256"`
	}
	if json.Unmarshal([]byte(l.Request), &request) != nil || r.Registry != request.Registry {
		return fail()
	}
	if len(r.Observations) < 3 || len(r.Observations) > 16 || r.Observed <= 0 || r.Completed < r.Observed || r.Completed-r.Observed > 60 {
		return fail()
	}
	previous := r.Observed
	for _, o := range r.Observations {
		if o.Healthy == nil || !*o.Healthy || o.At < previous || o.At > r.Completed || !autoHash256(o.Response) || !autoHash256(o.Metrics) {
			return fail()
		}
		previous = o.At
	}
	proof := &autoMaintenanceExternalHealthReceipt{ConflictReceiptSHA: r.ConflictReceiptSHA, OwnershipChecked: true, OwnedCandidate: false, OperationID: b.OperationID, ReceiptSHA: r.SHA, RegistrySHA: r.Registry, CurrentStateSHA: r.Current, PostStateSHA: r.Post, InvocationID: r.Invocation, PostInvocationID: r.PostInvocation, ObservedAt: time.Unix(0, int64(r.Observed*1e9)), CompletedAt: time.Unix(0, int64(r.Completed*1e9)), Profile: r.Profile, NoMutation: true, Healthy: true}
	return out.State, proof, nil
}
func autoWriteMaintenanceInspection(root string, t *autoMaintenanceTransaction, l *autoMaintenanceConflictInspection) error {
	if !autoHash256(l.ID) || l.Binding.OperationID != t.ID || !autoExpertJobID.MatchString(l.Binding.OwnerJob) || l.Binding.OwnerJob != t.Candidate.Admission.JobID || l.Binding.OwnerTask != t.Candidate.Admission.TaskID || autoSHA([]byte(l.Request)) != l.Binding.RequestSHA {
		return errors.New("maintenance inspection request invalid")
	}
	dir := filepath.Join(root, l.Binding.OwnerJob, "server-maintenance")
	for _, p := range []string{filepath.Join(root, l.Binding.OwnerJob), dir} {
		st, e := os.Lstat(p)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe maintenance inspection directory")
		}
	}
	path := filepath.Join(dir, t.ID+".inspect-"+l.ID+".json")
	f, e := os.CreateTemp(dir, ".inspect-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.WriteString(l.Request); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Link(f.Name(), path); os.IsExist(e) {
		old, re := autoReadRegular(path, 32<<10)
		if re != nil || string(old) != l.Request {
			return errors.New("immutable maintenance inspection request differs")
		}
	} else if e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func autoAdvanceMaintenanceConflict(ctx context.Context, a *autoRecord, t *autoMaintenanceTransaction, registry autoMaintenanceRegistry, now time.Time, io autoMaintenanceControllerIO) error {
	if t.State != "conflict" || a.Maintenance == nil || a.Maintenance.Operations[t.ID] == nil || a.Maintenance.Operations[t.ID].State != "conflict" {
		return errors.New("external inspection has no conflicted owned operation")
	}
	// Older controller versions retained the root conflict receipt without
	// projecting its journal SHA. Recover only from the exact bound archive.
	op := a.Maintenance.Operations[t.ID]
	if op.ConflictReceiptSHA == "" {
		for i := len(t.Receipts) - 1; i >= 0; i-- {
			var outer struct {
				Phase string `json:"phase"`
			}
			if json.Unmarshal(t.Receipts[i], &outer) != nil {
				continue
			}
			b := t.Binding
			b.Phase = outer.Phase
			r, e := autoDecodeMaintenanceExecution(t.Receipts[i], b)
			if e == nil && r.State == "rollback_conflict" && autoHash256(r.InnerReceiptSHA) {
				op.ConflictReceiptSHA = r.InnerReceiptSHA
				break
			}
		}
		if op.ConflictReceiptSHA == "" {
			return errors.New("retained conflict journal receipt missing")
		}
		if e := io.Save(); e != nil {
			return e
		}
	}
	// No provider/model quota applies to fixed read-only cleanup. Missing or
	// changed registration does not grant authority or release ownership.
	registered := registry.Digest == t.Candidate.Admission.Pin.RegistrySHA && registry.ResourceID == t.Candidate.Admission.Pin.TargetID+"/"+t.Candidate.Admission.Pin.ServiceID && !registry.Protected && registry.Stateless

	if now.Before(t.InspectionRetryAt) {
		return nil
	}
	var l *autoMaintenanceConflictInspection
	if len(t.Inspections) > 0 {
		last := t.Inspections[len(t.Inspections)-1]
		if last.State == "reserved" || last.State == "running" || last.State == "waiting" {
			l = last
		}
	}
	if l == nil {
		if !registered {
			return errors.New("external inspection registry unavailable or changed")
		}
		var e error
		l, e = autoNewMaintenanceConflictInspection(t)
		if e != nil {
			return e
		}
		t.Inspections = append(t.Inspections, l)
		if e = io.Save(); e != nil {
			return e
		}
	}
	if io.InspectWrite == nil {
		return errors.New("inspection request writer unavailable")
	}
	command := "server-maintenance-inspect-status"
	if !l.Started && !l.LaunchRequested {
		if !registered {
			return errors.New("external inspection registry unavailable or changed")
		}
		if e := io.InspectWrite(t, l); e != nil {
			return e
		}
		l.LaunchRequested = true
		if e := io.Save(); e != nil {
			return e
		}
		command = "server-maintenance-inspect"
	}
	raw, e := io.Call(ctx, command, "--job", l.Binding.OwnerJob, "--operation-id", t.ID, "--inspection-id", l.ID, "--generation", strconv.Itoa(l.Binding.Generation))
	if e != nil {
		return e
	}
	state, proof, e := autoDecodeMaintenanceInspection(raw, l, time.Now())
	if e != nil {
		return e
	}
	l.Started = state != "waiting"
	if state == "waiting" {
		l.LaunchRequested = false
	}
	l.State = state
	if state == "running" || state == "waiting" {
		return io.Save()
	}
	l.Receipt = string(raw)
	l.Proof = proof
	if state == "external_healthy" {
		if !registered {
			l.State = "proof_rejected"
			l.Reason = "registry changed or unavailable after inspection"
			t.InspectionRetryAt = time.Now().Add(30 * time.Second)
			return errors.Join(errors.New(l.Reason), io.Save())
		}
		if e = autoResolveMaintenanceConflict(a.Maintenance.Operations[t.ID], *proof, time.Now()); e != nil {
			l.State = "proof_rejected"
			l.Reason = e.Error()
			t.InspectionRetryAt = time.Now().Add(30 * time.Second)
			return errors.Join(e, io.Save())
		}
		t.State = "superseded"
		t.Reason = "Healthy external generation retained; fresh audited plan required for any subsequent mutation"
	} else {
		// A terminal probe cannot become healthy in place. Retain it and admit a
		// fresh read-only ID after bounded cooldown; original effects stay revoked.
		delay := 30 * time.Second
		for i := 1; i < len(t.Inspections) && delay < 15*time.Minute; i++ {
			delay *= 2
		}
		if delay > 30*time.Minute {
			delay = 30 * time.Minute
		}
		t.InspectionRetryAt = time.Now().Add(delay)
		t.Reason = "External generation inspection " + state + "; ownership retained, read-only retry scheduled"
	}
	return io.Save()
}

func autoRetainedMaintenanceConflictSHA(t *autoMaintenanceTransaction) (string, error) {
	for i := len(t.Receipts) - 1; i >= 0; i-- {
		var out struct {
			Phase string `json:"phase"`
		}
		if json.Unmarshal(t.Receipts[i], &out) != nil {
			continue
		}
		b := t.Binding
		b.Phase = out.Phase
		r, e := autoDecodeMaintenanceExecution(t.Receipts[i], b)
		if e == nil && r.State == "rollback_conflict" && autoHash256(r.InnerReceiptSHA) {
			return r.InnerReceiptSHA, nil
		}
	}
	return "", errors.New("authenticated original conflict journal unavailable")
}
