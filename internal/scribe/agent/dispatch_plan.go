package agent

import (
	"fmt"
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
