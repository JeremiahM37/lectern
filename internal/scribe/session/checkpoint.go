package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Checkpoints record what each agent turn changed on disk, so the turn can
// be undone and redone. It implements tools.FileTracker.
//
// Design notes (versus per-turn git tags):
//   - Nothing touches git. The index, stash and tags are never read or written,
//     so staged changes and the user's own edits are safe.
//   - Files are stored as raw bytes, content-addressed by SHA-256 under
//     blobs/, so binary files restore exactly, including their mode.
//   - Each file gets a "before" state captured the first time a turn writes it
//     and an "after" state captured when the turn ends. Undo compares the
//     file with "after" first, so edits made outside the agent are reported
//     as conflicts rather than silently overwritten.
//   - Undo first saves the current state of the files it changes, so Redo can
//     put them back exactly.
//   - A turn that wrote no files leaves no checkpoint.
type Checkpoints struct {
	root string // project root, for relative keys
	dir  string // session directory (holds checkpoints.json and blobs/)

	mu   sync.Mutex
	st   cpState
	open *cpRecord // the turn in progress, nil between turns
	err  error     // first capture or persist error since the last End
}

// Checkpoint describes one turn that changed files.
type Checkpoint struct {
	Turn    int
	Prompt  string
	Files   []string // paths relative to the project root, sorted
	Created time.Time
	Undone  bool
	// Added and Removed are the text lines the turn added and removed across
	// its files, for the checkpoint rail. Binary files and files over
	// lineDeltaMaxLines count as zero.
	Added   int
	Removed int
}

// lineDeltaMaxLines bounds the diff List runs per file; a larger file is
// not worth an O(n*m) diff on every rail refresh.
const lineDeltaMaxLines = 5000

// ErrConflict means files changed on disk after the turn that wrote them.
var ErrConflict = errors.New("files changed since the turn that wrote them")

// ErrNothingToUndo and ErrNothingToRedo report an empty stack.
var (
	ErrNothingToUndo = errors.New("nothing to undo")
	ErrNothingToRedo = errors.New("nothing to redo")
	ErrTurnRunning   = errors.New("a turn is still running")
)

// ConflictError lists the files that conflict. It matches ErrConflict.
type ConflictError struct {
	Files []string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: %v (use force to overwrite them)", ErrConflict, e.Files)
}

// Is makes errors.Is(err, ErrConflict) true.
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// fileState is a file as it was at a point in time. SHA is empty when the
// file did not exist.
type fileState struct {
	Exists bool   `json:"exists"`
	Mode   uint32 `json:"mode,omitempty"`
	SHA    string `json:"sha,omitempty"`
}

func sameContent(a, b fileState) bool { return a.Exists == b.Exists && a.SHA == b.SHA }

type cpFile struct {
	Before fileState  `json:"before"`
	After  *fileState `json:"after,omitempty"` // nil while open or if capture failed
}

type cpRecord struct {
	Turn    int                `json:"turn"`
	Prompt  string             `json:"prompt"`
	Created time.Time          `json:"created"`
	Undone  bool               `json:"undone,omitempty"`
	Files   map[string]*cpFile `json:"files"`
}

// redoOp undoes one undo operation. Pre is what the files held when Undo ran;
// Left is what Undo left them as. Redo only proceeds when they still match Left.
type redoOp struct {
	Turns []int                `json:"turns"`
	Files map[string]*redoFile `json:"files"`
}

type redoFile struct {
	Pre  fileState `json:"pre"`
	Left fileState `json:"left"`
}

// readRec is what the model saw when it last read a file.
type readRec struct {
	MTime int64  `json:"mtime"`
	Size  int64  `json:"size"`
	SHA   string `json:"sha"`
}

type cpState struct {
	Version     int                `json:"version"`
	Checkpoints []*cpRecord        `json:"checkpoints"`
	Redo        []*redoOp          `json:"redo,omitempty"`
	Reads       map[string]readRec `json:"reads,omitempty"`
}

const cpStateVersion = 1

