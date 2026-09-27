package autonomy

// GPU research state is deliberately independent of model reports. Only the
// controller's authenticated runner adapter may submit receipts or qualification.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const GPUResearchTarget = "aiserver-amd-research-v1"
const gpuResearchLimitMS int64 = 60 * 60 * 1000

var gpuHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var gpuUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type GPUResearchRequest struct {
	SchemaVersion    int      `json:"schema_version"`
	OwnerJob         string   `json:"owner_job"`
	OwnerTask        int64    `json:"owner_task"`
	Role             string   `json:"role"`
	ExperimentRoot   string   `json:"experiment_root"`
	SourceJob        string   `json:"source_job"`
	SourceSnapshotID string   `json:"source_snapshot_id"`
	SourceTreeSHA    string   `json:"source_tree_sha256"`
	SourceArchiveSHA string   `json:"source_archive_sha256"`
	AdmissionSHA     string   `json:"admission_sha256"`
	AcceptanceSHA    string   `json:"acceptance_sha256"`
	Target           string   `json:"target"`
	RuntimeKey       string   `json:"runtime_key"`
	QualificationSHA string   `json:"qualification_sha256"`
	Profile          string   `json:"profile"`
	Script           string   `json:"script"`
	Argv             []string `json:"argv"`
	Trial            int      `json:"trial"`
}

