package worktree

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/internal/executor"
)

// Interactive records an allocation before any remote Git mutation. A failed
// launch keeps this record so the directory is still discoverable and removable.
type Interactive struct {
	OperationActive bool                  `json:"operation_active,omitempty"`
	ControlToken    string                `json:"control_token,omitempty"`
	SetupCommand    string                `json:"setup_command,omitempty"`
	SetupEnv        map[string]string     `json:"setup_env,omitempty"`
	SetupState      string                `json:"setup_state,omitempty"`
	SetupOutput     string                `json:"setup_output,omitempty"`
	Repo            string                `json:"repo"`
	Path            string                `json:"path"`
	Branch          string                `json:"branch"`
	Base            string                `json:"base"`
	Commit          string                `json:"commit"`
	Token           string                `json:"token,omitempty"`
	State           string                `json:"state"`
	Error           string                `json:"error,omitempty"`
	Repositories    []WorkspaceRepository `json:"repositories,omitempty"`
}
type InteractiveOptions struct {
	Base              string                `json:"base"`
	Branch            string                `json:"branch"`
	Namespace         string                `json:"-"`
	Workroot          string                `json:"-"`
	ExtraRepositories []RepositorySelection `json:"extra_repositories,omitempty"`
}

type RepositorySelection struct {
	ProjectID int64  `json:"project_id"`
	Base      string `json:"base,omitempty"`
}

func PlanInteractive(repo string, id int64, o InteractiveOptions) *Interactive {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		panic(err)
	}
	key := hex.EncodeToString(token)
	branch := strings.TrimSpace(o.Branch)
	if branch == "" {
		branch = fmt.Sprintf("lec/session%d-%s", id, key[:8])
		branch = NamespacedBranch(branch, o.Namespace)
	}
	base := strings.TrimSpace(o.Base)
	if base == "" {
		base = "HEAD"
	}
	root := o.Workroot
	if root == "" {
		root = DefaultWorkroot(repo)
	}
	root = NamespacedWorkroot(root, o.Namespace)
	return &Interactive{Repo: repo, Path: fmt.Sprintf("%s/session%d-%s", root, id, key[:8]), Branch: branch, Base: base, Token: key, State: "creating"}
}

//go:embed interactive.py
var interactiveScript string

//go:embed setup_control.py
var setupControlRaw string

var setupControlScript = func() string {
	source, _ := json.Marshal(setupControlRaw)
	return "setup_control_source = " + string(source) + "\n" + setupControlRaw
}()

func RunInteractive(ctx context.Context, ex executor.Executor, action string, plan *Interactive) error {
	return RunInteractiveWithTimeout(ctx, ex, action, plan, 120)
}

func RunInteractiveWithTimeout(ctx context.Context, ex executor.Executor, action string, plan *Interactive, timeout float64) error {
	script := setupControlScript + "\n" + interactiveScript
	extra := fmt.Sprintf(" - %.0f", max(1, timeout-30))
	if len(plan.Repositories) > 0 {
		if action == "check-create" {
			script = multiPreflightScript
			extra = ""
		} else {
			script = setupControlScript + "\n" + multiWorkerScript
			extra = " " + executor.ShellQuote(multiPreflightScript) + " " + executor.ShellQuote(setupControlScript+"\n"+interactiveScript) + fmt.Sprintf(" %.0f", max(1, timeout-15))
		}
	}
	if action == "cancel" {
		script = setupControlScript + "\nimport sys\np=json.loads(sys.argv[2])\ntry:\n SetupControl(p).access(cancel=True)\n print(json.dumps({'workspace':p}))\nexcept (OSError,ValueError,KeyError) as e:\n print(json.dumps({'error':str(e)}));sys.exit(1)"
		extra = ""
		timeout = 10
	}
	data, _ := json.Marshal(plan)
	if action == "status" {
		timeout = 10
	}
	result, err := ex.Run(ctx, "python3 -c "+executor.ShellQuote(script)+" "+executor.ShellQuote(action)+" "+executor.ShellQuote(string(data))+extra, executor.RunOpts{Timeout: timeout})
	if err != nil {
		return err
	}
	var out struct {
		Workspace *Interactive `json:"workspace"`
		Error     string       `json:"error"`
	}
	if json.Unmarshal([]byte(result.Stdout), &out) != nil {
		return fmt.Errorf("worktree operation failed on target: %s", strings.TrimSpace(result.Stderr))
	}
	if !result.OK() {
		if out.Workspace != nil {
			*plan = *out.Workspace
		}
		return fmt.Errorf("%s", out.Error)
	}
	if out.Workspace == nil {
		return fmt.Errorf("target returned no worktree record")
	}
	*plan = *out.Workspace
	return nil
}

// HasSetupCommand gives package installation a longer overall setup deadline.
func (p *Interactive) HasSetupCommand() bool {
	if strings.TrimSpace(p.SetupCommand) != "" {
		return true
	}
	for _, repo := range p.Repositories {
		if repo.Worktree != nil && repo.Worktree.HasSetupCommand() {
			return true
		}
	}
	return false
}
