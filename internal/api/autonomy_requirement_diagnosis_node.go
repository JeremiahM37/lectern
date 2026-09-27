package api

import (
	"encoding/json"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

type autoDiagnosisRecipe struct {
	Python *autoPythonRequest `json:"python,omitempty"`
	Node   *autoNodeRequest   `json:"node,omitempty"`
}

func autoDiagnosisRecipeFor(requirements []autonomy.Requirement) (autoDiagnosisRecipe, bool, error) {
	var out autoDiagnosisRecipe
	var py []autonomy.Requirement
	var nodes []*autoNodeRequest
	for _, r := range requirements {
		switch r.Capability {
		case "python_wheels":
			py = append(py, r)
		case "node_packages":
			n, err := autoNodeInputs(r)
			if err != nil {
				return out, false, err
			}
			nodes = append(nodes, n)
		default:
			return out, false, nil
		}
	}
	if len(py) > 0 {
		p, m, ok, err := autoDiagnosisRemedy(py)
		if err != nil || !ok {
			return out, false, err
		}
		out.Python = &autoPythonRequest{SchemaVersion: 1, Kind: "python_wheels", Requirements: p, Imports: m}
	}
	if len(nodes) > 0 {
		n, err := autoMergeNodeRequests(nil, nodes)
		if err != nil {
			return out, false, err
		}
		out.Node = n
	}
	return out, out.Python != nil || out.Node != nil, nil
}
func autoDiagnosisNodeUnavailable(j *autoJob) bool {
	return j.NodeRequest != nil && j.NodeRecovery != nil && j.NodeRecovery.State == "unavailable" && j.NodeRecovery.Unsupported && autoNodeBound(j.NodeRequest, *j.NodeRecovery)
}
func autoDiagnosisPreflightUnavailable(j *autoJob) bool {
	return autoDiagnosisPythonUnavailable(j) || autoDiagnosisNodeUnavailable(j)
}
func autoDiagnosisUnavailableSource(j *autoJob, capability string) (string, string, bool) {
	if capability == "node_packages" && autoDiagnosisNodeUnavailable(j) {
		return j.NodeRequest.SourceJob, j.NodeRequest.SourceSHA, true
	}
	if (capability == "python_wheels" || !autoDiagnosisNodeUnavailable(j)) && autoDiagnosisPythonUnavailable(j) {
		return j.PythonRequest.SourceJob, j.PythonRequest.SourceSHA, true
	}
	if autoDiagnosisNodeUnavailable(j) && !autoDiagnosisPythonUnavailable(j) {
		return j.NodeRequest.SourceJob, j.NodeRequest.SourceSHA, true
	}
	return "", "", false
}
func autoRefuteNodeDiagnosisRemedy(a *autoRecord, j *autoJob) {
	if !j.NodeNeedsChange || !autoDiagnosisNodeUnavailable(j) {
		return
	}
	for _, key := range j.RequirementIDs {
		d := a.RequirementDiagnoses[autoDiagnosisID(key, j.TaskID)]
		if d == nil || d.Outcome != "completed" || d.AttemptedNodeRequest == nil || store.J(d.AttemptedNodeRequest) != store.J(j.NodeRequest) {
			continue
		}
		f := autoDiagnosisFailure{PreviousTargetJob: d.TargetJob, OwnerJob: j.ID, EvidenceJob: j.NodeRequest.SourceJob, ArchiveSHA: j.NodeRequest.SourceSHA, AdmissionSHA: autoDiagnosisAdmission(j)}
		_ = json.Unmarshal([]byte(store.J(j.NodeRequest)), &f.NodeRequest)
		_ = json.Unmarshal([]byte(store.J(j.NodeRecovery)), &f.NodeReceipt)
		f.EvidenceSHA = autoSHA([]byte(store.J(f)))
		d.Failures = append(d.Failures, f)
		d.TargetJob, d.TargetEvidenceJob, d.TargetArchiveSHA, d.TargetAdmissionSHA = j.ID, f.EvidenceJob, f.ArchiveSHA, f.AdmissionSHA
		d.Outcome = "remedy_failed"
		d.Reason = "Trusted Node provisioner could not verify the independently reviewed remedy: " + j.NodeRecovery.Reason
	}
}

// Provisioner failure may add a typed sibling for the exact attempted recipe.
// Its immutable failure record makes that sibling part of the same correction,
// rather than demanding a second investigation of the identical failed request.
func autoDiagnosisRefutedSibling(a *autoRecord, j *autoJob, r autonomy.Requirement) bool {
	recipe, ok, err := autoDiagnosisRecipeFor([]autonomy.Requirement{r})
	if err != nil || !ok {
		return false
	}
	for _, key := range j.RequirementIDs {
		d := a.RequirementDiagnoses[autoDiagnosisID(key, j.TaskID)]
		if d == nil || d.TargetJob != j.ID || d.Outcome != "completed" || len(d.Failures) == 0 {
			continue
		}
		f := d.Failures[len(d.Failures)-1]
		sha := f.EvidenceSHA
		f.EvidenceSHA = ""
		if sha != autoSHA([]byte(store.J(f))) || f.OwnerJob != j.ID {
			continue
		}
		if recipe.Python != nil && d.Remedy != nil {
			if store.J(recipe.Python.Requirements) == store.J(f.Request.Requirements) && store.J(recipe.Python.Imports) == store.J(f.Request.Imports) {
				return true
			}
		}
		if recipe.Node != nil && d.NodeRemedy != nil && f.NodeRequest != nil {
			n := *f.NodeRequest
			n.SourceJob = ""
			n.SourceSHA = ""
			n.AdmissionSHA = ""
			n.ExpectedInputKey = ""
			n.ExpectedBundleKey = ""
			n.ExpectedLockSHA = ""
			if store.J(recipe.Node) == store.J(&n) {
				return true
			}
		}
	}
	return false
}

// All sibling blockers are accounted for before either capability is released.
// Reviewed replacements override only matching direct pins; sealed prior
// requests and receipts are never modified.
func autoDiagnosisCombinedRecipe(a *autoRecord, target *autoJob) (autoDiagnosisRecipe, bool) {
	var out autoDiagnosisRecipe
	var inherited, reviewed []autoDiagnosisRecipe
	inherited = append(inherited, autoDiagnosisRecipe{target.PythonRequest, target.NodeRequest})
	var binding *autoRequirementDiagnosis
	needPython, needNode := false, false
	for _, key := range target.RequirementIDs {
		if d := a.RequirementDiagnoses[autoDiagnosisID(key, target.TaskID)]; d != nil && d.TargetJob == target.ID && d.Outcome == "completed" && (d.Remedy != nil || d.NodeRemedy != nil) {
			reviewed = append(reviewed, autoDiagnosisRecipe{d.Remedy, d.NodeRemedy})
			needPython = needPython || d.Remedy != nil
			needNode = needNode || d.NodeRemedy != nil
			binding = d
			continue
		}
		r := a.Requirements[key]
		if r == nil {
			return out, false
		}
		// A deterministic failed sibling needs its own reviewed remedy. Merely
		// repeating its rejected exact pins is not resolution.
		if r.State == "unavailable" || r.State == "unsupported" || r.State == "diagnosis_rejected" || r.State == "diagnosed_unavailable" {
			if autoDiagnosisRefutedSibling(a, target, r.Request) {
				continue
			}
			return out, false
		}
		recipe, ok, err := autoDiagnosisRecipeFor([]autonomy.Requirement{r.Request})
		if err != nil || !ok {
			return out, false
		}
		inherited = append(inherited, recipe)
		needPython = needPython || recipe.Python != nil
		needNode = needNode || recipe.Node != nil
	}
	if binding == nil {
		return out, false
	}
	// A reviewed Node recipe can replace a mistaken probe or deliberately switch
	// between direct pins and a different immutable archived lock. Keep unrelated
	// inherited direct pins, but do not perpetuate the refuted probe itself.
	var reviewedNodes []*autoNodeRequest
	for _, recipe := range reviewed {
		if recipe.Node != nil {
			reviewedNodes = append(reviewedNodes, recipe.Node)
		}
	}
	if len(reviewedNodes) > 0 && inherited[0].Node != nil {
		merged, err := autoMergeNodeRequests(nil, reviewedNodes)
		if err != nil {
			return out, false
		}
		old := *inherited[0].Node
		if old.PackageLock != "" || merged.PackageLock != "" {
			inherited[0].Node = nil
		} else {
			old.Modules = nil
			old.Binaries = nil
			inherited[0].Node = &old
		}
	}
	pyPins, nodePins := map[string]string{}, map[string]string{}
	var imports, modules, binaries []string
	var nodeLock, nodeJSON string
	hasPy, hasNode := false, false
	for _, recipes := range [][]autoDiagnosisRecipe{inherited, reviewed} {
		seenPy, seenNode := map[string]string{}, map[string]string{}
		for _, recipe := range recipes {
			if p := recipe.Python; p != nil {
				hasPy = true
				imports = append(imports, p.Imports...)
				for _, pin := range p.Requirements {
					name := strings.SplitN(pin, "==", 2)[0]
					if old := seenPy[name]; old != "" && old != pin {
						return out, false
					}
					seenPy[name] = pin
					pyPins[name] = pin
				}
			}
			if n := recipe.Node; n != nil {
				hasNode = true
				modules = append(modules, n.Modules...)
				binaries = append(binaries, n.Binaries...)
				if n.PackageLock != "" {
					if len(nodePins) > 0 || nodeLock != "" && (nodeLock != n.PackageLock || nodeJSON != n.PackageJSON) {
						return out, false
					}
					nodeLock, nodeJSON = n.PackageLock, n.PackageJSON
				}
				for _, pin := range n.Requirements {
					if nodeLock != "" {
						return out, false
					}
					name := pin[:strings.LastIndex(pin, "@")]
					if old := seenNode[name]; old != "" && old != pin {
						return out, false
					}
					seenNode[name] = pin
					nodePins[name] = pin
				}
			}
		}
	}
	if hasPy && needPython {
		pins := []string{}
		for _, pin := range pyPins {
			pins = append(pins, pin)
		}
		p, m, err := autoPythonInputs(pins, imports)
		if err != nil {
			return out, false
		}
		out.Python = &autoPythonRequest{SchemaVersion: 1, Kind: "python_wheels", Requirements: p, Imports: m, SourceJob: binding.TargetEvidenceJob, SourceSHA: binding.TargetArchiveSHA, AdmissionSHA: binding.TargetAdmissionSHA}
	}
	if hasNode && needNode {
		pins := []string{}
		for _, pin := range nodePins {
			pins = append(pins, pin)
		}
		n, err := autoNodeInputs(autonomy.Requirement{SchemaVersion: 1, Capability: "node_packages", Condition: "offline_node_available", Requirements: pins, Modules: modules, Binaries: binaries, PackageJSON: nodeJSON, PackageLock: nodeLock})
		if err != nil {
			return out, false
		}
		n.SourceJob, n.SourceSHA, n.AdmissionSHA = binding.TargetEvidenceJob, binding.TargetArchiveSHA, binding.TargetAdmissionSHA
		out.Node = n
	}
	return out, true
}

func autoNodeDiagnosisEnvironmentUsed(j *autoJob, request *autoNodeRequest, receipt *autoNodeReceipt) bool {
	if j == nil || request == nil || receipt == nil || receipt.Generation != autoNodeGeneration(j) {
		return false
	}
	if request.PackageLock != "" && (!autoHash256(receipt.SourceManifestSHA) || !autoHash256(receipt.SourceLockSHA)) {
		return false
	}
	return j != nil && request != nil && receipt != nil && receipt.State == "verified" && receipt.Capability == "node_packages" && autoHash256(receipt.InputKey) && autoHash256(receipt.BundleKey) && autoHash256(receipt.RuntimeDigest) && autoHash256(receipt.LockSHA) && autoHash256(receipt.ProbeSHA) && j.NodeUsedBundle == receipt.BundleKey && autoNodeBound(request, *receipt) && store.J(j.NodeRequest) == store.J(request) && store.J(j.NodeRecovery) == store.J(receipt)
}
func autoSameNodeEnvironment(b, r *autoNodeReceipt) bool {
	return b != nil && r != nil && b.InputKey == r.InputKey && b.BundleKey == r.BundleKey && b.RuntimeDigest == r.RuntimeDigest && b.LockSHA == r.LockSHA
}
func autoDiagnosisNodeUsedRequirements(j *autoJob, n *autoNodeRequest) bool {
	if j == nil || !autoNodeDiagnosisEnvironmentUsed(j, j.NodeRequest, j.NodeRecovery) {
		return false
	}
	got := j.NodeRequest
	if n.PackageJSON != got.PackageJSON || n.PackageLock != got.PackageLock {
		return false
	}
	contains := func(have, want []string) bool {
		m := map[string]bool{}
		for _, x := range have {
			m[x] = true
		}
		for _, x := range want {
			if !m[x] {
				return false
			}
		}
		return true
	}
	return contains(got.Requirements, n.Requirements) && contains(got.Modules, n.Modules) && contains(got.Binaries, n.Binaries)
}
func autoApplyNodeDiagnosisEnvironment(j *autoJob, e *autoVerifiedDiagnosisEnvironment) error {
	if e.NodeRequest == nil {
		return nil
	}
	if j.NodeRequest != nil {
		return nil
	}
	_ = json.Unmarshal([]byte(store.J(e.NodeRequest)), &j.NodeRequest)
	j.NodeRecovery = nil
	j.NodeExpectedInput = e.NodeReceipt.InputKey
	j.NodeExpectedBundle = e.NodeReceipt.BundleKey
	j.NodeExpectedLock = e.NodeReceipt.LockSHA
	return nil
}