type GPUResearchReceipt struct {
	RunID      string `json:"run_id"`
	RequestSHA string `json:"request_sha256"`
	State      string `json:"state"`
	// nil means the trusted supervisor could not establish whether execution
	// started; this consumes the full reserved profile rather than freeing budget.
	Executed         *bool  `json:"executed"`
	ChargedMS        int64  `json:"charged_ms"`
	ElapsedMS        int64  `json:"elapsed_ms,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	OutputSHA        string `json:"output_sha256,omitempty"`
	CleanupConfirmed bool   `json:"cleanup_confirmed"`
	ReceiptSHA       string `json:"receipt_sha256"`
}

type GPUResearchLease struct {
	SemanticSHA      string              `json:"semantic_sha256"`
	ID               string              `json:"id"`
	Request          GPUResearchRequest  `json:"request"`
	RequestRaw       json.RawMessage     `json:"request_raw"`
	RequestSHA       string              `json:"request_sha256"`
	QualificationSHA string              `json:"qualification_sha256"`
	ReservedAt       time.Time           `json:"reserved_at"`
	StartedAt        time.Time           `json:"started_at,omitempty"`
	SettledAt        time.Time           `json:"settled_at,omitempty"`
	MaxMS            int64               `json:"max_ms"`
	StopRequested    bool                `json:"stop_requested"`
	Receipt          *GPUResearchReceipt `json:"receipt,omitempty"`
}

type GPUResearchLedger struct {
	Runs map[string]*GPUResearchLease `json:"runs"`
}

func gpuDigest(raw []byte) string { v := sha256.Sum256(raw); return hex.EncodeToString(v[:]) }
func (r GPUResearchRequest) Validate() error {
	if r.SchemaVersion != 1 || r.Target != GPUResearchTarget || r.Profile != "gpu-screen600" || r.OwnerTask <= 0 || !gpuUUID.MatchString(r.OwnerJob) || !gpuUUID.MatchString(r.SourceJob) {
		return errors.New("invalid fixed GPU request ownership/profile")
	}
	if (r.SourceSnapshotID != "" && (!gpuHash.MatchString(r.SourceSnapshotID) || !gpuHash.MatchString(r.SourceTreeSHA))) || (r.SourceTreeSHA != "" && !gpuHash.MatchString(r.SourceTreeSHA)) {
		return errors.New("invalid GPU source snapshot")
	}
	if r.Role != "builder" && r.Role != "reviewer" && r.Role != "auditor_a" && r.Role != "auditor_b" {
		return errors.New("unsupported GPU owner role")
	}
	for _, v := range []string{r.ExperimentRoot, r.SourceArchiveSHA, r.AdmissionSHA, r.AcceptanceSHA, r.RuntimeKey} {
		if !gpuHash.MatchString(v) {
			return errors.New("invalid GPU identity")
		}
	}
	if strings.TrimSpace(r.Script) == "" || strings.ContainsRune(r.Script, 0) || len(r.Script) > 128<<10 || len(r.Argv) > 32 || r.Trial < 0 || r.Trial >= 5 {
		return errors.New("GPU input/trial bound")
	}
	for _, a := range r.Argv {
		if len(a) > 4096 || strings.ContainsRune(a, 0) {
			return errors.New("GPU argument bound")
		}
	}
	return nil
}

// Reserve requires an independently authenticated qualification and fresh quota
// decision. Caller pins ExperimentRoot to the retained assignment lineage; a
// worker cannot choose a new root to reset the rolling resource ledger.
func (l *GPUResearchLedger) Reserve(r GPUResearchRequest, runtimeKey, qualificationSHA string, now time.Time, enabled, quotaAllowed bool) (*GPUResearchLease, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if !enabled || !quotaAllowed {
		return nil, errors.New("GPU launch disabled or quota reserve unavailable")
	}
	if runtimeKey != r.RuntimeKey || !gpuHash.MatchString(qualificationSHA) {
		return nil, errors.New("GPU runtime is not qualified")
	}
	if r.QualificationSHA != "" && r.QualificationSHA != qualificationSHA {
		return nil, errors.New("GPU qualification differs from request")
	}
	r.QualificationSHA = qualificationSHA
	if r.Argv == nil {
		r.Argv = []string{}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if len(raw) > 2<<20 {
		return nil, errors.New("GPU exact request exceeds2 MiB wire budget; reduce combined input")
	}
	requestSHA := gpuDigest(raw)
	id := gpuDigest([]byte(r.OwnerJob + ":" + requestSHA))
	if l.Runs == nil {
		l.Runs = map[string]*GPUResearchLease{}
	}
	if old := l.Runs[id]; old != nil {
		return old, nil
	}
	semantic := gpuSemantic(r)
	for _, old := range l.Runs {
		if gpuSemantic(old.Request) == semantic && old.Receipt != nil && old.Receipt.CleanupConfirmed && (old.Receipt.Executed == nil || *old.Receipt.Executed) {
			return old, nil
		}
	}
	var charged int64
	for _, old := range l.Runs {
		if old.Request.ExperimentRoot != r.ExperimentRoot {
			continue
		}
		if old.Receipt == nil || !old.Receipt.CleanupConfirmed {
			return nil, errors.New("prior GPU ownership must finish cleanup")
		}
		settled := old.SettledAt
		if settled.IsZero() {
			settled = old.ReservedAt
		}
		if settled.After(now.Add(-24 * time.Hour)) {
			charged += old.Receipt.ChargedMS
		}
	}
	if charged+600000 > gpuResearchLimitMS {
		return nil, errors.New("GPU rolling budget exhausted; retry after prior charges age out")
	}
	lease := &GPUResearchLease{SemanticSHA: semantic, ID: id, Request: r, RequestRaw: raw, RequestSHA: requestSHA, QualificationSHA: qualificationSHA, ReservedAt: now, MaxMS: 600000}
	l.Runs[id] = lease
	return lease, nil
}
func (l *GPUResearchLedger) Start(id string, now time.Time, enabled, quotaAllowed bool) error {
	run := l.Runs[id]
	if run == nil {
		return errors.New("unknown GPU run")
	}
	if !enabled || !quotaAllowed || run.StopRequested || run.Receipt != nil {
		return errors.New("GPU run cannot start")
	}
	if !run.StartedAt.IsZero() {
		return nil
	}
	run.StartedAt = now
	return nil
}
func (l *GPUResearchLedger) Cancel(id string) error {
	run := l.Runs[id]
	if run == nil {
		return errors.New("unknown GPU run")
	}
	run.StopRequested = true
	return nil
}
func (l *GPUResearchLedger) Record(receipt GPUResearchReceipt) error {
	return l.RecordAt(receipt, time.Now())
}
func (l *GPUResearchLedger) RecordAt(receipt GPUResearchReceipt, now time.Time) error {
	run := l.Runs[receipt.RunID]
	if run == nil || receipt.RequestSHA != run.RequestSHA || !gpuHash.MatchString(receipt.ReceiptSHA) {
		return errors.New("GPU receipt binding differs")
	}
	if !receipt.CleanupConfirmed {
		return errors.New("GPU ownership cleanup remains outstanding")
	}
	switch receipt.State {
	case "exited", "cancelled", "timeout", "output_limit", "interrupted", "unavailable":
	default:
		return errors.New("GPU receipt is not terminal")
	}
	if receipt.ElapsedMS < 0 || receipt.ChargedMS < 0 || receipt.ChargedMS > run.MaxMS {
		return errors.New("GPU charge exceeds reservation")
	}
	if receipt.Executed == nil {
		if receipt.ChargedMS != run.MaxMS || receipt.State == "exited" {
			return errors.New("uncertain GPU execution must retain full charge")
		}
	} else if !*receipt.Executed {
		if receipt.ChargedMS != 0 || receipt.State == "exited" {
			return errors.New("nonexecution cannot carry execution evidence")
		}
	} else {
		if !gpuHash.MatchString(receipt.OutputSHA) || receipt.ExitCode == nil {
			return errors.New("GPU execution evidence incomplete")
		}
		expected := receipt.ElapsedMS
		if expected > run.MaxMS {
			expected = run.MaxMS
		}
		if receipt.ChargedMS != expected {
			return errors.New("GPU billed charge differs from observed elapsed time")
		}
	}
	if run.Receipt != nil {
		a, _ := json.Marshal(run.Receipt)
		b, _ := json.Marshal(receipt)
		if string(a) != string(b) {
			return fmt.Errorf("GPU terminal receipt is immutable")
		}
		return nil
	}
	copied := receipt
	run.Receipt = &copied
	run.SettledAt = now
	return nil
}

// Stable execution identity excludes transport archives, observation timestamps
// and process UUIDs. Explicit trial ordinals permit deliberate repetitions.
func gpuSemantic(r GPUResearchRequest) string {
	source := r.SourceTreeSHA
	if source == "" {
		source = r.SourceArchiveSHA
	}
	args := r.Argv
	if args == nil {
		args = []string{}
	}
	raw, _ := json.Marshal(struct {
		Root, Role, Source, Acceptance, Runtime, Qualification, Profile, Script string
		Args                                                                    []string
		Trial                                                                   int
	}{r.ExperimentRoot, r.Role, source, r.AcceptanceSHA, r.RuntimeKey, r.QualificationSHA, r.Profile, r.Script, args, r.Trial})
	return gpuDigest(raw)
}
