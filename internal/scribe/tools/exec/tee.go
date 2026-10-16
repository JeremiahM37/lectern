package exec

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// TeeDir overrides where tee files are written. Empty means the user data
// directory: $XDG_DATA_HOME/lectern/scribe/tee, or %LOCALAPPDATA% on Windows.
var TeeDir string

const (
	// teeMaxFiles is how many tee files are kept; the oldest are pruned.
	teeMaxFiles = 20
	// teeMaxBytes caps one tee file. Its head and tail are kept.
	teeMaxBytes = 512 << 10
)

// teeDir returns the tee directory, creating it.
func teeDir() (string, error) {
	dir := TeeDir
	if dir == "" {
		base := os.Getenv("XDG_DATA_HOME")
		switch {
		case runtime.GOOS == "windows" && os.Getenv("LOCALAPPDATA") != "":
			base = os.Getenv("LOCALAPPDATA")
		case base == "":
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "share")
		}
		dir = filepath.Join(base, "lectern", "scribe", "tee")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// writeTee saves text as a new tee file named after label, then prunes the
// directory to teeMaxFiles. It returns the file's path.
func writeTee(label, text string) (string, error) {
	dir, err := teeDir()
	if err != nil {
		return "", err
	}
	if len(text) > teeMaxBytes {
		half := teeMaxBytes / 2
		text = text[:half] + "\n… " + fmt.Sprint(len(text)-teeMaxBytes) + " bytes omitted from the middle …\n" + text[len(text)-half:]
	}
	name := fmt.Sprintf("%s-%d.txt", sanitizeLabel(label), time.Now().UnixNano())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	pruneTee(dir)
	return path, nil
}

// pruneTee removes the oldest tee files beyond teeMaxFiles.
func pruneTee(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		mod  time.Time
	}
	var files []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, item{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	if len(files) <= teeMaxFiles {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files[:len(files)-teeMaxFiles] {
		_ = os.Remove(f.path)
	}
}

// sanitizeLabel keeps a tee file name to letters, digits and dashes.
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "output"
	}
	return b.String()
}

// spill caps text at limit bytes for the model. When it had to cut, the full
// text is kept in a tee file and the returned body names that file. omitted
// is the number of lines dropped, as truncateTo reports it.
func spill(text string, limit int, label string) (body string, omitted int) {
	body, omitted = truncateTo(text, limit)
	if len(text) <= limit {
		return body, omitted
	}
	body = strings.TrimRight(body, "\n")
	if path, err := writeTee(label, text); err == nil {
		body += "\n[full output: " + path + "]"
	} else {
		body += "\n[full output could not be saved: " + err.Error() + "]"
	}
	return body, omitted
}
