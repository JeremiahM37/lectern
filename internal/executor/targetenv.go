package executor

import "sync"

// TargetEnv is what Lectern knows about the machine an executor drives, beyond
// how to reach it: which session backend keeps its terminals (docs/ptyhost.md)
// and where the lectern binary is on it, if anywhere.
//
// It is kept beside the executor rather than in it, so every executor kind —
// and every test double — carries it without knowing about it. An executor
// nobody resolved (tests, a sandbox's per-attempt container) has the zero
// value: tmux, and no lectern binary.
type TargetEnv struct {
	// SessionBackend is "tmux" or "pty"; "" means tmux.
	SessionBackend string
	// Lectern is the lectern binary's path on the target, in a form the
	// target's shell accepts, or "" when there is none. Target-side helpers
	// then fall back to their Python versions.
	Lectern string
}

var targetEnvs sync.Map // Executor -> TargetEnv

// SetTargetEnv records what is known about the target ex drives.
func SetTargetEnv(ex Executor, env TargetEnv) {
	if ex != nil {
		targetEnvs.Store(ex, env)
	}
}

// TargetEnvOf returns what was recorded for ex, or the zero TargetEnv.
func TargetEnvOf(ex Executor) TargetEnv {
	if ex == nil {
		return TargetEnv{}
	}
	if v, ok := targetEnvs.Load(ex); ok {
		return v.(TargetEnv)
	}
	return TargetEnv{}
}

// forgetTargetEnv drops the record of a closed executor.
func forgetTargetEnv(ex Executor) { targetEnvs.Delete(ex) }
