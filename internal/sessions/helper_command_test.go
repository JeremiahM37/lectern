package sessions

import (
	"context"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

func TestAllocationRealpathRunsTheTargetsLecternHelper(t *testing.T) {
	for _, c := range []struct {
		lectern, prefix string
	}{
		{"", "python3 -c "},
		{"/opt/lectern/bin/lectern", "/opt/lectern/bin/lectern helper realpath /w/alloc"},
	} {
		ex := executor.NewMock(0)
		if c.lectern != "" {
			executor.SetTargetEnv(ex, executor.TargetEnv{Lectern: c.lectern})
		}
		canonicalWorkspaceAllocation(context.Background(), ex, "/w/alloc")
		if log := ex.CmdLog(); len(log) != 1 || !strings.HasPrefix(log[0], c.prefix) {
			t.Fatalf("lectern %q ran %q", c.lectern, log)
		}
	}
}
