package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// FileChange describes one file a workspace edit changed on disk.
type FileChange struct {
	Path    string // absolute
	Before  string // content before the edit ("" when created)
	After   string // content after the edit ("" when deleted)
	Created bool
	Deleted bool
}

// ApplyResult lists what ApplyWorkspaceEdit changed, in the order it wrote.
type ApplyResult struct {
	Changes []FileChange
}

// Paths returns the absolute paths changed.
func (r ApplyResult) Paths() []string {
	out := make([]string, 0, len(r.Changes))
	for _, c := range r.Changes {
		out = append(out, c.Path)
	}
	return out
}

// ApplyWorkspaceEdit applies edit to disk. See ApplyWorkspaceEditWith.
func ApplyWorkspaceEdit(edit WorkspaceEdit) (ApplyResult, error) {
	return ApplyWorkspaceEditWith(edit, nil)
}

// ApplyWorkspaceEditWith applies edit to disk. The whole edit is computed in
// memory first, so a failing operation leaves every file untouched. before,
// when non-nil, is called with each absolute path just before that file is
// written or removed. Only files whose content actually changes are touched.
func ApplyWorkspaceEditWith(edit WorkspaceEdit, before func(abs string)) (ApplyResult, error) {
	ops, err := edit.operations()
	if err != nil {
		return ApplyResult{}, err
	}
	p := &plan{files: map[string]*fileState{}}
	for _, op := range ops {
		if err := p.apply(op); err != nil {
			return ApplyResult{}, err
		}
	}
	return p.commit(before)
}

type editOp interface{ isEditOp() }

type textOp struct {
	path  string
	edits []TextEdit
}

type createOp struct {
	path                      string
	overwrite, ignoreIfExists bool
}

type renameOp struct {
	from, to                  string
	overwrite, ignoreIfExists bool
}

type deleteOp struct {
	path              string
	ignoreIfNotExists bool
}

func (textOp) isEditOp()   {}
func (createOp) isEditOp() {}
func (renameOp) isEditOp() {}
func (deleteOp) isEditOp() {}

// operations flattens the edit into an ordered list. The map form is sorted
// by URI so results are deterministic.
func (e WorkspaceEdit) operations() ([]editOp, error) {
	var ops []editOp
	if len(e.DocumentChanges) > 0 {
		for _, raw := range e.DocumentChanges {
			var dc struct {
				Kind         string `json:"kind"`
				URI          string `json:"uri"`
				OldURI       string `json:"oldUri"`
				NewURI       string `json:"newUri"`
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				Edits   []TextEdit `json:"edits"`
				Options struct {
					Overwrite         bool `json:"overwrite"`
					IgnoreIfExists    bool `json:"ignoreIfExists"`
					IgnoreIfNotExists bool `json:"ignoreIfNotExists"`
				} `json:"options"`
			}
			if err := json.Unmarshal(raw, &dc); err != nil {
				return nil, fmt.Errorf("decoding documentChanges entry: %w", err)
			}
			switch dc.Kind {
			case "":
				path, err := localPath(dc.TextDocument.URI)
				if err != nil {
					return nil, err
				}
				ops = append(ops, textOp{path: path, edits: dc.Edits})
			case "create":
				path, err := localPath(dc.URI)
				if err != nil {
					return nil, err
				}
				ops = append(ops, createOp{path: path, overwrite: dc.Options.Overwrite, ignoreIfExists: dc.Options.IgnoreIfExists})
			case "rename":
				from, err := localPath(dc.OldURI)
				if err != nil {
					return nil, err
				}
				to, err := localPath(dc.NewURI)
				if err != nil {
					return nil, err
				}
				ops = append(ops, renameOp{from: from, to: to, overwrite: dc.Options.Overwrite, ignoreIfExists: dc.Options.IgnoreIfExists})
			case "delete":
				path, err := localPath(dc.URI)
				if err != nil {
					return nil, err
				}
				ops = append(ops, deleteOp{path: path, ignoreIfNotExists: dc.Options.IgnoreIfNotExists})
			default:
				return nil, fmt.Errorf("unsupported documentChanges kind %q", dc.Kind)
			}
		}
		return ops, nil
	}
	uris := make([]string, 0, len(e.Changes))
	for uri := range e.Changes {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	for _, uri := range uris {
		path, err := localPath(uri)
		if err != nil {
			return nil, err
		}
		ops = append(ops, textOp{path: path, edits: e.Changes[uri]})
	}
	return ops, nil
}

func localPath(uri string) (string, error) {
	p, ok := uriToPath(uri)
	if !ok {
		return "", fmt.Errorf("refusing to edit non-file URI %q", uri)
	}
	return p, nil
}

// fileState is the in-memory result for one path while a plan is built.
type fileState struct {
	path    string
	orig    string
	existed bool
	content string
	exists  bool
}

type plan struct {
	files map[string]*fileState
	order []string
}

func (p *plan) get(path string) (*fileState, error) {
	if fs, ok := p.files[path]; ok {
		return fs, nil
	}
	st := &fileState{path: path}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		st.existed, st.exists = true, true
		st.orig = string(data)
		st.content = st.orig
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, err
	}
	p.files[path] = st
	p.order = append(p.order, path)
	return st, nil
}

