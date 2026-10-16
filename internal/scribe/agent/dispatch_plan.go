package agent

import (
	"fmt"
	"os"
	"slices"
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
		if rep == "" {
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
		out[target] = dst
		label := taskLabel(dst, target)
		out = slices.Delete(out, from, from+1)
		notes = append(notes, fmt.Sprintf("merged an explore task into the explore task %s (the dispatch allows %d tasks)", label, limit))
	}
	return out, notes, nil
}

// taskLabel names a task for a note: its id if it has one, else its position.
func taskLabel(t subTask, i int) string {
	if id := strings.TrimSpace(t.ID); id != "" {
		return id
	}
	return fmt.Sprintf("%d", i+1)
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