// NewCheckpoints loads the checkpoints of session id. A corrupt
// checkpoints.json is set aside (as checkpoints.json.corrupt) and an empty
// set returned along with an error describing it; the returned value is
// usable either way. A turn left open by a crash is finished using the files'
// current contents.
func NewCheckpoints(s *Store, id string) (*Checkpoints, error) {
	c := &Checkpoints{root: s.Root, dir: s.Dir(id)}
	c.st.Version = cpStateVersion
	c.st.Reads = map[string]readRec{}
	path := filepath.Join(c.dir, "checkpoints.json")
	b, err := os.ReadFile(path)
	var loadErr error
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		loadErr = err
	default:
		if uerr := json.Unmarshal(b, &c.st); uerr != nil {
			loadErr = fmt.Errorf("checkpoints.json is corrupt, starting empty (old file kept as checkpoints.json.corrupt): %w", uerr)
			_ = os.Rename(path, path+".corrupt")
			c.st = cpState{Version: cpStateVersion, Reads: map[string]readRec{}}
		}
	}
	if c.st.Reads == nil {
		c.st.Reads = map[string]readRec{}
	}
	// Finish a turn that a crash left open. Its files are whatever is on disk.
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.st.Checkpoints {
		if r.Files == nil {
			r.Files = map[string]*cpFile{}
		}
		for k, f := range r.Files {
			if f.After == nil {
				if st, err := c.snapshot(c.abs(k), true); err == nil {
					f.After = &st
				}
			}
		}
	}
	if loadErr != nil {
		return c, loadErr
	}
	return c, c.saveLocked()
}

func (c *Checkpoints) abs(key string) string {
	if filepath.IsAbs(key) {
		return key
	}
	return filepath.Join(c.root, filepath.FromSlash(key))
}

func (c *Checkpoints) key(abs string) string {
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(c.root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

func (c *Checkpoints) blobPath(sha string) string {
	return filepath.Join(c.dir, "blobs", sha)
}

// snapshot reads abs and returns its state. With writeBlob it also stores the
// contents as a blob. A missing file is {Exists:false} with no error.
func (c *Checkpoints) snapshot(abs string, writeBlob bool) (fileState, error) {
	st, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	if st.IsDir() {
		return fileState{}, fmt.Errorf("%s is a directory", abs)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fileState{}, err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if writeBlob {
		if _, err := os.Stat(c.blobPath(sha)); errors.Is(err, fs.ErrNotExist) {
			if err := atomicWrite(c.blobPath(sha), data, 0o600); err != nil {
				return fileState{}, err
			}
		}
	}
	return fileState{Exists: true, Mode: uint32(st.Mode().Perm()), SHA: sha}, nil
}

func (c *Checkpoints) readBlob(s fileState) ([]byte, error) {
	if !s.Exists {
		return nil, nil
	}
	return os.ReadFile(c.blobPath(s.SHA))
}

// saveLocked persists the state. Caller holds c.mu.
func (c *Checkpoints) saveLocked() error { return c.writeState(c.st) }

func (c *Checkpoints) writeState(st cpState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(c.dir, "checkpoints.json"), append(b, '\n'), 0o600)
}

// readCurrent returns whether abs exists and its bytes, without writing a blob.
func (c *Checkpoints) readCurrent(abs string) (bool, []byte, error) {
	st, err := c.snapshot(abs, false)
	if err != nil || !st.Exists {
		return false, nil, err
	}
	data, err := os.ReadFile(abs)
	return true, data, err
}

func (c *Checkpoints) recordErrLocked(err error) {
	if err != nil && c.err == nil {
		c.err = err
	}
}

// Begin starts the checkpoint for turn. A turn still open is ended first.
func (c *Checkpoints) Begin(turn int, prompt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open != nil {
		c.endLocked()
	}
	c.open = &cpRecord{Turn: turn, Prompt: prompt, Created: time.Now().UTC(), Files: map[string]*cpFile{}}
}

// BeforeWrite snapshots abs before the agent changes it. The first call in a
// turn for a given file records its state; later calls are no-ops. Outside a
// turn it does nothing.
func (c *Checkpoints) BeforeWrite(abs string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open == nil {
		return
	}
	k := c.key(abs)
	if _, ok := c.open.Files[k]; ok {
		return
	}
	st, err := c.snapshot(filepath.Clean(abs), true)
	if err != nil {
		// Without a before-state the file cannot be undone; End reports it.
		c.recordErrLocked(fmt.Errorf("checkpoint: cannot snapshot %s: %w", k, err))
		return
	}
	c.open.Files[k] = &cpFile{Before: st}
	c.recordErrLocked(c.saveOpenLocked())
}

// saveOpenLocked persists the open turn alongside the finished ones, so a
// crash mid-turn still leaves something to undo.
func (c *Checkpoints) saveOpenLocked() error {
	if c.open == nil {
		return nil
	}
	st := c.st
	st.Checkpoints = append(append([]*cpRecord(nil), c.st.Checkpoints...), c.open)
	return c.writeState(st)
}

// End closes the open turn. Turns that wrote no files are dropped. Otherwise
// the after-state of each file is recorded, undone turns are discarded (their
// redo data is no longer valid), and the redo stack is cleared. It returns the
// first error seen during the turn.
func (c *Checkpoints) End() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endLocked()
	err := c.err
	c.err = nil
	return err
}

func (c *Checkpoints) endLocked() {
	cur := c.open
	c.open = nil
	if cur == nil {
		return
	}
	if len(cur.Files) == 0 {
		return
	}
	for k, f := range cur.Files {
		st, err := c.snapshot(c.abs(k), true)
		if err != nil {
			c.recordErrLocked(fmt.Errorf("checkpoint: cannot record %s after turn %d: %w", k, cur.Turn, err))
			continue
		}
		f.After = &st
	}
	kept := c.st.Checkpoints[:0]
	for _, r := range c.st.Checkpoints {
		if !r.Undone {
			kept = append(kept, r)
		}
	}
	c.st.Checkpoints = append(kept, cur)
	c.st.Redo = nil
	c.recordErrLocked(c.saveLocked())
}

// MarkRead records that the model has read abs as it is now.
func (c *Checkpoints) MarkRead(abs string) {
	abs = filepath.Clean(abs)
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return
	}
	sum := sha256.Sum256(data)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.st.Reads[c.key(abs)] = readRec{MTime: st.ModTime().UnixNano(), Size: st.Size(), SHA: hex.EncodeToString(sum[:])}
	c.recordErrLocked(c.saveLocked())
}

