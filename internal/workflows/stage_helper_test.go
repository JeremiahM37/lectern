package workflows

import (
	"context"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

func TestStageFilesRunsTheTargetsLecternHelper(t *testing.T) {
	files := []File{{Path: []string{"bin", "run"}, Data: []byte("x"), Mode: 0o755}}
	for _, c := range []struct {
		lectern, prefix string
	}{
		{"", "python3 -c "},
		{"/opt/lectern/bin/lectern", "/opt/lectern/bin/lectern helper workflow-stage '{"},
	} {
		ex := executor.NewMock(0)
		if c.lectern != "" {
			executor.SetTargetEnv(ex, executor.TargetEnv{Lectern: c.lectern})
		}
		if err := StageFiles(context.Background(), ex, "/r", files); err != nil {
			t.Fatal(err)
		}
		if log := ex.CmdLog(); len(log) != 1 || !strings.HasPrefix(log[0], c.prefix) {
			t.Fatalf("lectern %q ran %q", c.lectern, log)
		}
	}
}
