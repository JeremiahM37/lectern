package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanDispatchKeepsGivenIDsAndResolvesDependencies(t *testing.T) {
	tasks := []subTask{
		{ID: " alpha ", Task: "a"},
		{Task: "b"},
		{ID: "gamma", Task: "c", DependsOn: []string{"alpha", "task2"}},
	}
	p, err := planDispatch(tasks)
	if err != nil {
		t.Fatalf("planDispatch: %v", err)
	}
	if want := []string{"alpha", "task2", "gamma"}; !reflect.DeepEqual(p.ids, want) {
		t.Fatalf("ids = %v, want %v", p.ids, want)
	}
	if want := []int{0, 1}; !reflect.DeepEqual(p.deps[2], want) {
		t.Fatalf("deps of gamma = %v, want %v", p.deps[2], want)
	}
	if len(p.deps[0]) != 0 || len(p.deps[1]) != 0 {
		t.Fatalf("independent tasks have dependencies: %v", p.deps)
	}
}

func TestPlanDispatchGeneratedIDsNeverClashWithGivenIDs(t *testing.T) {
	// The second task has no id; "task2" is taken by the first, so it gets task3.
	p, err := planDispatch([]subTask{{ID: "task2", Task: "a"}, {Task: "b"}})
	if err != nil {
		t.Fatalf("planDispatch: %v", err)
	}
	if p.ids[0] != "task2" || p.ids[1] != "task3" {
		t.Fatalf("ids = %v", p.ids)
	}
}

func TestPlanDispatchRejectsBadIDsAndCycles(t *testing.T) {
	cases := []struct {
		name  string
		tasks []subTask
		want  string
	}{
		{
			name:  "duplicate id",
			tasks: []subTask{{ID: "x", Task: "a"}, {ID: "x", Task: "b"}},
			want:  `"x" is used by tasks 1 and 2`,
		},
		{
			name:  "unknown dependency",
			tasks: []subTask{{ID: "x", Task: "a", DependsOn: []string{"nope"}}},
			want:  `depends on "nope", which is not the id of any task`,
		},
		{
			name:  "self dependency",
			tasks: []subTask{{ID: "x", Task: "a", DependsOn: []string{"x"}}},
			want:  "dependency cycle: x -> x",
		},
		{
			name: "three-task cycle",
			tasks: []subTask{
				{ID: "a", Task: "1", DependsOn: []string{"c"}},
				{ID: "b", Task: "2", DependsOn: []string{"a"}},
				{ID: "c", Task: "3", DependsOn: []string{"b"}},
			},
			want: "dependency cycle: a -> c -> b -> a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planDispatch(tc.tasks)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestPlanDispatchAcceptsDiamond(t *testing.T) {
	// a -> (b, c) -> d is a DAG, not a cycle, even though d is reached twice.
	p, err := planDispatch([]subTask{
		{ID: "a", Task: "1"},
		{ID: "b", Task: "2", DependsOn: []string{"a"}},
		{ID: "c", Task: "3", DependsOn: []string{"a"}},
		{ID: "d", Task: "4", DependsOn: []string{"b", "c", "b"}},
	})
	if err != nil {
		t.Fatalf("diamond rejected: %v", err)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(p.deps[3], want) {
		t.Fatalf("deps of d = %v, want %v (duplicates removed)", p.deps[3], want)
	}
}

func TestStepBudgetsMatchSpec(t *testing.T) {
	// Spec 03 §3.5 (explore 12, code 18) and §3.9 (web search 15).
	if ExploreMaxSteps != 12 || CodeMaxSteps != 18 || WebMaxSteps != 15 {
		t.Fatalf("step budgets = %d/%d/%d, want 12/18/15", ExploreMaxSteps, CodeMaxSteps, WebMaxSteps)
	}
}
