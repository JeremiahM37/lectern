package sessions

import (
	"context"
	"testing"

	"github.com/JeremiahM37/lectern/internal/executor"
)

type bootExecutor struct {
	result executor.Result
	err    error
	cmd    string
}

func (e *bootExecutor) Run(_ context.Context, cmd string, _ executor.RunOpts) (executor.Result, error) {
	e.cmd = cmd
	return e.result, e.err
}
func (*bootExecutor) ReadFile(context.Context, string, int64) ([]byte, error) { return nil, nil }
func (*bootExecutor) WriteFile(context.Context, string, []byte) error         { return nil }
func (*bootExecutor) Close() error                                            { return nil }

func TestProbeBootIDRequiresKnownLinuxIdentity(t *testing.T) {
	e := &bootExecutor{result: executor.Result{Stdout: "01234567-89ab-cdef-0123-456789abcdef\n"}}
	id, ok := ProbeBootID(context.Background(), e)
	if !ok || id != "01234567-89ab-cdef-0123-456789abcdef" || e.cmd != "cat /proc/sys/kernel/random/boot_id" {
		t.Fatalf("probe: %q %v %q", id, ok, e.cmd)
	}
	for _, result := range []executor.Result{{RC: 1, Stdout: "x"}, {Stdout: "not-a-boot-id"}, {Stdout: "------------------------------------"}, {Stdout: "01234567-89ab-cdef-0123-456789abcdef0"}} {
		e.result = result
		if id, ok := ProbeBootID(context.Background(), e); ok || id != "" {
			t.Fatalf("accepted unknown probe: %q %v", id, ok)
		}
	}
}

func TestHasSessionUsesExactTmuxTarget(t *testing.T) {
	got := HasSessionCommand("lec-s12")
	if got != "tmux has-session -t =lec-s12" {
		t.Fatalf("must use an exact tmux target, got %q", got)
	}
}
