package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

func TestSkillCommandsRunTheTargetsLecternHelper(t *testing.T) {
	for _, c := range []struct {
		lectern, prefix string
	}{
		{"", "python3 -c "},
		{"/opt/lectern/bin/lectern", "/opt/lectern/bin/lectern helper skills '{"},
	} {
		ex := executor.NewMock(0)
		if c.lectern != "" {
			executor.SetTargetEnv(ex, executor.TargetEnv{Lectern: c.lectern})
		}
		Discover(context.Background(), ex, nil, "claude") // the mock's reply is not a skill list
		if log := ex.CmdLog(); len(log) != 1 || !strings.HasPrefix(log[0], c.prefix) {
			t.Fatalf("lectern %q ran %q", c.lectern, log)
		}
	}
}
