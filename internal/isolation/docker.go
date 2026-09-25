package isolation

import (
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// BuildDockerArgv renders `docker run` up to (not including) the trailing
// `bash -c <command>`. Like BuildBwrapArgv it is a pure function of its
// inputs. $HOME-relative mounts use a literal "$HOME/..." shell token so the
// same command works whichever machine's docker daemon actually runs it —
// see BuildBwrapArgv's comment.
//
// Network policy: allow leaves Docker's own default bridge network in place
// (the agent reaches the internet exactly as an unisolated launch would);
// deny is `--network none` — real, kernel-enforced, and total. There is no
// egress-proxy bridge for Docker in this release (see NetworkDeny's doc
// comment and docs/isolation.md), so a denied Docker session has no network
// at all, not even its own model API. Callers that want network=deny with a
// reachable model API should use bwrap on a local target instead.
func BuildDockerArgv(cfg Config, o WrapOpts) ([]string, error) {
	if strings.TrimSpace(o.Workdir) == "" {
		return nil, fmt.Errorf("isolation: workdir is required")
	}
	n := cfg.Normalized()
	if n.Mode != Docker {
		return nil, fmt.Errorf("isolation: BuildDockerArgv called with mode %q", n.Mode)
	}
	workdir := shellq.Quote(o.Workdir)
	args := []string{
		"docker", "run", "--rm", "-i", "-t",
		"-w", workdir,
		"-v", workdir + ":" + workdir,
	}
	for _, rel := range HomePaths(o.Agent) {
		p := homeJoin(rel)
		args = append(args, "-v", p+":"+p)
	}
	if n.Network == NetworkDeny {
		args = append(args, "--network", "none")
	}
	args = append(args, shellq.Quote(n.DockerImage))
	return args, nil
}

// wrapDocker renders the full `docker run` command for one agent invocation.
func wrapDocker(invocation string, cfg Config, o WrapOpts) (string, error) {
	argv, err := BuildDockerArgv(cfg, o)
	if err != nil {
		return "", err
	}
	full := append(append([]string{}, argv...), "bash", "-c", shellq.Quote(invocation))
	return strings.Join(full, " "), nil
}
