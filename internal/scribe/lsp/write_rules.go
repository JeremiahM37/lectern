package lsp

import (
	"context"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
)

// CheckEditRules applies the file write rules the edit tools use to every
// file edit would change: .git internals and secret-named files are refused,
// forbidden paths are refused, and CI and container definitions need the
// user's approval. It runs before anything is written, so a refused file means
// no file in the edit changes.
func CheckEditRules(ctx context.Context, env *tools.Env, edit WorkspaceEdit) error {
	changes, err := PreviewWorkspaceEdit(edit)
	if err != nil {
		return err
	}
	for _, c := range changes {
		if err := fs.CheckWrite(ctx, env, "rename", c.Path); err != nil {
			return err
		}
	}
	return nil
}

// applyChecked is ApplyWorkspaceEditWith behind CheckEditRules. Every refactor
// and format write in this package goes through it.
func applyChecked(ctx context.Context, env *tools.Env, edit WorkspaceEdit, before func(abs string)) (ApplyResult, error) {
	if err := CheckEditRules(ctx, env, edit); err != nil {
		return ApplyResult{}, err
	}
	return ApplyWorkspaceEditWith(edit, before)
}
