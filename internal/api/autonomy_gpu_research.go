package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// Worker input contains experiment code only. Source, admission, runtime and
// budget lineage are selected from controller-owned records before reservation.
type autoGPUExperimentInput struct {
	Script      string   `json:"script"`
	Argv        []string `json:"argv"`
	Trial       int      `json:"trial"`
	SourcePaths []string `json:"source_paths"`
}
type autoGPUQualifiedRuntime struct {
	RuntimeKey string `json:"runtime_key"`
	ReceiptSHA string `json:"receipt_sha256"`
}

// autoGPUAdmittedRequest consumes an already frozen source identity. Callers
// must obtain it through the asynchronous archive path, never hash mutable work
// or hold autoMu while exporting. It does not itself grant launch authority.
func autoGPUAdmittedRequest(a *autoRecord, j *autoJob, source *autoJob, archiveSHA string, runtime autoGPUQualifiedRuntime, input autoGPUExperimentInput) (autonomy.GPUResearchRequest, error) {
	empty := autonomy.GPUResearchRequest{}
	normalized, normalizeErr := autoGPUNormalizeInput(input)
	if normalizeErr != nil {
		return empty, normalizeErr
	}
	input = normalized
	if a == nil || j == nil || source == nil || j.Admission == nil || j.Admission.TaskID <= 0 || j.Admission.Proposal.ProjectID <= 0 || len(j.Admission.Proposal.Acceptance) == 0 {
		return empty, errors.New("GPU experiment requires a retained audited assignment")
	}
	if j.Role != "builder" && j.Role != "reviewer" {
		return empty, errors.New("GPU pre-admission audit experiments require a separately pinned audit contract")
	}
	if j.Status != "running" {
		return empty, errors.New("GPU owner is not an active assignment")
	}
	if source.Status != "done" && source.Status != "stopped" && source.Status != "failed" {
		return empty, errors.New("GPU source must be an immutable archived version")
	}
	if source.TaskID != j.TaskID && (source.Admission == nil || source.Admission.TaskID != j.Admission.TaskID) {
		return empty, errors.New("GPU source does not belong to the retained assignment")
	}
	if !autoHash256(archiveSHA) || !autoHash256(runtime.RuntimeKey) || !autoHash256(runtime.ReceiptSHA) {
		return empty, errors.New("GPU source or qualified runtime identity missing")
	}
	root := ""
	if j.PrivateIntegrationRoot != "" {
		root = "private:" + j.PrivateIntegrationRoot
	} else {
		task := autoRepairRoot(a, j.Admission.TaskID)
		if task <= 0 {
			return empty, errors.New("GPU retained assignment lineage unavailable")
		}
		root = fmt.Sprintf("task:%d", task)
	}
	admission, _ := json.Marshal(j.Admission)
	acceptance, _ := json.Marshal(j.Admission.Proposal.Acceptance)
	r := autonomy.GPUResearchRequest{SchemaVersion: 1, OwnerJob: j.ID, OwnerTask: j.TaskID, Role: j.Role, ExperimentRoot: autoSHA([]byte(fmt.Sprintf("gpu/project:%d/%s", j.Admission.Proposal.ProjectID, root))), SourceJob: source.ID, SourceArchiveSHA: archiveSHA, AdmissionSHA: autoSHA(admission), AcceptanceSHA: autoSHA(acceptance), Target: autonomy.GPUResearchTarget, RuntimeKey: runtime.RuntimeKey, QualificationSHA: runtime.ReceiptSHA, Profile: "gpu-screen600", Script: input.Script, Argv: input.Argv, Trial: input.Trial}
	if r.Argv == nil {
		r.Argv = []string{}
	}
	return r, r.Validate()
}

