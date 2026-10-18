package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// dispatchPlan is a checked task list. ids names every task (given or
// generated) and deps[i] lists the tasks that task i waits for.
type dispatchPlan struct {
	ids  []string
	deps [][]int
}

// planDispatch checks task ids and depends_on references and returns the
// dependency graph. Duplicate ids, references to unknown ids and cycles are
// errors, worded so the model can correct the call.
func planDispatch(tasks []subTask) (dispatchPlan, error) {
	p := dispatchPlan{ids: make([]string, len(tasks)), deps: make([][]int, len(tasks))}
	taken := map[string]int{} // id -> task index
	for i, t := range tasks {
		id := strings.TrimSpace(t.ID)
		if id == "" {
			continue
		}
		if j, dup := taken[id]; dup {
			return p, fmt.Errorf("task ids must be unique: %q is used by tasks %d and %d", id, j+1, i+1)
		}
		taken[id] = i
		p.ids[i] = id
	}
	// Generated ids skip any id a task already uses, so they never clash.
	for i := range tasks {
		if p.ids[i] != "" {
			continue
		}
		for k := i + 1; ; k++ {
			id := fmt.Sprintf("task%d", k)
			if _, used := taken[id]; !used {
				taken[id] = i
				p.ids[i] = id
				break
			}
		}
	}
	for i, t := range tasks {
		for _, raw := range t.DependsOn {
			ref := strings.TrimSpace(raw)
			j, ok := taken[ref]
			if !ok {
				return p, fmt.Errorf("task %s depends on %q, which is not the id of any task in this dispatch", p.ids[i], ref)
			}
			if !slices.Contains(p.deps[i], j) {
				p.deps[i] = append(p.deps[i], j)
			}
		}
	}
	if cycle := findCycle(p.ids, p.deps); cycle != nil {
		return p, fmt.Errorf("dependency cycle: %s", strings.Join(cycle, " -> "))
	}
	return p, nil
}

