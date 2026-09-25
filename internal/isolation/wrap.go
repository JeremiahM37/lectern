package isolation

import "fmt"

// Wrap renders one already-built agent invocation (the env-prefixed command
// line a caller would otherwise have run directly) as it should actually run
// under cfg. None returns invocation unchanged — every existing launch path
// keeps behaving exactly as it does today.
//
// The caller is responsible for the surrounding "cd workdir && ...; exec
// bash"/redirection wrapping that sessions.Spec.LaunchCommand and
// agents.Launcher.Command already build: Wrap only replaces the command that
// runs, not how its stdio is captured or how the pane exits.
func Wrap(invocation string, cfg Config, o WrapOpts) (string, error) {
	n := cfg.Normalized()
	switch n.Mode {
	case None:
		return invocation, nil
	case Bwrap:
		return wrapBwrap(invocation, n, o)
	case Docker:
		return wrapDocker(invocation, n, o)
	default:
		return "", fmt.Errorf("isolation: unknown mode %q", n.Mode)
	}
}
