package symedit

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fileWrite is one planned change: the new content of rel, or the removal
// of rel. Paths are slash-separated and relative to the project root.
type fileWrite struct {
	rel     string
	content string
	remove  bool
}

// textEdit replaces src[start:end] with text.
type textEdit struct {
	start, end int
	text       string
}

// applyEdits applies non-overlapping edits to src in one pass.
func applyEdits(src string, edits []textEdit) string {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := src
	for _, e := range edits {
		out = out[:e.start] + e.text + out[e.end:]
	}
	return out
}

// commitWrites applies the planned changes in order. The first failure puts
// back every file already changed, so a move either happens whole or not at
// all. Each file is reported to the checkpoint tracker before it changes.
func commitWrites(env *tools.Env, workdir string, ws []fileWrite) error {
	type prior struct {
		abs     string
		orig    []byte
		existed bool
		perm    os.FileMode
	}
	var done []prior
	rollback := func() {
		for i := len(done) - 1; i >= 0; i-- {
			p := done[i]
			if p.existed {
				_ = os.MkdirAll(filepath.Dir(p.abs), 0o755)
				_ = os.WriteFile(p.abs, p.orig, p.perm)
			} else {
				_ = os.Remove(p.abs)
			}
		}
	}
	for _, w := range ws {
		abs := filepath.Join(workdir, filepath.FromSlash(w.rel))
		p := prior{abs: abs, perm: 0o644}
		if fi, err := os.Stat(abs); err == nil {
			if fi.IsDir() {
				rollback()
				return fmt.Errorf("%s is a directory", w.rel)
			}
			p.existed, p.perm = true, fi.Mode().Perm()
			if p.orig, err = os.ReadFile(abs); err != nil {
				rollback()
				return fmt.Errorf("read %s: %w", w.rel, err)
			}
		}
		if env != nil && env.Files != nil {
			env.Files.BeforeWrite(abs)
		}
		done = append(done, p)
		var err error
		if w.remove {
			err = os.Remove(abs)
		} else {
			if err = os.MkdirAll(filepath.Dir(abs), 0o755); err == nil {
				err = os.WriteFile(abs, []byte(w.content), p.perm)
			}
		}
		if err != nil {
			rollback()
			return fmt.Errorf("%s: %w", w.rel, err)
		}
		if !w.remove && env != nil && env.Files != nil {
			env.Files.MarkRead(abs)
		}
	}
	return nil
}

// skipDirs are never searched for references.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "__pycache__": true, ".venv": true}

// maxSweepBytes bounds the size of a file that is read for references.
const maxSweepBytes = 2 << 20

// projectFiles lists the files under root, slash-separated and relative to
// it. Tracked files come from git; without git every file is walked.
func projectFiles(root string) []string {
	if out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output(); err == nil {
		var files []string
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, filepath.ToSlash(f))
			}
		}
		return files
	}
	var files []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(root, p); err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files
}

// candidateFiles lists the files that may refer to name. When a code map is
// open and knows the name, it names them; otherwise every project file is a
// candidate.
func candidateFiles(env *tools.Env, workdir, name string) []string {
	if m, ok := tools.Service[*repomap.Map](env, "repomap"); ok && m != nil {
		if refs, err := m.References(name); err == nil && len(refs) > 0 {
			seen := map[string]bool{}
			var out []string
			for _, r := range refs {
				f := filepath.ToSlash(r.File)
				if !seen[f] {
					seen[f] = true
					out = append(out, f)
				}
			}
			sort.Strings(out)
			return out
		}
	}
	return projectFiles(workdir)
}

// readProjectText reads rel (slash-separated, project-relative). It returns
// false for files that are missing, binary or too large to be source.
func readProjectText(workdir, rel string) (string, bool) {
	abs := filepath.Join(workdir, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() || fi.Size() > maxSweepBytes {
		return "", false
	}
	data, err := os.ReadFile(abs)
	if err != nil || isBinaryData(data) {
		return "", false
	}
	return string(data), true
}

func isBinaryData(data []byte) bool {
	n := min(len(data), 8000)
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// extOf is the lower-case extension of a slash path.
func extOf(rel string) string { return strings.ToLower(path.Ext(rel)) }

// isTSFamily reports whether a language is TypeScript or JavaScript, whose
// modules can import each other.
func isTSFamily(l *language) bool {
	return l != nil && (l.name == "typescript" || l.name == "javascript")
}

// wordHits returns the offsets of whole-word occurrences of name in s.
func wordHits(s, name string) []int {
	var out []int
	for _, off := range allIndexes(s, name) {
		if off > 0 && isIdentByte(s[off-1]) {
			continue
		}
		if end := off + len(name); end < len(s) && isIdentByte(s[end]) {
			continue
		}
		out = append(out, off)
	}
	return out
}