// findCycle returns the ids along one dependency cycle ("a -> b -> a"), or
// nil when the graph is acyclic. A task that depends on itself is a cycle.
func findCycle(ids []string, deps [][]int) []string {
	const (
		unvisited = iota
		inProgress
		finished
	)
	state := make([]int, len(ids))
	var stack []int
	var visit func(i int) []int
	visit = func(i int) []int {
		state[i] = inProgress
		stack = append(stack, i)
		for _, j := range deps[i] {
			switch state[j] {
			case inProgress:
				k := slices.Index(stack, j)
				return append(slices.Clone(stack[k:]), j)
			case unvisited:
				if c := visit(j); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = finished
		return nil
	}
	for i := range ids {
		if state[i] != unvisited {
			continue
		}
		if c := visit(i); c != nil {
			names := make([]string, len(c))
			for k, idx := range c {
				names[k] = ids[idx]
			}
			return names
		}
	}
	return nil
}

// withPeerObjectives tells task i what the other tasks of its dispatch are
// doing, so it does not repeat their work. A single-task dispatch adds
// nothing.
func withPeerObjectives(task string, tasks []subTask, p dispatchPlan, i int) string {
	if len(tasks) < 2 {
		return task
	}
	var b strings.Builder
	b.WriteString(task)
	b.WriteString("\n\nOther agents in this dispatch, and their objectives (do not repeat their work):\n")
	for j, st := range tasks {
		if j == i {
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", p.ids[j], st.Role, firstLine(clip(st.Task, 160)))
	}
	return b.String()
}

// withDependencyReports appends the reports of task i's dependencies to its
// task text, so the subagent builds on what they found or changed. The
// dependencies must have finished (their done channels closed).
func withDependencyReports(task string, p dispatchPlan, i int, results []taskResult) string {
	if len(p.deps[i]) == 0 {
		return task
	}
	var b strings.Builder
	b.WriteString(task)
	b.WriteString("\n\nReports from the tasks this one depends on (complete; build on them):\n")
	for _, j := range p.deps[i] {
		rep := strings.TrimSpace(results[j].report)
		if rep == "" && results[j].err != nil {
			rep = "(no report: this task failed: " + results[j].err.Error() + ")"
		} else if rep == "" {
			rep = "(no report)"
		}
		fmt.Fprintf(&b, "\n### %s\n%s\n", p.ids[j], rep)
	}
	return b.String()
}

// mergeOverLimit folds explore tasks together until at most limit remain
// (spec 03 §3.1 rule 4). Code tasks, tasks with dependencies and tasks other
// tasks depend on are pinned. Of the rest, the last explore task is merged
// into the one with the most target files, as an "Also:" paragraph. It
// returns the remaining tasks and a note for each merge.
func mergeOverLimit(tasks []subTask, limit int) ([]subTask, []string, error) {
	out := slices.Clone(tasks)
	var notes []string
	for len(out) > limit {
		referenced := map[string]bool{}
		for _, t := range out {
			for _, d := range t.DependsOn {
				referenced[strings.TrimSpace(d)] = true
			}
		}
		var free []int // explore tasks that can be merged
		for i, t := range out {
			pinned := t.Role != "explore" || len(t.DependsOn) > 0 ||
				(strings.TrimSpace(t.ID) != "" && referenced[strings.TrimSpace(t.ID)])
			if !pinned {
				free = append(free, i)
			}
		}
		if len(free) < 2 {
			return nil, nil, fmt.Errorf("at most %d tasks per call; got %d, and only %d of them are explore tasks without dependencies that can be merged", limit, len(tasks), len(free))
		}
		target := free[0]
		for _, i := range free[1:] {
			if len(out[i].Files) > len(out[target].Files) {
				target = i
			}
		}
		from := free[len(free)-1]
		if from == target {
			from = free[len(free)-2]
		}
		src := out[from]
		dst := out[target]
		dst.Task += "\n\nAlso: " + src.Task
		dst.Files = append(slices.Clone(dst.Files), src.Files...)
		// The absorbed task's item is marked with the outcome of the task that
		// runs its work, so it keeps its id too (see Planner.MarkTask).
		dst.TaskID = joinTaskIDs(dst.TaskID, src.TaskID)
		out[target] = dst
		label := taskLabel(dst, target)
		out = slices.Delete(out, from, from+1)
		notes = append(notes, fmt.Sprintf("merged an explore task into the explore task %s (the dispatch allows %d tasks)", label, limit))
	}
	return out, notes, nil
}

// numberedItem matches a line that starts a numbered item ("3. edit the
// parser"). Spec 03 §3.1 counts these at the start of a line only.
var numberedItem = regexp.MustCompile(`^\d+\.`)

// splitMinItems is how many numbered items a code task may list before it is
// split (spec 03 §3.1 rule 5: more than 8).
const splitMinItems = 8

// splitOversizedCode splits each code task that has more than eight numbered
// items and exactly one target file (spec 03 §3.1 rule 5). The items are cut
// into two serial halves. The second half depends on the first, and it is
// told to read the file again before it edits, since part 1 has already
// changed it. Tasks that depended on the original id now wait for the second
// half, which finishes last. Both halves keep the task-list taskId, so the
// item follows the outcome of the final part to finish.
//
// A split adds a task, so a task is split only while the dispatch stays within
// limit. One that would go past the limit is left whole and a note says so.
func splitOversizedCode(tasks []subTask, limit int) ([]subTask, []string) {
	out := slices.Clone(tasks)
	var notes []string
	for i := 0; i < len(out); i++ {
		t := out[i]
		if t.Role != "code" || distinctTargets(t.Files) != 1 {
			continue
		}
		lines := strings.Split(t.Task, "\n")
		var items []int // line index of each numbered item
		for n, line := range lines {
			if numberedItem.MatchString(line) {
				items = append(items, n)
			}
		}
		if len(items) <= splitMinItems {
			continue
		}
		label := taskLabel(t, i)
		if len(out)+1 > limit {
			notes = append(notes, fmt.Sprintf("agent %s lists %d numbered items, but the dispatch is at its %d-task limit, so it was not split", label, len(items), limit))
			continue
		}
		taken := map[string]bool{}
		for _, o := range out {
			if id := strings.TrimSpace(o.ID); id != "" {
				taken[id] = true
			}
		}
		origID := strings.TrimSpace(t.ID)
		base := origID
		if base == "" {
			base = freeID(fmt.Sprintf("task%d", i+1), taken)
		}
		taken[base] = true
		secondID := freeID(base+"-2", taken)

		cut := items[len(items)/2]
		first := subTask{
			ID:           base,
			Task:         strings.TrimRight(strings.Join(lines[:cut], "\n"), "\n "),
			Role:         t.Role,
			ReturnFormat: t.ReturnFormat,
			Files:        slices.Clone(t.Files),
			DependsOn:    slices.Clone(t.DependsOn),
			TaskID:       t.TaskID,
		}
		// The second half keeps the header above the first item, so it still
		// knows the overall goal, then carries on with the items from cut on.
		rest := slices.Concat(lines[:items[0]], lines[cut:])
		second := subTask{
			ID:           secondID,
			Task:         splitPreamble + "\n\n" + strings.Join(rest, "\n"),
			Role:         t.Role,
			ReturnFormat: t.ReturnFormat,
			Files:        slices.Clone(t.Files),
			DependsOn:    []string{base},
			TaskID:       t.TaskID,
		}
		if origID != "" {
			for j := range out {
				if j != i && slices.ContainsFunc(out[j].DependsOn, func(d string) bool { return strings.TrimSpace(d) == origID }) {
					out[j].DependsOn = append(slices.Clone(out[j].DependsOn), secondID)
				}
			}
		}
		out = slices.Replace(out, i, i+1, first, second)
		notes = append(notes, fmt.Sprintf("split agent %s into two serial parts (%d numbered items): part 2 waits for part 1 and reads the file first", label, len(items)))
		i++ // the second half is not split again
	}
	return out, notes
}

// splitPreamble opens the second half of a split task. The second half runs
// on the file part 1 has already edited, so it is told to read it first.
const splitPreamble = "This is part 2 of 2 of the task below. Part 1 has already edited the same file.\nContinue from part 1; read the file first, then do the numbered items in this part."

// distinctTargets counts the files a task names, ignoring line ranges.
func distinctTargets(files []string) int {
	seen := map[string]bool{}
	for _, f := range files {
		seen[stripRange(f)] = true
	}
	return len(seen)
}

// freeID returns base, or base with the smallest -N suffix that taken does
// not hold.
func freeID(base string, taken map[string]bool) string {
	id := base
	for k := 2; taken[id]; k++ {
		id = fmt.Sprintf("%s-%d", base, k)
	}
	return id
}

// joinTaskIDs combines the task-list ids of two merged dispatch tasks into
// one comma-separated taskId. Empty ids are dropped.
func joinTaskIDs(a, b string) string {
	var ids []string
	for _, id := range []string{a, b} {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, ",")
}

// taskLabel names a task for a note: its id if it has one, else its position.
func taskLabel(t subTask, i int) string {
	if id := strings.TrimSpace(t.ID); id != "" {
		return id
	}
	return fmt.Sprintf("%d", i+1)
}

// claimConflicts warns about code targets that another tab has claimed (spec
// 03 §3.1 rule 3). others maps each file another tab holds to that tab's
// label, as the tool environment reports it. A warning is never a block: the
// task keeps its place and runs. Each task that names a claimed file gets the
// warning in claimNotes, which taskText hands to its subagent, and the
// returned notes name the same tasks for the dispatch result. The check runs
// after checkTargets, so dropped files are not warned about.
func claimConflicts(tasks []subTask, workdir string, others map[string]string) []string {
	if workdir == "" || len(others) == 0 {
		return nil
	}
	var notes []string
	for i := range tasks {
		if tasks[i].Role != "code" {
			continue
		}
		var hits []string
		seen := map[string]bool{}
		for _, f := range tasks[i].Files {
			abs := stripRange(f)
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(workdir, abs)
			}
			abs = filepath.Clean(abs)
			label, claimed := others[abs]
			if !claimed || seen[abs] {
				continue
			}
			seen[abs] = true
			rel := abs
			if r, err := filepath.Rel(workdir, abs); err == nil {
				rel = filepath.ToSlash(r)
			}
			hits = append(hits, fmt.Sprintf("%s (%s)", rel, label))
		}
		if len(hits) == 0 {
			continue
		}
		note := "Files claimed by other tabs: " + strings.Join(hits, ", ") +
			". Those tabs are editing these files now, so this task's edits may conflict with theirs. Avoid these files where you can, and re-read a file before you change it. This is a warning only; the task still runs."
		tasks[i].claimNotes = append(tasks[i].claimNotes, note)
		notes = append(notes, fmt.Sprintf("agent %d: %s", i+1, note))
	}
	return notes
}

// stripRange drops a trailing :start-end (or :line) from a target path.
func stripRange(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.LastIndex(p, ":"); i > 0 {
		tail := p[i+1:]
		if tail != "" && strings.Trim(tail, "0123456789-") == "" {
			return p[:i]
		}
	}
	return p
}

// splitRange reads a target path with a line range, "path:start-end" or
// "path:line". ok is false when the path has no range this reader understands;
// the caller then keeps the text as written.
func splitRange(p string) (path string, start, end int, ok bool) {
	p = strings.TrimSpace(p)
	i := strings.LastIndex(p, ":")
	if i <= 0 {
		return p, 0, 0, false
	}
	start, end, ok = parseSpan(p[i+1:])
	if !ok {
		return p, 0, 0, false
	}
	return p[:i], start, end, true
}

// parseSpan reads "start-end" or a single "line". Lines start at 1, and a
// reversed span is put in order rather than refused, since the reader only
// needs the lines the task means.
func parseSpan(tail string) (int, int, bool) {
	lo, hi, ranged := strings.Cut(tail, "-")
	start, err := strconv.Atoi(lo)
	if err != nil {
		return 0, 0, false
	}
	end := start
	if ranged {
		if end, err = strconv.Atoi(hi); err != nil {
			return 0, 0, false
		}
	}
	start, end = max(start, 1), max(end, 1)
	if end < start {
		start, end = end, start
	}
	return start, end, true
}

// describeFiles writes each target for a subagent. A target with a line range
// says which lines to read and edit, so the range is a hard hint rather than
// something the subagent has to infer from the path. Other targets are kept
// as the task wrote them.
func describeFiles(files []string) []string {
	out := make([]string, len(files))
	for i, f := range files {
		if path, start, end, ok := splitRange(f); ok {
			out[i] = fmt.Sprintf("%s (lines %d-%d: read and edit this range)", path, start, end)
			continue
		}
		out[i] = f
	}
	return out
}

// codeLockKeys are the file locks a code task takes: its targets with ranges
// dropped, once each, in a stable order. Two tasks on different ranges of one
// file share the file lock, and a task that names one file twice must not
// lock it twice.
func codeLockKeys(files []string) []string {
	keys := make([]string, 0, len(files))
	for _, f := range files {
		keys = append(keys, stripRange(f))
	}
	return sortedUnique(keys)
}

// fileExists reports whether path names an existing file or directory.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// sortedKeys returns the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// joinInts renders agent numbers as "1, 3".
func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
}
