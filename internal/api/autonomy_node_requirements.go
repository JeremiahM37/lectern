package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Worker selectors are data. Only the fixed isolated provisioner receives the
// registry socket; neither package lifecycle hooks nor probes run on the host.
type autoNodeRequest struct {
	SchemaVersion     int      `json:"schema_version"`
	Kind              string   `json:"kind"`
	Requirements      []string `json:"requirements,omitempty"`
	Modules           []string `json:"modules,omitempty"`
	Binaries          []string `json:"binaries,omitempty"`
	PackageJSON       string   `json:"package_json,omitempty"`
	PackageLock       string   `json:"package_lock,omitempty"`
	SourceJob         string   `json:"source_job"`
	SourceSHA         string   `json:"source_archive_sha256"`
	AdmissionSHA      string   `json:"admission_sha256"`
	ExpectedInputKey  string   `json:"expected_input_key,omitempty"`
	ExpectedBundleKey string   `json:"expected_bundle_key,omitempty"`
	ExpectedLockSHA   string   `json:"expected_lock_sha256,omitempty"`
}
type autoNodeReceipt struct {
	Generation        int    `json:"generation"`
	State             string `json:"state"`
	Capability        string `json:"capability"`
	Unsupported       bool   `json:"unsupported,omitempty"`
	Reason            string `json:"reason,omitempty"`
	Diagnostic        string `json:"diagnostic,omitempty"`
	SourceJob         string `json:"source_job"`
	SourceSHA         string `json:"source_archive_sha256"`
	AdmissionSHA      string `json:"admission_sha256"`
	InputKey          string `json:"input_key,omitempty"`
	BundleKey         string `json:"bundle_key,omitempty"`
	RuntimeDigest     string `json:"runtime_digest,omitempty"`
	LockSHA           string `json:"lock_sha256,omitempty"`
	ProbeSHA          string `json:"probe_sha256,omitempty"`
	SourceManifestSHA string `json:"source_manifest_sha256,omitempty"`
	SourceLockSHA     string `json:"source_lock_sha256,omitempty"`
}

