package lsp

import (
	"context"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/symedit"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// PreviewWorkspaceEdit returns the changes edit would make to disk, in the
// order ApplyWorkspaceEdit would write them. Nothing is written.
func PreviewWorkspaceEdit(edit WorkspaceEdit) ([]FileChange, error) {
	ops, err := edit.operations()
	if err != nil {
		return nil, err
	}
	p := &plan{files: map[string]*fileState{}}
	for _, op := range ops {
		if err := p.apply(op); err != nil {
			return nil, err
		}
	}
	return p.changes(), nil
}

// changes lists what commit would write or remove, without touching disk.
func (p *plan) changes() []FileChange {
	var out []FileChange
	var removed []FileChange
	for _, path := range p.order {
		st := p.files[path]
		switch {
		case st.exists && (!st.existed || st.content != st.orig):
			out = append(out, FileChange{Path: path, Before: st.orig, After: st.content, Created: !st.existed})
		case !st.exists && st.existed:
			removed = append(removed, FileChange{Path: path, Before: st.orig, Deleted: true})
		}
	}
	return append(out, removed...)
}

// applyGuarded applies edit the way the other write tools do. When apply is
// true every file the edit touches must pass symedit.CheckWritable (path
// rules, and read-and-not-stale for existing files) before anything is
// written; the edit is then written, and each file is recorded as read at its
// new content so the model can keep editing it. When apply is false the
// changes are returned as a preview and nothing is checked or written.
func applyGuarded(ctx context.Context, m *Manager, env *tools.Env, edit WorkspaceEdit, apply bool) ([]FileChange, error) {
	changes, err := PreviewWorkspaceEdit(edit)
	if err != nil {
		return nil, err
	}
	if !apply {
		return changes, nil
	}
	for _, c := range changes {
		if err := symedit.CheckWritable(ctx, env, c.Path); err != nil {
			return nil, err
		}
	}
	before := func(path string) {
		if env != nil && env.Files != nil {
			env.Files.BeforeWrite(path)
		}
	}
	applied, err := ApplyWorkspaceEditWith(edit, before)
	if err != nil {
		return nil, err
	}
	for _, c := range applied.Changes {
		if !c.Deleted {
			symedit.NoteWritten(env, c.Path)
		}
		m.FileChanged(c.Path)
	}
	return applied.Changes, nil
}
