package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// The TUI finds /codex by type assertion, so the methods must hold on the
// backend itself. The orchestrator adds the same line to backend_optional.go.
var _ tui.CodexAccount = (*scribeBackend)(nil)

func TestCodexBinaryComesFromEnv(t *testing.T) {
	t.Setenv(codexBinaryEnv, "/opt/codex/bin/codex")
	if got := codexBinary(); got != "/opt/codex/bin/codex" {
		t.Errorf("codexBinary() = %q, want the SCRIBE_CODEX_BIN value", got)
	}
	t.Setenv(codexBinaryEnv, "")
	if got := codexBinary(); got != "" {
		t.Errorf("codexBinary() = %q with the variable unset, want empty (PATH lookup)", got)
	}
}

// A binary that does not exist must report "not installed" and never run
// anything real, so the test cannot touch an account.
func TestCodexStatusWithMissingBinaryReportsNotInstalled(t *testing.T) {
	t.Setenv(codexBinaryEnv, filepath.Join(t.TempDir(), "no-such-codex"))
	b := &scribeBackend{}
	st := b.CodexStatus(context.Background())
	if st.Installed {
		t.Fatalf("Installed = true for a binary that does not exist: %+v", st)
	}
}