func (p *plan) apply(op editOp) error {
	switch op := op.(type) {
	case textOp:
		st, err := p.get(op.path)
		if err != nil {
			return err
		}
		if !st.exists {
			return fmt.Errorf("workspace edit targets %s, which does not exist", op.path)
		}
		st.content, err = applyTextEdits(st.content, op.edits)
		return err
	case createOp:
		st, err := p.get(op.path)
		if err != nil {
			return err
		}
		if st.exists && op.ignoreIfExists {
			return nil
		}
		if st.exists && !op.overwrite {
			return fmt.Errorf("workspace edit creates %s, which already exists", op.path)
		}
		st.exists, st.content = true, ""
	case renameOp:
		src, err := p.get(op.from)
		if err != nil {
			return err
		}
		dst, err := p.get(op.to)
		if err != nil {
			return err
		}
		if !src.exists {
			return fmt.Errorf("workspace edit renames %s, which does not exist", op.from)
		}
		if dst.exists && !op.overwrite {
			if op.ignoreIfExists {
				return nil
			}
			return fmt.Errorf("workspace edit renames onto %s, which already exists", op.to)
		}
		dst.exists, dst.content = true, src.content
		src.exists, src.content = false, ""
	case deleteOp:
		st, err := p.get(op.path)
		if err != nil {
			return err
		}
		if !st.exists {
			if op.ignoreIfNotExists {
				return nil
			}
			return fmt.Errorf("workspace edit deletes %s, which does not exist", op.path)
		}
		st.exists, st.content = false, ""
	}
	return nil
}

// applyTextEdits applies edits that all refer to the text as it was before
// the batch. Edits are applied from the end of the file backwards so earlier
// offsets stay valid.
func applyTextEdits(text string, edits []TextEdit) (string, error) {
	type span struct {
		start, end int
		text       string
	}
	spans := make([]span, 0, len(edits))
	for _, e := range edits {
		s := offsetFromPosition(text, e.Range.Start)
		en := offsetFromPosition(text, e.Range.End)
		if en < s {
			s, en = en, s
		}
		spans = append(spans, span{s, en, e.NewText})
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end < spans[j].end
	})
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return "", errors.New("workspace edit has overlapping text edits in one file")
		}
	}
	for i := len(spans) - 1; i >= 0; i-- {
		sp := spans[i]
		text = text[:sp.start] + sp.text + text[sp.end:]
	}
	return text, nil
}

func (p *plan) commit(before func(string)) (ApplyResult, error) {
	var res ApplyResult
	var removals []*fileState
	for _, path := range p.order {
		st := p.files[path]
		switch {
		case st.exists && (!st.existed || st.content != st.orig):
			if before != nil {
				before(path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return res, err
			}
			if err := os.WriteFile(path, []byte(st.content), 0o644); err != nil {
				return res, fmt.Errorf("writing %s: %w", path, err)
			}
			res.Changes = append(res.Changes, FileChange{Path: path, Before: st.orig, After: st.content, Created: !st.existed})
		case !st.exists && st.existed:
			removals = append(removals, st)
		}
	}
	for _, st := range removals {
		if before != nil {
			before(st.path)
		}
		if err := os.Remove(st.path); err != nil {
			return res, fmt.Errorf("removing %s: %w", st.path, err)
		}
		res.Changes = append(res.Changes, FileChange{Path: st.path, Before: st.orig, Deleted: true})
	}
	return res, nil
}
