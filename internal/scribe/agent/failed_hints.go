package agent

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// Failed-path hints (spec 03 §2.1 step 15; REAUDIT3 #37). A call that fails
// because its path does not exist tends to be retried with the same path. The
// failure is queued here, and the next step carries a short reminder that
// names the path and, when the project has a file with a similar name, the
// closest matches. The reminder is a latched user notice like the task list:
// each path is hinted once per turn, and the system prompt is never touched.

const (
	// pathHintWalkLimit bounds how many directory entries one lookup reads.
	pathHintWalkLimit = 5000
	// pathHintSuggestions is how many similar names one hint offers.
	pathHintSuggestions = 3
	// pathHintPerStep bounds how many failed paths one reminder names. The
	// rest wait for the next step.
	pathHintPerStep = 3
	// pathHintMaxPending bounds the queue, so a run of failures cannot grow it
	// without limit.
	pathHintMaxPending = 16
)

// pathHints is the per-turn state of the failed-path channel. pending holds
// the paths that failed and have not been hinted yet, in failure order. shown
// holds the paths already hinted this turn.
type pathHints struct {
	pending []string
	shown   map[string]bool
}

// notePathResult records a finished call for the failed-path channel. A
// not-found failure queues the call's paths. A success on a path withdraws any
// hint still waiting for it, because the path exists now (the model may have
// created the file in the same step).
func (a *Agent) notePathResult(call, res llm.Part) {
	paths := failedPathArgs(call.Args)
	if len(paths) == 0 {
		return
	}
	notFound := res.IsError && isNotFound(res.Result)
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range paths {
		switch {
		case notFound:
			a.pathHints.queue(p)
		case !res.IsError:
			a.pathHints.withdraw(p)
		}
	}
}

// pathFailureHint returns the reminder for the paths queued since the last
// step, or "" when there are none. It takes at most pathHintPerStep paths and
// marks them shown, so they are never hinted again this turn.
func (a *Agent) pathFailureHint() string {
	a.mu.Lock()
	batch := a.pathHints.take(pathHintPerStep)
	a.mu.Unlock()
	if len(batch) == 0 {
		return ""
	}
	root := a.opts.Env.Workdir
	lines := make([]string, 0, len(batch))
	for _, p := range batch {
		lines = append(lines, pathHintLine(p, similarPaths(root, p, pathHintSuggestions, pathHintWalkLimit)))
	}
	return "<system-reminder>\n" + strings.Join(lines, "\n") + "\n</system-reminder>"
}

// queue adds a failed path unless it is already waiting or was hinted this
// turn.
func (h *pathHints) queue(p string) {
	if h.shown[p] || len(h.pending) >= pathHintMaxPending {
		return
	}
	for _, q := range h.pending {
		if q == p {
			return
		}
	}
	h.pending = append(h.pending, p)
}

// withdraw drops a waiting hint for p.
func (h *pathHints) withdraw(p string) {
	for i, q := range h.pending {
		if q == p {
			h.pending = append(h.pending[:i], h.pending[i+1:]...)
			return
		}
	}
}

// take removes up to n waiting paths, marks them shown and returns them.
func (h *pathHints) take(n int) []string {
	if len(h.pending) == 0 {
		return nil
	}
	if n > len(h.pending) {
		n = len(h.pending)
	}
	out := append([]string(nil), h.pending[:n]...)
	h.pending = append(h.pending[:0], h.pending[n:]...)
	if h.shown == nil {
		h.shown = map[string]bool{}
	}
	for _, p := range out {
		h.shown[p] = true
	}
	return out
}

// failedPathArgs returns the path arguments of a call: path, paths and file,
// the names the file tools use.
func failedPathArgs(args json.RawMessage) []string {
	var in struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
		File  string   `json:"file"`
	}
	_ = json.Unmarshal(args, &in)
	var out []string
	for _, p := range append([]string{in.Path, in.File}, in.Paths...) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// notFoundMarkers are the failure texts that mean a path is missing. They are
// specific on purpose: "old_string not found" from an edit means the file
// exists and the text does not, and must not be reported as a missing path.
var notFoundMarkers = []string{
	"file not found",
	"path not found",
	"directory not found",
	"no such file",
	"no such directory",
	"does not exist",
	"doesn't exist",
	"enoent",
}

// isNotFound reports whether a failure output says the path is missing.
func isNotFound(out string) bool {
	low := strings.ToLower(out)
	for _, m := range notFoundMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// pathHintLine is the reminder for one failed path. Without suggestions it
// points at the search tools instead.
func pathHintLine(p string, similar []string) string {
	if len(similar) == 0 {
		return "`" + p + "` does not exist. Use glob or list_dir to find the right path before trying again."
	}
	quoted := make([]string, len(similar))
	for i, s := range similar {
		quoted[i] = "`" + s + "`"
	}
	return "`" + p + "` does not exist. Did you mean " + joinOr(quoted) + "?"
}

// joinOr joins names as an English list: "a", "a or b", "a, b, or c".
func joinOr(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " or " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", or " + items[len(items)-1]
}

// similarPaths finds up to n files or directories under root whose base name
// is close to the base name of want, closest first. The walk skips hidden and
// dependency directories and stops after limit entries, so a lookup costs the
// same however large the project is. Paths come back relative to root, with
// forward slashes.
func similarPaths(root, want string, n, limit int) []string {
	if root == "" || strings.TrimSpace(want) == "" || n <= 0 {
		return nil
	}
	clean := filepath.Clean(want)
	base := strings.ToLower(filepath.Base(clean))
	maxDist := nameDistanceLimit(base)
	type match struct {
		rel  string
		dist int
	}
	var found []match
	visited := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		if d.IsDir() && skipHintDir(d.Name()) {
			return filepath.SkipDir
		}
		visited++
		if visited > limit {
			return fs.SkipAll
		}
		dist := levenshtein(base, strings.ToLower(d.Name()))
		if dist > maxDist {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == clean {
			return nil
		}
		found = append(found, match{filepath.ToSlash(rel), dist})
		return nil
	})
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].dist != found[j].dist {
			return found[i].dist < found[j].dist
		}
		if len(found[i].rel) != len(found[j].rel) {
			return len(found[i].rel) < len(found[j].rel)
		}
		return found[i].rel < found[j].rel
	})
	if len(found) > n {
		found = found[:n]
	}
	out := make([]string, len(found))
	for i, m := range found {
		out[i] = m.rel
	}
	return out
}

// nameDistanceLimit is how many edits a name may differ by and still count as
// similar: a third of its length, between 1 and 3.
func nameDistanceLimit(base string) int {
	d := len(base) / 3
	if d < 1 {
		return 1
	}
	if d > 3 {
		return 3
	}
	return d
}

// skipHintDir reports whether a directory is left out of the lookup. Hidden
// directories and the usual dependency trees hold files nobody meant.
func skipHintDir(name string) bool {
	switch name {
	case "node_modules", "vendor", "__pycache__":
		return true
	}
	return strings.HasPrefix(name, ".")
}