// StaleSinceRead reports whether abs changed since the model read it. A file
// that was never read is not stale. A file that is now gone is.
func (c *Checkpoints) StaleSinceRead(abs string) bool {
	abs = filepath.Clean(abs)
	c.mu.Lock()
	rec, ok := c.st.Reads[c.key(abs)]
	c.mu.Unlock()
	if !ok {
		return false
	}
	st, err := os.Stat(abs)
	if err != nil {
		return true
	}
	if st.Size() == rec.Size && st.ModTime().UnixNano() == rec.MTime {
		return false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return true
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) != rec.SHA
}

// WasRead reports whether the model has read abs in this session.
func (c *Checkpoints) WasRead(abs string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.st.Reads[c.key(filepath.Clean(abs))]
	return ok
}

var _ tools.FileTracker = (*Checkpoints)(nil)

// List returns the turns that changed files, oldest first.
func (c *Checkpoints) List() []Checkpoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	recs := c.st.Checkpoints
	if c.open != nil {
		recs = append(append([]*cpRecord(nil), recs...), c.open)
	}
	out := make([]Checkpoint, 0, len(recs))
	for _, r := range recs {
		v := viewOf(r)
		v.Added, v.Removed = c.lineDeltaLocked(r)
		out = append(out, v)
	}
	return out
}

// lineDeltaLocked counts the lines a turn added and removed, comparing each
// file's before-state with its after-state (or the disk, for an open turn).
// Caller holds c.mu.
func (c *Checkpoints) lineDeltaLocked(r *cpRecord) (added, removed int) {
	for k, f := range r.Files {
		before, err := c.readBlob(f.Before)
		if err != nil || looksBinary(before) {
			continue
		}
		var after []byte
		if f.After != nil {
			after, err = c.readBlob(*f.After)
		} else {
			_, after, err = c.readCurrent(c.abs(k))
		}
		if err != nil || looksBinary(after) {
			continue
		}
		a, b := splitKeepEOL(string(before)), splitKeepEOL(string(after))
		if len(a) > lineDeltaMaxLines || len(b) > lineDeltaMaxLines {
			continue
		}
		for _, op := range diffOps(a, b) {
			switch op.kind {
			case '+':
				added++
			case '-':
				removed++
			}
		}
	}
	return added, removed
}

