package symedit

import (
	"context"
	"fmt"
	"os"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// CheckWritable reports whether a tool may write abs through an edit it did
// not compute itself, such as a language server's workspace edit. It applies
// the same path rules ast_edit does (no .git, approval for sensitive files and
// for files outside the project), and an existing file must have been read in
// this session and must not have changed since. It returns an error that says
// what to do next.
func CheckWritable(ctx context.Context, env *tools.Env, abs string) error {
	if env == nil {
		return fmt.Errorf("no environment to check the write against")
	}
	_, rel, err := gate(ctx, env, abs)
	if err != nil {
		return err
	}
	if env.Files == nil {
		return nil
	}
	if _, statErr := os.Stat(abs); statErr != nil {
		return nil // a file the edit creates has nothing to be stale against
	}
	if !env.Files.WasRead(abs) {
		return fmt.Errorf("%s has not been read this session. Read it first so the edit is based on its current content", rel)
	}
	if env.Files.StaleSinceRead(abs) {
		return fmt.Errorf("%s changed on disk since you read it. Read it again, then retry", rel)
	}
	return nil
}

// NoteWritten records that the model now has abs at its new content, so a
// later edit to it is not refused as stale. Call it after a successful write.
func NoteWritten(env *tools.Env, abs string) {
	if env != nil && env.Files != nil {
		env.Files.MarkRead(abs)
	}
}