var autoNodeBin = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func autoNodePath(p, base string) bool {
	if len(p) == 0 || len(p) > 512 || strings.ContainsAny(p, "\\\x00\r\n") || path.IsAbs(p) || path.Clean(p) != p || path.Base(p) != base {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." || s == "." || s == "" {
			return false
		}
	}
	return true
}
func autoNodeInputs(r autonomy.Requirement) (*autoNodeRequest, error) {
	if r.Capability != "node_packages" || r.SchemaVersion != 1 || r.Condition != "offline_node_available" || len(r.Imports) > 0 {
		return nil, errors.New("unsupported Node requirement capability, condition or selectors")
	}
	if len(r.Requirements) > 32 || len(r.Modules) > 32 || len(r.Binaries) > 32 {
		return nil, errors.New("Node selectors exceed bounded input")
	}
	out := &autoNodeRequest{SchemaVersion: 1, Kind: "node_packages", PackageJSON: r.PackageJSON, PackageLock: r.PackageLock}
	locked := r.PackageJSON != "" || r.PackageLock != ""
	if locked {
		if len(r.Requirements) > 0 || !autoNodePath(r.PackageJSON, "package.json") || !autoNodePath(r.PackageLock, "package-lock.json") || path.Dir(r.PackageJSON) != path.Dir(r.PackageLock) {
			return nil, errors.New("Node source manifests must be same-directory canonical archive-relative package.json/package-lock.json, exclusive with exact pins")
		}
	} else if len(r.Requirements) == 0 {
		return nil, errors.New("Node requires exact package pins or archived manifests")
	}
	pins := map[string]string{}
	for _, pin := range r.Requirements {
		at := strings.LastIndex(pin, "@")
		if at <= 0 {
			return nil, errors.New("Node pins require exact name@version")
		}
		name, v := pin[:at], pin[at+1:]
		if len(name) > 214 || len(v) > 128 || !autoNodeName.MatchString(name) || !autoNodeVersion.MatchString(v) {
			return nil, errors.New("Node pins require canonical public npm names and exact semantic versions")
		}
		if old, ok := pins[name]; ok && old != v {
			return nil, fmt.Errorf("conflicting Node versions for %s", name)
		}
		pins[name] = v
	}
	for n, v := range pins {
		out.Requirements = append(out.Requirements, n+"@"+v)
	}
	sort.Strings(out.Requirements)
	normalize := func(values []string, module bool) ([]string, error) {
		seen := map[string]bool{}
		res := []string{}
		for _, v := range values {
			valid := autoNodeBin.MatchString(v)
			if module {
				valid = len(v) <= 256 && !strings.ContainsAny(v, "\\\x00\r\n:") && path.Clean(v) == v && !path.IsAbs(v)
				parts := strings.Split(v, "/")
				name := parts[0]
				offset := 1
				if strings.HasPrefix(v, "@") {
					if len(parts) < 2 {
						return nil, errors.New("invalid scoped Node module")
					}
					name = strings.Join(parts[:2], "/")
					offset = 2
				}
				valid = valid && len(name) <= 214 && autoNodeName.MatchString(name)
				for _, part := range parts[offset:] {
					valid = valid && part != ".." && part != "." && autoNodeBin.MatchString(part)
				}
			}
			if !valid {
				return nil, errors.New("invalid Node module/binary verification selector")
			}
			if !seen[v] {
				res = append(res, v)
				seen[v] = true
			}
		}
		sort.Strings(res)
		return res, nil
	}
	var err error
	out.Modules, err = normalize(r.Modules, true)
	if err != nil {
		return nil, err
	}
	out.Binaries, err = normalize(r.Binaries, false)
	if err != nil {
		return nil, err
	}
	if len(out.Modules)+len(out.Binaries) == 0 {
		return nil, errors.New("Node delivery requires at least one module or binary probe")
	}
	if len(store.J(out)) > 12<<10 {
		return nil, errors.New("Node requirement exceeds bounded envelope")
	}
	return out, nil
}
func autoNodeSemanticKey(r *autoNodeRequest) string {
	v := *r
	v.SourceJob = ""
	v.SourceSHA = ""
	v.AdmissionSHA = ""
	v.ExpectedInputKey = ""
	v.ExpectedBundleKey = ""
	v.ExpectedLockSHA = ""
	// Archive identity is required until trusted extraction supplies exact source
	// bytes. A path alone cannot group different projects' lockfiles.
	if v.PackageLock != "" {
		v.SourceSHA = r.SourceSHA
	}
	return autoSHA([]byte(store.J(v)))
}
func autoNodeBound(r *autoNodeRequest, p autoNodeReceipt) bool {
	return r != nil && p.Capability == "node_packages" && p.SourceJob == r.SourceJob && p.SourceSHA == r.SourceSHA && p.AdmissionSHA == r.AdmissionSHA
}
func autoNodePending(j *autoJob) bool {
	return j.NodeRequest != nil && !j.NodeStopped && (j.NodeStopRequested || j.Status == "prepared" || j.Status == "deferred")
}
func autoWriteNodeRequestAt(root string, j *autoJob) error {
	if j.NodeRequest == nil {
		return errors.New("missing Node request")
	}
	r := *j.NodeRequest
	r.ExpectedInputKey = j.NodeExpectedInput
	r.ExpectedBundleKey = j.NodeExpectedBundle
	r.ExpectedLockSHA = j.NodeExpectedLock
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(raw) > 16<<10 {
		return errors.New("Node request envelope exceeds limit")
	}
	dest := filepath.Join(root, j.ID, "node-requirement.json")
	if old, e := os.ReadFile(dest); e == nil {
		if string(old) != string(raw) {
			return errors.New("Node requirement intent changed")
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(dest), ".node-requirement-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if _, e = f.Write(raw); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), dest)
}
func autoApplyNodeReceipt(a *autoRecord, j *autoJob, raw []byte) (bool, error) {
	var p autoNodeReceipt
	if json.Unmarshal(raw, &p) != nil || !autoNodeBound(j.NodeRequest, p) || p.Generation != autoNodeGeneration(j) {
		return false, errors.New("Node prerequisite receipt binding mismatch")
	}
	if len(p.Diagnostic) > 16<<10 || len(p.Reason) > 2000 {
		return false, errors.New("Node diagnostic exceeds bound")
	}
	switch p.State {
	case "checking", "recovering", "waiting", "unavailable", "failed", "verified":
	default:
		return false, errors.New("invalid Node prerequisite state")
	}
	ready := p.State == "verified"
	if ready {
		for _, v := range []string{p.InputKey, p.BundleKey, p.RuntimeDigest, p.LockSHA, p.ProbeSHA} {
			if !autoHash256(v) {
				return false, errors.New("Node verification identity missing")
			}
		}
		if j.NodeRequest.PackageLock != "" && (!autoHash256(p.SourceManifestSHA) || !autoHash256(p.SourceLockSHA)) {
			return false, errors.New("Node source manifest identity missing")
		}
		if (j.NodeExpectedInput != "" && j.NodeExpectedInput != p.InputKey) || (j.NodeExpectedBundle != "" && j.NodeExpectedBundle != p.BundleKey) || (j.NodeExpectedLock != "" && j.NodeExpectedLock != p.LockSHA) {
			ready = false
			p.State = "unavailable"
			p.Unsupported = true
			p.Reason = "Inherited Node runtime/lock changed; independent review must use the exact tested environment"
		}
		if ready && j.NodeNeedsChange && p.BundleKey == j.NodePreviousBundle {
			ready = false
			p.State = "unavailable"
			p.Unsupported = true
			p.Reason = "The verified Node environment is unchanged from the blocked worker; investigate actual failure"
		}
	}
	j.NodeRecovery = &p
	if p.State == "unavailable" && p.Unsupported {
		j.RequirementHold = true
	}
	if ready {
		j.NodeNeedsChange = false
	}
	for _, id := range j.RequirementIDs {
		if e := a.Requirements[id]; e != nil && e.Request.Capability == "node_packages" {
			if e.NodeEnvironments == nil {
				e.NodeEnvironments = map[string]*autoNodeReceipt{}
			}
			if p.InputKey != "" {
				e.NodeEnvironments[p.InputKey] = &p
			}
			e.NodeReceipt = &p
			e.State = p.State
			e.Reason = p.Reason
			e.RecoveryJob = j.ID
			e.UpdatedAt = time.Now()
		}
	}
	if !ready {
		a.Status = "recovering_prerequisite"
		a.Reason = p.Reason
	}
	return ready, nil
}
func (s *Server) recoverAutoNode(ctx context.Context, a *autoRecord, j *autoJob) (bool, error) {
	if j.RequirementHold {
		return false, nil
	}
	if j.NodeRequest == nil || (j.Status != "prepared" && j.Status != "deferred") {
		return true, nil
	}
	if !a.Config.Enabled {
		return false, nil
	}
	if j.NodeStopRequested && !j.NodeStopped {
		if e := s.stopAutoNode(ctx, a, j); e != nil {
			return false, e
		}
		return false, nil
	}
	if j.NodeStopped {
		j.NodeGeneration = autoNodeGeneration(j) + 1
		j.NodeStopped = false
		j.NodeStopRequested = false
	} else if j.NodeGeneration == 0 {
		j.NodeGeneration = 1
	}
	if e := s.saveAuto(a); e != nil {
		return false, e
	}
	if e := autoWriteNodeRequestAt(autoRoot, j); e != nil {
		return false, e
	}
	if e := s.ensureAutoBridges(j); e != nil {
		return false, e
	}
	raw, e := s.runAutoCommand(ctx, "node-dependencies", "--job", j.ID, "--generation", strconv.Itoa(j.NodeGeneration))
	if e != nil {
		return false, e
	}
	ready, e := autoApplyNodeReceipt(a, j, raw)
	if e != nil {
		return false, e
	}
	if j.RequirementHold && j.NodeRecovery.Unsupported {
		if e = s.recordAutoNodeProvisionerRequirement(a, j); e != nil {
			return false, e
		}
	}
	return ready, s.saveAuto(a)
}
func autoPrepareNodeResume(old, j *autoJob) {
	j.NodeNeedsResume = false
	j.NodeGeneration = 1
	j.NodeStopRequested = false
	j.NodeStopped = false
	if old.NodeRecovery != nil && old.NodeRecovery.State == "verified" {
		j.NodeExpectedInput = old.NodeRecovery.InputKey
		j.NodeExpectedBundle = old.NodeRecovery.BundleKey
		j.NodeExpectedLock = old.NodeRecovery.LockSHA
	}
	if old.PendingNodeRequest != nil {
		r := *old.PendingNodeRequest
		j.NodeRequest = &r
		j.PendingNodeRequest = nil
		j.NodePreviousBundle = old.NodeUsedBundle
		if j.NodePreviousBundle == "" {
			j.NodePreviousBundle = old.NodePreviousBundle
		}
		if j.NodePreviousBundle == "" && old.NodeRecovery != nil && old.NodeRecovery.State == "verified" {
			j.NodePreviousBundle = old.NodeRecovery.BundleKey
		}
		j.NodeExpectedInput = ""
		j.NodeExpectedBundle = ""
		j.NodeExpectedLock = ""
		j.NodeNeedsChange = true
		j.NodeRecovery = nil
	}
}
func autoInheritNodeFrom(j, source *autoJob) {
	if j.NodeRequest != nil || source == nil || source.NodeRequest == nil || source.NodeRecovery == nil || source.NodeRecovery.State != "verified" {
		return
	}
	r := *source.NodeRequest
	j.NodeRequest = &r
	j.NodeRecovery = nil
	j.NodeExpectedInput = source.NodeRecovery.InputKey
	j.NodeExpectedBundle = source.NodeRecovery.BundleKey
	j.NodeExpectedLock = source.NodeRecovery.LockSHA
}
func (s *Server) recordAutoNodeProvisionerRequirement(a *autoRecord, j *autoJob) error {
	if j.NodeRecovery == nil || !j.NodeRecovery.Unsupported || j.NodeRecovery.State != "unavailable" || !autoNodeBound(j.NodeRequest, *j.NodeRecovery) {
		return errors.New("unbound Node prerequisite observation")
	}
	task, e := s.DB.Task(j.TaskID)
	if e != nil {
		return e
	}
	request := autonomy.Requirement{Capability: "node_packages", SchemaVersion: 1, Condition: "offline_node_available", Requirements: j.NodeRequest.Requirements, Modules: j.NodeRequest.Modules, Binaries: j.NodeRequest.Binaries, PackageJSON: j.NodeRequest.PackageJSON, PackageLock: j.NodeRequest.PackageLock, Evidence: []string{"Trusted isolated Node provisioner could not deliver retained environment"}}
	key := autoRequirementKey(request)
	if request.PackageLock != "" {
		key = autoSHA([]byte(key + ":" + j.NodeRequest.SourceSHA))
	}
	if a.Requirements == nil {
		a.Requirements = map[string]*autoRequirement{}
	}
	entry := a.Requirements[key]
	if entry == nil {
		if len(a.Requirements) >= 512 {
			return errors.New("prerequisite ledger capacity reached")
		}
		entry = &autoRequirement{Key: key, Request: request}
		a.Requirements[key] = entry
	}
	found := false
	for _, id := range j.RequirementIDs {
		found = found || id == key
	}
	if !found {
		j.RequirementIDs = append(j.RequirementIDs, key)
	}
	found = false
	for _, o := range entry.Occurrences {
		found = found || o.JobID == j.ID
	}
	if !found {
		entry.Occurrences = append(entry.Occurrences, autoRequirementOccurrence{TaskID: j.TaskID, RootTaskID: autoRepairRoot(a, j.TaskID), ProjectID: task.ProjectID, JobID: j.ID, EvidenceJob: j.NodeRequest.SourceJob, ArchiveSHA: j.NodeRequest.SourceSHA, Evidence: request.Evidence})
	}
	entry.State = "unavailable"
	entry.Reason = j.NodeRecovery.Reason
	entry.NodeReceipt = j.NodeRecovery
	entry.RecoveryJob = j.ID
	entry.UpdatedAt = time.Now()
	autoRefuteDiagnosisRemedy(a, j)
	return nil
}

func autoMergeNodeRequests(old *autoNodeRequest, inputs []*autoNodeRequest) (*autoNodeRequest, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	merged := *inputs[0]
	merged.Requirements = append([]string(nil), merged.Requirements...)
	merged.Modules = append([]string(nil), merged.Modules...)
	merged.Binaries = append([]string(nil), merged.Binaries...)
	for _, r := range inputs[1:] {
		if r.PackageJSON != merged.PackageJSON || r.PackageLock != merged.PackageLock {
			return nil, errors.New("one retained assignment needs one unambiguous Node source lock")
		}
		merged.Requirements = append(merged.Requirements, r.Requirements...)
		merged.Modules = append(merged.Modules, r.Modules...)
		merged.Binaries = append(merged.Binaries, r.Binaries...)
	}
	if old != nil && old.PackageLock == "" && merged.PackageLock == "" {
		merged.Requirements = append(merged.Requirements, old.Requirements...)
		merged.Modules = append(merged.Modules, old.Modules...)
		merged.Binaries = append(merged.Binaries, old.Binaries...)
	}
	return autoNodeInputs(autonomy.Requirement{Capability: "node_packages", SchemaVersion: 1, Condition: "offline_node_available", Requirements: merged.Requirements, Modules: merged.Modules, Binaries: merged.Binaries, PackageJSON: merged.PackageJSON, PackageLock: merged.PackageLock})
}
func autoRequirementAdmissionSHA(a *autoRecord, j *autoJob) string {
	admission := j.Admission
	if admission == nil && a.State != nil {
		for i := len(a.State.Assignments) - 1; i >= 0; i-- {
			as := a.State.Assignments[i]
			if as.Role == "builder" && as.Item == a.State.Item && as.Step == a.State.Step {
				if b := autoFindJob(a, as.TaskID); b != nil {
					admission = b.Admission
				}
				break
			}
		}
	}
	binding := store.J(admission)
	if admission == nil {
		binding = store.J(struct {
			Task  int64
			State *autonomy.State
		}{j.TaskID, a.State})
	}
	return autoSHA([]byte(binding))
}
func autoNodeGeneration(j *autoJob) int {
	if j.NodeGeneration <= 0 {
		return 1
	}
	return j.NodeGeneration
}
func (s *Server) stopAutoNode(ctx context.Context, a *autoRecord, j *autoJob) error {
	if j.NodeGeneration == 0 {
		j.NodeGeneration = 1
	}
	j.NodeStopRequested = true
	if e := s.saveAuto(a); e != nil {
		return e
	}
	raw, e := s.runAutoCommand(ctx, "node-dependencies-stop", "--job", j.ID, "--generation", strconv.Itoa(j.NodeGeneration))
	if e != nil {
		return e
	}
	var r struct {
		State      string `json:"state"`
		Job        string `json:"job"`
		Generation int    `json:"generation"`
	}
	if json.Unmarshal(raw, &r) != nil || r.State != "stopped" || r.Job != j.ID || r.Generation != j.NodeGeneration {
		return errors.New("Node provisioner stop not yet confirmed")
	}
	j.NodeStopped = true
	return s.saveAuto(a)
}
