package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// A root runner receipt describes the immutable Go runtime delivered before the
// model starts. Module provisioning alone is not historical toolchain evidence.
type autoGoRuntimeReceipt struct {
	SchemaVersion   int    `json:"schema_version"`
	State           string `json:"state"`
	OwnerJob        string `json:"owner_job"`
	DependencyKey   string `json:"go_dependency_key"`
	BundleDigest    string `json:"go_bundle_digest"`
	ToolchainDigest string `json:"go_toolchain_digest"`
	Executed        *bool  `json:"executed,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Diagnostic      string `json:"diagnostic,omitempty"`
}

func autoGoRuntimeValid(r *autoGoRuntimeReceipt) bool {
	return r != nil && r.SchemaVersion == 1 && r.State == "verified" && autoHash256(r.DependencyKey) && autoHash256(r.BundleDigest) && autoHash256(r.ToolchainDigest)
}
func autoSameGoRuntime(a, b *autoGoRuntimeReceipt) bool {
	return autoGoRuntimeValid(a) && autoGoRuntimeValid(b) && a.DependencyKey == b.DependencyKey && a.BundleDigest == b.BundleDigest && a.ToolchainDigest == b.ToolchainDigest
}
func autoExpectedGoDependency(j *autoJob) string {
	if autoGoRuntimeValid(j.GoExpectedRuntime) {
		return j.GoExpectedRuntime.DependencyKey
	}
	if j.Recovery != nil {
		return j.Recovery.Key
	}
	return ""
}
func autoRecordGoRuntime(j *autoJob, raw []byte) error {
	var status struct {
		Go *autoGoRuntimeReceipt `json:"go_runtime"`
	}
	if json.Unmarshal(raw, &status) != nil {
		return errors.New("invalid Go runtime status")
	}
	if status.Go == nil {
		return nil
	} // Historical jobs do not acquire retrospective proof.
	r := status.Go
	if r.SchemaVersion != 1 || r.OwnerJob != j.ID || j.Recovery == nil || j.Recovery.State != "verified" || r.DependencyKey != autoExpectedGoDependency(j) {
		return errors.New("Go runtime owner/dependency binding differs")
	}
	if r.State == "unavailable" && r.Executed != nil && !*r.Executed && (r.Diagnostic == "missing" || r.Diagnostic == "integrity" || r.Diagnostic == "incompatible") {
		selected := j.GoExpectedRuntime
		if selected == nil && autoGoRuntimeValid(j.GoRuntime) {
			selected = j.GoRuntime
		}
		if selected != nil && (r.BundleDigest != selected.BundleDigest || r.ToolchainDigest != selected.ToolchainDigest) {
			return errors.New("Go failed runtime differs from selected immutable environment")
		}
		// Preserve a previously delivered selection before replacing its status
		// with the failure diagnostic; retry clones must not fall back to host Go.
		if j.GoExpectedRuntime == nil && autoGoRuntimeValid(j.GoRuntime) {
			selected := *j.GoRuntime
			j.GoExpectedRuntime = &selected
		}
		j.GoRuntime = r
		return nil
	}
	if !autoGoRuntimeValid(r) || j.GoExpectedRuntime != nil && !autoSameGoRuntime(j.GoExpectedRuntime, r) {
		return errors.New("Go delivered runtime differs from selected immutable environment")
	}
	if autoGoRuntimeValid(j.GoRuntime) && !autoSameGoRuntime(j.GoRuntime, r) {
		return errors.New("Go delivered runtime changed within one worker")
	}
	j.GoRuntime = r
	return nil
}
func autoInheritGoRuntime(j, source *autoJob) {
	if j.GoExpectedRuntime == nil && source != nil && autoGoRuntimeValid(source.GoRuntime) {
		r := *source.GoRuntime
		j.GoExpectedRuntime = &r
	}
}
func autoPrepareGoRuntimeAt(root string, j *autoJob) error {
	if j.GoRuntime != nil && j.GoRuntime.OwnerJob != j.ID {
		autoInheritGoRuntime(j, &autoJob{GoRuntime: j.GoRuntime})
		j.GoRuntime = nil
		j.GoNeedsResume = false
		j.GoRecoveryVerified = false
		j.GoRuntimeSourceSHA = ""
	}
	if j.GoExpectedRuntime == nil {
		return nil
	}
	r := j.GoExpectedRuntime
	if !autoGoRuntimeValid(r) {
		return errors.New("invalid inherited Go runtime")
	}
	raw, err := json.Marshal(struct {
		Schema    int    `json:"schema_version"`
		Key       string `json:"go_dependency_key"`
		Bundle    string `json:"go_bundle_digest"`
		Toolchain string `json:"go_toolchain_digest"`
	}{1, r.DependencyKey, r.BundleDigest, r.ToolchainDigest})
	if err != nil {
		return err
	}
	target := filepath.Join(root, j.ID, "go-runtime-requirement.json")
	if old, e := os.ReadFile(target); e == nil {
		if string(old) != string(raw) {
			return errors.New("Go runtime request changed")
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".go-runtime-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), target); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func autoSelectGoTestRuntime(j *autoJob, r *autoExpertProbeRuntime) error {
	if j.GoRuntime == nil {
		if j.GoExpectedRuntime != nil || j.Recovery != nil && j.Recovery.State == "verified" {
			return errors.New("expected Go runtime delivery not yet recorded")
		}
		return nil
	}
	if !autoGoRuntimeValid(j.GoRuntime) || j.GoRuntime.OwnerJob != j.ID || j.Recovery == nil || j.Recovery.State != "verified" || j.GoRuntime.DependencyKey != autoExpectedGoDependency(j) {
		return errors.New("Go runtime delivery unavailable")
	}
	r.GoDependency = j.GoRuntime.DependencyKey
	r.GoBundle = j.GoRuntime.BundleDigest
	r.GoToolchain = j.GoRuntime.ToolchainDigest
	return nil
}
func autoGoReceiptRuntime(lease *autoExpertProbeLease, r autoExpertProbeReceipt) error {
	if r.State != "exited" && (r.Executed == nil || !*r.Executed) {
		return nil
	}
	selected := lease.Runtime != nil && lease.Runtime.GoDependency != ""
	present := r.Runtime != nil && (r.Runtime.GoDependency != "" || r.Runtime.GoBundle != "" || r.Runtime.GoToolchain != "")
	if selected {
		if r.Runtime == nil || *r.Runtime != *lease.Runtime {
			return errors.New("expert Go execution differs from reserved runtime")
		}
	} else if present {
		return errors.New("unreserved Go runtime in expert execution")
	}
	return nil
}
