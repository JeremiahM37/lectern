package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// ForbiddenFile is the name of the forbidden-paths file, in the global
// config home and in each project's .scribe directory. It holds
// {"patterns": ["*.log", "secrets/**"]}.
const ForbiddenFile = "forbidden.json"

// ForbiddenPatterns is the one list of forbidden path patterns. File tools and
// the shell both use it, so a path blocked for one is blocked for the other.
// It merges, in order and without duplicates:
//
//   - the forbidden key of the merged config (global, then project),
//   - the global file <config home>/forbidden.json,
//   - the project file <root>/.scribe/forbidden.json.
//
// Patterns are returned as written. Matching (globs, case, path segments) is
// the caller's job. A file that exists but cannot be read or parsed is an
// error: failing open would expose the paths the user meant to hide. A
// missing file is not an error. An empty root skips the project file.
func ForbiddenPatterns(root string) ([]string, error) {
	c, err := Load(root)
	if err != nil {
		return nil, err
	}
	out := unionStrings(c.Forbidden, nil)
	out = unionStrings(out, SessionForbidden())
	files := []string{filepath.Join(Home(), ForbiddenFile)}
	if root != "" {
		files = append(files, filepath.Join(ProjectDir(root), ForbiddenFile))
	}
	for _, f := range files {
		list, err := readForbiddenFile(f)
		if err != nil {
			return nil, err
		}
		out = unionStrings(out, list)
	}
	return out, nil
}

// readForbiddenFile reads one {"patterns": [...]} file. A missing file gives
// nothing.
func readForbiddenFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read %s: %v. Fix or remove it; file access is paused until then.", path, err)
	}
	var doc struct {
		Patterns []string `json:"patterns"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON (%v). Fix or remove it; file access is paused until then.", path, err)
	}
	return unionStrings(nil, doc.Patterns), nil
}

// sessionForbidden holds the patterns added with /privacy session. They last
// for this process only and are never written to a file.
var (
	sessionForbiddenMu sync.Mutex
	sessionForbidden   []string
)

// SessionForbidden returns the patterns added for this session only.
func SessionForbidden() []string {
	sessionForbiddenMu.Lock()
	defer sessionForbiddenMu.Unlock()
	return append([]string(nil), sessionForbidden...)
}

// AddSessionForbidden adds a pattern for this process only. It reports false
// when the pattern is already there.
func AddSessionForbidden(pat string) bool {
	sessionForbiddenMu.Lock()
	defer sessionForbiddenMu.Unlock()
	for _, p := range sessionForbidden {
		if p == pat {
			return false
		}
	}
	sessionForbidden = append(sessionForbidden, pat)
	return true
}

// RemoveSessionForbidden drops a session-only pattern. It reports whether it
// was there.
func RemoveSessionForbidden(pat string) bool {
	sessionForbiddenMu.Lock()
	defer sessionForbiddenMu.Unlock()
	for i, p := range sessionForbidden {
		if p == pat {
			sessionForbidden = append(sessionForbidden[:i], sessionForbidden[i+1:]...)
			return true
		}
	}
	return false
}
