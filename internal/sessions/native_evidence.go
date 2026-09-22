package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/nativeidentity"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// NativeEvidence is the read-only proof that a native conversation belongs to
// the interactive process currently occupying a tracked terminal.
type NativeEvidence struct {
	State      string `json:"state"`
	ID         string `json:"id"`
	PID        int    `json:"pid"`
	ProcStart  string `json:"proc_start"`
	Workspace  string `json:"workspace"`
	RootPID    int    `json:"root_pid"`
	RootStart  string `json:"root_start"`
	PaneID     string `json:"pane_id"`
	NativeHome string `json:"native_home"`
	BootID     string `json:"boot_id"`
}

// CaptureNativeEvidence resolves the native identity in one target-side probe.
// It deliberately returns no identity when the process, transcript, or pane
// changes during the probe; callers must treat that as a hard validation error.
func CaptureNativeEvidence(ctx context.Context, ex executor.Executor, agent, workdir, home, tmuxName, tracking string, discovery bool) (NativeEvidence, error) {
	args := make([]string, 0, 5)
	for _, v := range []string{agent, workdir, home, tmuxName, tracking} {
		b, _ := json.Marshal(v)
		args = append(args, string(b))
	}
	flag := "False"
	if discovery {
		flag = "True"
	}
	cmd := "python3 -c " + shellq.Quote(nativeidentity.RecordsScript+"\n"+nativeidentity.IdentityScript+"\nimport json\nprint(json.dumps(native_identity("+strings.Join(args, ",")+","+flag+")))")
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 10})
	if err != nil || !r.OK() {
		return NativeEvidence{}, fmt.Errorf("could not inspect the interactive native process")
	}
	var raw struct {
		State    string `json:"state"`
		ID       string `json:"id"`
		Evidence struct {
			PID        int    `json:"pid"`
			ProcStart  string `json:"proc_start"`
			Workspace  string `json:"workspace"`
			RootPID    int    `json:"root_pid"`
			RootStart  string `json:"root_start"`
			PaneID     string `json:"pane_id"`
			NativeHome string `json:"native_home"`
			BootID     string `json:"boot_id"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &raw); err != nil {
		return NativeEvidence{}, fmt.Errorf("could not parse native process identity")
	}
	if raw.State != "identified" || raw.ID == "" || raw.Evidence.PID <= 0 || raw.Evidence.ProcStart == "" || raw.Evidence.RootPID <= 0 || raw.Evidence.RootStart == "" || raw.Evidence.PaneID == "" || raw.Evidence.NativeHome == "" || raw.Evidence.BootID == "" {
		if raw.State == "ambiguous" {
			return NativeEvidence{}, fmt.Errorf("native process identity is ambiguous")
		}
		return NativeEvidence{}, fmt.Errorf("the interactive process has no provable native conversation identity; keep the terminal open and try again")
	}
	return NativeEvidence{State: raw.State, ID: raw.ID, PID: raw.Evidence.PID, ProcStart: raw.Evidence.ProcStart, Workspace: raw.Evidence.Workspace, RootPID: raw.Evidence.RootPID, RootStart: raw.Evidence.RootStart, PaneID: raw.Evidence.PaneID, NativeHome: raw.Evidence.NativeHome, BootID: raw.Evidence.BootID}, nil
}
