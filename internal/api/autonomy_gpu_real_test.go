package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/google/uuid"
)

// Explicit privileged fixture only. Ordinary verification never launches GPU
// work. The supplied fixture uses frozen reviewed helpers and actual shared
// lease/guest cleanup, but its assignment/quota records are synthetic.
func TestGPURealFrozenControllerTransport(t *testing.T) {
	root := os.Getenv("LECTERN_GPU_REAL_PROOF_ROOT")
	if root == "" {
		t.Skip("requires explicitly prepared privileged GPU fixture; no hardware touched")
	}
	if os.Geteuid() != 0 || filepath.Dir(root) != "/mnt/bulk" || !strings.HasPrefix(filepath.Base(root), "gpu-controller-proof-") {
		t.Fatal("invalid explicit GPU fixture")
	}
	jobs := filepath.Join(root, "jobs")
	runner := filepath.Join(root, "tools", "autonomy-runner.py")
	if _, err := os.Lstat(runner); err != nil {
		t.Fatal(err)
	}
	s, a, proposal := documentationFixture(t)
	j := a.Jobs[0]
	j.ID = uuid.NewString()
	j.Status = "running"
	j.Provider = "codex"
	j.Admission = &autoAdmission{TaskID: j.TaskID, JobID: j.ID, Proposal: autonomy.Proposal{ProjectID: proposal.ProjectID, Acceptance: []string{"actual isolated GPU fixture positive reference with immutable captured source"}}}
	a.State.Assignments = []autonomy.Assignment{{TaskID: j.TaskID, Role: j.Role}}
	expertQuota(a, time.Now())
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(jobs, j.ID, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "candidate.txt"), []byte("42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(root, "shim")
	if err := os.MkdirAll(shim, 0700); err != nil {
		t.Fatal(err)
	}
	// The shim accepts only the fixed runner interface. It does not emulate any
	// receipt, guest call, source snapshot, process or GPU output.
	program := "#!/usr/bin/python3 -I\nimport os,sys\nassert sys.argv[1:3]==['-n','/usr/local/libexec/lectern-autonomy-runner']\nassert sys.argv[3] in ('gpu-source-prepare','gpu-source-status','gpu-source-stop','gpu-source-manifest','gpu-research','gpu-research-status','gpu-research-stop','gpu-research-output')\nos.execv('/usr/bin/python3',['/usr/bin/python3','-I'," + string(mustGPUJSON(runner)) + ",*sys.argv[3:]])\n"
	if err := os.WriteFile(filepath.Join(shim, "sudo"), []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	defer func() {
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			saved, err := s.loadAuto()
			if err != nil {
				t.Error(err)
				return
			}
			err = s.pollAutoGPUAt(context.Background(), saved, jobs, false)
			_ = s.saveAuto(saved)
			if !autoGPUPending(saved) {
				return
			}
			if err != nil {
				t.Log("cleanup pending:", err)
			}
			time.Sleep(time.Second)
		}
		t.Error("owned GPU fixture cleanup did not settle; retained shared fence must remain")
	}()
	script := "from pathlib import Path\nimport torch\nassert int(Path('/source/candidate.txt').read_text())==42\nassert torch.cuda.is_available()\nx=torch.arange(256,device='cuda',dtype=torch.float32).reshape(16,16)\ny=x@x.T\ntorch.cuda.synchronize()\nassert torch.allclose(y.cpu(),x.cpu()@x.cpu().T)\nprint('controller_bound_gpu_reference_pass')\n"
	input, _ := json.Marshal(autoGPUExperimentInput{Script: script, Argv: []string{}, SourcePaths: []string{"candidate.txt"}})
	posted := httptest.NewRecorder()
	s.autoGPUBridgeAt(jobs, j.ID, posted, httptest.NewRequest("POST", "/research-runs", strings.NewReader(string(input))))
	if posted.Code != 202 {
		t.Fatalf("POST %d: %s", posted.Code, posted.Body.String())
	}
	var view struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(posted.Body.Bytes(), &view) != nil || !autoHash256(view.ID) {
		t.Fatal("missing durable experiment ID")
	}
	deadline := time.Now().Add(240 * time.Second)
	var final *autoGPUExperiment
	for time.Now().Before(deadline) {
		saved, err := s.loadAuto()
		if err != nil {
			t.Fatal(err)
		}
		experiment := saved.GPUExperiments[view.ID]
		experiment.NextPoll = time.Time{}
		if err = s.pollAutoGPUAt(context.Background(), saved, jobs, true); err != nil {
			t.Log("poll:", err)
		}
		if err = s.saveAuto(saved); err != nil {
			t.Fatal(err)
		}
		if autoGPUFinished(experiment) {
			final = experiment
			break
		}
		time.Sleep(time.Second)
	}
	if final == nil {
		t.Fatal("GPU fixture did not reach a terminal receipt")
	}
	var receipt autonomy.GPUResearchReceipt
	if json.Unmarshal([]byte(final.Receipt), &receipt) != nil || receipt.State != "exited" || receipt.Executed == nil || !*receipt.Executed || receipt.ExitCode == nil || *receipt.ExitCode != 0 || !receipt.CleanupConfirmed {
		t.Fatalf("terminal GPU fixture: %+v", final)
	}
	output := httptest.NewRecorder()
	s.autoGPUBridgeAt(jobs, j.ID, output, httptest.NewRequest("GET", "/research-runs?id="+view.ID+"&offset=0", nil))
	if output.Code != 200 {
		t.Fatalf("output: %d %s", output.Code, output.Body.String())
	}
	var observedOutput struct {
		Data  string `json:"data_base64"`
		SHA   string `json:"output_sha256"`
		Total int    `json:"total_bytes"`
	}
	if err := json.Unmarshal(output.Body.Bytes(), &observedOutput); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(observedOutput.Data)
	if err != nil || !strings.Contains(string(decoded), "controller_bound_gpu_reference_pass") || observedOutput.SHA != receipt.OutputSHA || len(decoded) != observedOutput.Total || fmt.Sprintf("%x", sha256.Sum256(decoded)) != receipt.OutputSHA {
		t.Fatal("actual authenticated output lacks reference result or digest binding")
	}
	proof := map[string]any{"scope": "Actual HTTP/controller/frozen-root-runner/current-source/shared-fence/guest-GPU execution; synthetic disposable assignment and quota, not an autonomous workshop outcome", "experiment": final, "output": json.RawMessage(output.Body.Bytes())}
	raw, _ := json.MarshalIndent(proof, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "proof.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func mustGPUJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
