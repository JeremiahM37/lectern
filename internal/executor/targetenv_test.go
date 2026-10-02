package executor

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Each executor carries its own environment. A local target and a sandbox
// target both get a Local executor; were Local zero-sized, Go could hand both
// the same address, and the sandbox's empty environment would replace the
// local target's backend.
func TestLocalExecutorsKeepTheirOwnTargetEnv(t *testing.T) {
	r := NewRegistry(false, 0)
	r.Env = func(t *store.Target, _ Executor) TargetEnv {
		if t.Kind == "local" {
			return TargetEnv{SessionBackend: "pty", Lectern: "/bin/lectern"}
		}
		return TargetEnv{}
	}
	local, err := r.For(&store.Target{ID: 1, Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := r.For(&store.Target{ID: 2, Kind: "sandbox"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { forgetTargetEnv(local); forgetTargetEnv(sandbox) })
	if got := TargetEnvOf(local).SessionBackend; got != "pty" {
		t.Fatalf("the local target's backend became %q", got)
	}
}