// The root helper's stdout is an authenticated transport, but it still must
// describe this exact reserved request. Caller stores raw evidence separately.
func autoGPUDecodeReceipt(raw []byte, lease *autonomy.GPUResearchLease) (autonomy.GPUResearchReceipt, error) {
	var receipt autonomy.GPUResearchReceipt
	var envelope struct {
		Owner    string          `json:"owner_job"`
		Executed json.RawMessage `json:"executed"`
		Charge   *int64          `json:"charged_ms"`
		Cleanup  *bool           `json:"cleanup_confirmed"`
	}
	if lease == nil || len(raw) > 65536 || json.Unmarshal(raw, &receipt) != nil || json.Unmarshal(raw, &envelope) != nil || envelope.Owner != lease.Request.OwnerJob || receipt.RunID != lease.ID || receipt.RequestSHA != lease.RequestSHA || len(envelope.Executed) == 0 || envelope.Charge == nil || envelope.Cleanup == nil {
		return receipt, errors.New("GPU terminal receipt ownership/accounting differs")
	}
	detached := *lease
	detached.Receipt = nil
	check := autonomy.GPUResearchLedger{Runs: map[string]*autonomy.GPUResearchLease{lease.ID: &detached}}
	if err := check.Record(receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

type autoGPUCapturedSource struct {
	State      string `json:"state"`
	SnapshotID string `json:"snapshot_id"`
	OwnerJob   string `json:"owner_job"`
	InputSHA   string `json:"input_sha256"`
	ArchiveSHA string `json:"source_archive_sha256"`
	TreeSHA    string `json:"source_tree_sha256"`
	ReceiptSHA string `json:"receipt_sha256"`
	StartedNS  int64  `json:"capture_started_unix_ns"`
	FinishedNS int64  `json:"capture_finished_unix_ns"`
}

func autoGPUInputSHA(input autoGPUExperimentInput, runtime autoGPUQualifiedRuntime) string {
	if normalized, err := autoGPUNormalizeInput(input); err == nil {
		input = normalized
	}
	if input.Argv == nil {
		input.Argv = []string{}
	}
	raw, _ := json.Marshal(struct {
		Input   autoGPUExperimentInput  `json:"input"`
		Runtime autoGPUQualifiedRuntime `json:"runtime"`
	}{input, runtime})
	return autoSHA(raw)
}
func autoGPUCapturedRequest(a *autoRecord, j *autoJob, snapshotID string, capture autoGPUCapturedSource, runtime autoGPUQualifiedRuntime, input autoGPUExperimentInput) (autonomy.GPUResearchRequest, error) {
	if j == nil || capture.State != "ready" || capture.OwnerJob != j.ID || capture.SnapshotID != snapshotID || !autoHash256(snapshotID) || capture.InputSHA != autoGPUInputSHA(input, runtime) || !autoHash256(capture.ArchiveSHA) || !autoHash256(capture.TreeSHA) || !autoHash256(capture.ReceiptSHA) || capture.StartedNS <= 0 || capture.FinishedNS < capture.StartedNS {
		return autonomy.GPUResearchRequest{}, errors.New("GPU captured source binding differs")
	}
	// Detached metadata only: never mutate the running owner's state. The
	// trusted snapshot receipt, rather than source.Status, proves frozen bytes.
	source := *j
	source.Status = "stopped"
	request, err := autoGPUAdmittedRequest(a, j, &source, capture.ArchiveSHA, runtime, input)
	if err != nil {
		return request, err
	}
	request.SourceSnapshotID = snapshotID
	request.SourceTreeSHA = capture.TreeSHA
	return request, request.Validate()
}

func autoGPUNormalizeInput(input autoGPUExperimentInput) (autoGPUExperimentInput, error) {
	if input.Argv == nil {
		input.Argv = []string{}
	}
	if len(input.SourcePaths) == 0 {
		input.SourcePaths = []string{"."}
	}
	if len(input.SourcePaths) > 64 {
		return input, errors.New("GPU source selection exceeds64 paths")
	}
	input.SourcePaths = append([]string{}, input.SourcePaths...)
	sort.Strings(input.SourcePaths)
	for i, p := range input.SourcePaths {
		if p == "" || len(p) > 1024 || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00\n\r") {
			return input, errors.New("GPU source selection must use canonical relative paths")
		}
		if p != "." {
			for _, part := range strings.Split(p, "/") {
				if part == "" || part == "." || part == ".." {
					return input, errors.New("GPU source selection path escapes or is not canonical")
				}
			}
		}
		for _, prior := range input.SourcePaths[:i] {
			if p == prior || prior == "." || strings.HasPrefix(p, prior+"/") {
				return input, errors.New("GPU source selections overlap")
			}
		}
	}
	if strings.TrimSpace(input.Script) == "" || len(input.Script) > 128<<10 || strings.ContainsRune(input.Script, 0) || len(input.Argv) > 32 || input.Trial < 0 || input.Trial >= 5 {
		return input, errors.New("GPU experiment input bound")
	}
	for _, arg := range input.Argv {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return input, errors.New("GPU argument bound")
		}
	}
	return input, nil
}
