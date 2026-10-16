package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const (
	undoPerFile  = 20
	undoMaxFiles = 200
)

// undoEntry is the state before one Scribe edit. afterHash fingerprints the
// content the edit produced, so undo can refuse when someone else changed the
// file since.
type undoEntry struct {
	before    []byte
	existed   bool
	afterHash string
	seq       uint64
}

// UndoStack holds per-file edit history for one session. The agent owns it
// and passes it to the constructors so every edit tool records into it.
// It is safe for concurrent use.
type UndoStack struct {
	mu     sync.Mutex
	stacks map[string][]undoEntry
	seq    uint64
}

// NewUndoStack returns an empty history.
func NewUndoStack() *UndoStack {
	return &UndoStack{stacks: map[string][]undoEntry{}}
}

func (s *UndoStack) push(abs string, e undoEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.seq = s.seq
	st := append(s.stacks[abs], e)
	if len(st) > undoPerFile {
		st = st[len(st)-undoPerFile:]
	}
	s.stacks[abs] = st
	if len(s.stacks) > undoMaxFiles {
		// Evict the file whose most recent edit is the oldest.
		var oldestKey string
		var oldest uint64 = ^uint64(0)
		for k, v := range s.stacks {
			if top := v[len(v)-1].seq; top < oldest {
				oldest, oldestKey = top, k
			}
		}
		delete(s.stacks, oldestKey)
	}
}

// top returns the most recent entry for abs without removing it.
func (s *UndoStack) top(abs string) (undoEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stacks[abs]
	if len(st) == 0 {
		return undoEntry{}, false
	}
	return st[len(st)-1], true
}

// drop removes the most recent entry for abs.
func (s *UndoStack) drop(abs string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stacks[abs]
	switch {
	case len(st) == 0:
	case len(st) == 1:
		delete(s.stacks, abs)
	default:
		s.stacks[abs] = st[:len(st)-1]
	}
}

// depth returns how many undo steps remain for abs.
func (s *UndoStack) depth(abs string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stacks[abs])
}

// NewUndoEdit returns undo_edit, which restores a file to the state before
// the most recent Scribe edit of it.
func NewUndoEdit(undo *UndoStack) tools.Tool {
	if undo == nil {
		undo = NewUndoStack()
	}
	return &tool{
		spec: newSpec("undo_edit",
			"Undo the most recent edit or write this session made to a file. Refused if the file changed since that edit.",
			tools.Schema(map[string]any{
				"path": tools.Str("File whose last edit to undo, relative to the project root."),
			}, "path")),
		run: func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
			var a struct {
				Path string `json:"path"`
			}
			if err := tools.Decode(args, &a); err != nil {
				return fail(err)
			}
			t, err := resolve(ctx, env, "undo_edit", a.Path, accessWrite)
			if err != nil {
				return fail(err)
			}
			e, ok := undo.top(t.abs)
			if !ok {
				return tools.Errorf("Nothing to undo for %s: no edit to it from this session is left.", t.rel)
			}
			cur, rerr := os.ReadFile(t.abs)
			if rerr != nil || hashBytes(cur) != e.afterHash {
				return tools.Errorf("%s changed since the last edit (by another agent or on disk), so undo was refused. Read it and revert your change with edit_file.", t.rel)
			}
			if env.Files != nil {
				env.Files.BeforeWrite(t.abs)
			}
			if e.existed {
				if err := writeAtomic(t.abs, e.before, existingPerm(t.abs)); err != nil {
					return tools.Errorf("restore %s: %v", t.rel, err)
				}
			} else if err := os.Remove(t.abs); err != nil {
				return tools.Errorf("remove %s: %v", t.rel, err)
			}
			undo.drop(t.abs)
			if e.existed && env.Files != nil {
				env.Files.MarkRead(t.abs) // our own write: the next edit must not look stale
			}

			diff, added, removed := UnifiedDiff(t.rel, toLF(string(cur)), toLF(string(e.before)))
			msg := fmt.Sprintf("Restored %s to its state before the last edit. %d earlier step(s) remain.", t.rel, undo.depth(t.abs))
			if !e.existed {
				msg = fmt.Sprintf("Removed %s (undoing its creation).", t.rel)
			}
			return tools.Result{
				Output:  msg,
				Diff:    diff,
				Files:   []string{t.rel},
				Summary: diffSummary(added, removed),
			}
		},
	}
}