// looksBinary reports whether data has a NUL byte in its first 8 KiB, the
// same heuristic git uses.
func looksBinary(data []byte) bool {
	if len(data) > 8192 {
		data = data[:8192]
	}
	return bytes.IndexByte(data, 0) >= 0
}

func viewOf(r *cpRecord) Checkpoint {
	files := make([]string, 0, len(r.Files))
	for k := range r.Files {
		files = append(files, k)
	}
	sort.Strings(files)
	return Checkpoint{Turn: r.Turn, Prompt: r.Prompt, Files: files, Created: r.Created, Undone: r.Undone}
}

// Undo reverts the files of the latest turn that is not undone. It returns
// that turn's number. If a file changed after the turn wrote it, Undo returns a
// *ConflictError (matching ErrConflict) and changes nothing, unless force.
func (c *Checkpoints) Undo(force bool) ([]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open != nil {
		return nil, ErrTurnRunning
	}
	var target *cpRecord
	for i := len(c.st.Checkpoints) - 1; i >= 0; i-- {
		if r := c.st.Checkpoints[i]; !r.Undone {
			target = r
			break
		}
	}
	if target == nil {
		return nil, ErrNothingToUndo
	}
	return c.applyLocked([]*cpRecord{target}, force)
}

// RestoreTo reverts every turn after turn (Turn > turn), newest first, as one
// undo operation. It returns the turns reverted; there may be none.
func (c *Checkpoints) RestoreTo(turn int, force bool) ([]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open != nil {
		return nil, ErrTurnRunning
	}
	var targets []*cpRecord
	for i := len(c.st.Checkpoints) - 1; i >= 0; i-- {
		if r := c.st.Checkpoints[i]; !r.Undone && r.Turn > turn {
			targets = append(targets, r)
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	return c.applyLocked(targets, force)
}

// applyLocked reverts targets (newest first). Caller holds c.mu.
func (c *Checkpoints) applyLocked(targets []*cpRecord, force bool) ([]int, error) {
	// The state each file must still be in: the after-state recorded by the
	// newest target that touched it. Unknown after-states are not checked.
	expected := map[string]*fileState{}
	for _, t := range targets {
		for k, f := range t.Files {
			if _, seen := expected[k]; !seen {
				expected[k] = f.After
			}
		}
	}
	if !force {
		var conflicts []string
		for k, after := range expected {
			if after == nil {
				continue
			}
			cur, err := c.snapshot(c.abs(k), false)
			if err != nil {
				return nil, fmt.Errorf("checkpoint: cannot read %s: %w", k, err)
			}
			if !sameContent(cur, *after) {
				conflicts = append(conflicts, k)
			}
		}
		if len(conflicts) > 0 {
			sort.Strings(conflicts)
			return nil, &ConflictError{Files: conflicts}
		}
	}

	// The state to leave each file in: the before-state of the oldest target
	// that touched it. Targets are newest first, so the last assignment wins.
	final := map[string]fileState{}
	for _, t := range targets {
		for k, f := range t.Files {
			final[k] = f.Before
		}
	}

	// Everything needed must be on disk before any file is touched.
	for k, st := range final {
		if st.Exists {
			if _, err := os.Stat(c.blobPath(st.SHA)); err != nil {
				return nil, fmt.Errorf("checkpoint: missing snapshot for %s (%w); nothing was changed", k, err)
			}
		}
	}

	// Save what is there now, so Redo can put it back.
	pre := map[string]fileState{}
	for k := range expected {
		st, err := c.snapshot(c.abs(k), true)
		if err != nil {
			return nil, fmt.Errorf("checkpoint: cannot save current %s before undo: %w", k, err)
		}
		pre[k] = st
	}

	for k, st := range final {
		if err := c.restoreFile(c.abs(k), st); err != nil {
			return nil, fmt.Errorf("checkpoint: restoring %s: %w", k, err)
		}
	}

	op := &redoOp{Files: map[string]*redoFile{}}
	for k := range final {
		op.Files[k] = &redoFile{Pre: pre[k], Left: final[k]}
	}
	turns := make([]int, 0, len(targets))
	for _, t := range targets {
		t.Undone = true
		op.Turns = append(op.Turns, t.Turn)
		turns = append(turns, t.Turn)
	}
	c.st.Redo = append(c.st.Redo, op)
	return turns, c.saveLocked()
}

// Redo re-applies the most recent undo operation. The files must still hold
// the state Undo left them in, or it returns a *ConflictError unless force.
// It returns the turns re-applied.
func (c *Checkpoints) Redo(force bool) ([]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open != nil {
		return nil, ErrTurnRunning
	}
	if len(c.st.Redo) == 0 {
		return nil, ErrNothingToRedo
	}
	op := c.st.Redo[len(c.st.Redo)-1]
	if !force {
		var conflicts []string
		for k, f := range op.Files {
			cur, err := c.snapshot(c.abs(k), false)
			if err != nil {
				return nil, fmt.Errorf("checkpoint: cannot read %s: %w", k, err)
			}
			if !sameContent(cur, f.Left) {
				conflicts = append(conflicts, k)
			}
		}
		if len(conflicts) > 0 {
			sort.Strings(conflicts)
			return nil, &ConflictError{Files: conflicts}
		}
	}
	for k, f := range op.Files {
		if f.Pre.Exists {
			if _, err := os.Stat(c.blobPath(f.Pre.SHA)); err != nil {
				return nil, fmt.Errorf("checkpoint: missing snapshot for %s (%w); nothing was changed", k, err)
			}
		}
	}
	for k, f := range op.Files {
		if err := c.restoreFile(c.abs(k), f.Pre); err != nil {
			return nil, fmt.Errorf("checkpoint: restoring %s: %w", k, err)
		}
	}
	c.st.Redo = c.st.Redo[:len(c.st.Redo)-1]
	for _, turn := range op.Turns {
		if r := c.findLocked(turn); r != nil {
			r.Undone = false
		}
	}
	return op.Turns, c.saveLocked()
}

func (c *Checkpoints) findLocked(turn int) *cpRecord {
	for _, r := range c.st.Checkpoints {
		if r.Turn == turn {
			return r
		}
	}
	return nil
}

// restoreFile makes abs match st: its exact bytes and mode, or absent.
func (c *Checkpoints) restoreFile(abs string, st fileState) error {
	if !st.Exists {
		if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := os.ReadFile(c.blobPath(st.SHA))
	if err != nil {
		return err
	}
	perm := fs.FileMode(st.Mode)
	if perm == 0 {
		perm = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if info, err := os.Stat(abs); err == nil {
		// Make it writable first if the user made it read-only.
		if err := os.Chmod(abs, info.Mode().Perm()|0o200); err != nil {
			return err
		}
	}
	if err := os.WriteFile(abs, data, perm); err != nil {
		return err
	}
	return os.Chmod(abs, perm)
}

// Diff returns a unified diff of what turn changed, from its before-state
// to its after-state (or to the current file when the after-state is
// unknown). Binary files are reported, not diffed.
func (c *Checkpoints) Diff(turn int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.findLocked(turn)
	if r == nil && c.open != nil && c.open.Turn == turn {
		r = c.open
	}
	if r == nil {
		return "", fmt.Errorf("no checkpoint for turn %d", turn)
	}
	var out bytes.Buffer
	for _, k := range viewOf(r).Files {
		f := r.Files[k]
		before, err := c.readBlob(f.Before)
		if err != nil {
			return "", fmt.Errorf("checkpoint: reading snapshot of %s: %w", k, err)
		}
		var after []byte
		afterExists := false
		if f.After != nil {
			afterExists = f.After.Exists
			after, err = c.readBlob(*f.After)
		} else {
			// The turn is still open: compare with what is on disk now.
			var cur []byte
			afterExists, cur, err = c.readCurrent(c.abs(k))
			after = cur
		}
		if err != nil {
			return "", fmt.Errorf("checkpoint: reading %s: %w", k, err)
		}
		out.WriteString(fileDiff(k, f.Before.Exists, before, afterExists, after))
	}
	return out.String(), nil
}
