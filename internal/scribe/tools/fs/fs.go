// Package fs holds Scribe's file tools: read, write_file, edit_file,
// multi_edit, undo_edit, list_dir, glob and grep.
//
// Every tool resolves its path through resolve (security.go), reports paths
// relative to the workdir, and records reads and writes on env.Files when it
// is set.
package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Register adds every file tool to r. Undo history is kept in a fresh
// UndoStack; use RegisterWithUndo to share one with the agent.
func Register(r *tools.Registry) {
	RegisterWithUndo(r, NewUndoStack())
}

// RegisterWithUndo adds every file tool to r, recording edits on undo.
func RegisterWithUndo(r *tools.Registry, undo *UndoStack) {
	r.Register(NewRead())
	r.Register(NewWriteFile(undo))
	r.Register(NewEditFile(undo))
	r.Register(NewMultiEdit(undo))
	r.Register(NewUndoEdit(undo))
	r.Register(NewListDir())
	r.Register(NewGlob())
	r.Register(NewGrep())
}

// tool adapts a run function to tools.Tool. Run recovers panics so a bug in
// one tool becomes an error result instead of killing the agent.
type tool struct {
	spec     llm.ToolSpec
	readOnly bool
	run      func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result
}

func (t *tool) Spec() llm.ToolSpec { return t.spec }
func (t *tool) ReadOnly() bool     { return t.readOnly }

func (t *tool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) (res tools.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = tools.Errorf("%s failed unexpectedly: %v", t.spec.Name, r)
		}
	}()
	if env == nil {
		return tools.Errorf("%s: no environment", t.spec.Name)
	}
	return t.run(ctx, env, args)
}

func newSpec(name, desc string, schema json.RawMessage) llm.ToolSpec {
	return llm.ToolSpec{Name: name, Description: desc, Schema: schema}
}

// fail converts an error into an error Result.
func fail(err error) tools.Result {
	return tools.Result{Output: err.Error(), IsError: true}
}

// flexInt accepts a JSON number or a numeric string. null leaves it zero.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("expected an integer, got %s", string(b))
	}
	*f = flexInt(n)
	return nil
}

// flexBool accepts a JSON bool or "true"/"false" in any case. null is false.
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.ToLower(strings.Trim(strings.TrimSpace(string(b)), `"`))
	switch s {
	case "", "null", "false":
		*f = false
	case "true":
		*f = true
	default:
		return fmt.Errorf("expected true or false, got %s", string(b))
	}
	return nil
}

// flexStrings accepts a string, an array of strings, or a JSON-encoded array
// held in a string.
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "" || s == "null":
		return nil
	case strings.HasPrefix(s, `"`):
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		if t := strings.TrimSpace(one); strings.HasPrefix(t, "[") {
			return json.Unmarshal([]byte(t), (*[]string)(f))
		}
		*f = flexStrings{one}
		return nil
	case strings.HasPrefix(s, "["):
		return json.Unmarshal(b, (*[]string)(f))
	}
	return fmt.Errorf("expected a string or an array of strings, got %s", s)
}

// decodeList decodes an array argument that may arrive as a JSON string.
func decodeList(raw json.RawMessage, v any) error {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return fmt.Errorf("edits is required")
	}
	if strings.HasPrefix(s, `"`) {
		var inner string
		if err := json.Unmarshal(raw, &inner); err != nil {
			return fmt.Errorf("invalid arguments: %w", err)
		}
		raw = json.RawMessage(inner)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// clipLine shortens text to max runes, marking the cut with "...".
func clipLine(text string, max int) string {
	if len(text) <= max {
		return text
	}
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	return string(r[:max]) + "..."
}

// humanSize formats a byte count for listings.
func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fK", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	}
}

// relSlash returns abs relative to wd with forward slashes. Paths outside wd
// keep their ".." prefix.
func relSlash(wd, abs string) string {
	r, err := filepath.Rel(wd, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(r)
}

// isBinaryData reports whether the first bytes contain a NUL.
func isBinaryData(b []byte) bool {
	n := len(b)
	if n > sniffBytes {
		n = sniffBytes
	}
	for _, c := range b[:n] {
		if c == 0 {
			return true
		}
	}
	return false
}

// countLines counts lines the way the editor shows them: a trailing newline
// does not start another line.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// crlfStyle reports whether a file's line endings are predominantly CRLF.
func crlfStyle(b []byte) bool {
	crlf := strings.Count(string(b), "\r\n")
	lf := strings.Count(string(b), "\n") - crlf
	return crlf > 0 && crlf >= lf
}

func toLF(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func fromLF(s string, crlf bool) string {
	if !crlf {
		return s
	}
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeAtomic replaces abs with data via a temp file in the same directory.
// Symlinks are followed so the link itself survives.
func writeAtomic(abs string, data []byte, perm os.FileMode) error {
	target := abs
	if p, err := filepath.EvalSymlinks(abs); err == nil {
		target = p
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".scribe-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	if err := os.Rename(name, target); err != nil {
		return err
	}
	done = true
	return nil
}

// existingPerm returns the permission bits of abs, or 0644 for a new file.
func existingPerm(abs string) os.FileMode {
	if fi, err := os.Stat(abs); err == nil {
		return fi.Mode().Perm()
	}
	return 0o644
}
