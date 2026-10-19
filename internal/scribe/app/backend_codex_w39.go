package app

// Wave 39: the Codex CLI hooks behind /codex (tui/commands_codex_w39.go). The
// binary comes from SCRIBE_CODEX_BIN. An empty value lets the codex package
// find `codex` on PATH, so a normal install needs no setting.

import (
	"context"
	"os"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/codex"
)

// codexBinaryEnv names the environment variable that overrides the codex binary.
const codexBinaryEnv = "SCRIBE_CODEX_BIN"

// codexBinary is the codex command to run, or "" for the one on PATH.
func codexBinary() string { return os.Getenv(codexBinaryEnv) }

// CodexStatus reads the CLI's install and account state (spec 03 section 4.4).
func (b *scribeBackend) CodexStatus(ctx context.Context) codex.Status {
	return codex.ReadStatus(ctx, codexBinary())
}

// CodexLogin starts a sign-in in the given flow. The returned channel is
// closed after a done or failed event.
func (b *scribeBackend) CodexLogin(ctx context.Context, mode codex.LoginMode) (<-chan codex.LoginEvent, error) {
	return codex.Login(ctx, codexBinary(), mode)
}

// CodexLogout signs the CLI out of its ChatGPT account.
func (b *scribeBackend) CodexLogout(ctx context.Context) error {
	return codex.Logout(ctx, codexBinary())
}
