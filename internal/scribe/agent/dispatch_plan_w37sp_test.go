package agent

import (
	"fmt"
	"strings"
	"testing"
)

// w37spItems builds a code task whose header is followed by n numbered items.
func w37spItems(n int) string {
	var b strings.Builder
	b.WriteString("Rename the parser helpers in one pass.\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d. change step %d\n", i, i)
	}
	return b.String()
}

func TestW37SPOversizedCodeTaskIsSplitIntoTwoSerialHalves(t *testing.T) {
	task := subTask{ID: "a", Task: w37spItems(9), Role: "code", Files: []string{"parse.go"}, TaskID: "t7"}
	out, notes := splitOversizedCode([]subTask{task}, 8)
	if len(out) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(out))
	}
	if len(notes) != 1 {
		t.Fatalf("want one note, got %v", notes)
	}
	first, second := out[0], out[1]
	if first.ID != "a" {
		t.Errorf("first half id = %q, want the original a", first.ID)
	}
	if second.ID == "a" || second.ID == "" {
		t.Errorf("second half id = %q, want its own id", second.ID)
	}
	if !strings.Contains(first.Task, "4. change step 4") || strings.Contains(first.Task, "5. change step 5") {
		t.Errorf("first half should hold items 1-4 only:\n%s", first.Task)
	}
	if !strings.Contains(second.Task, "5. change step 5") || !strings.Contains(second.Task, "9. change step 9") {
		t.Errorf("second half should hold items 5-9:\n%s", second.Task)
	}
	if !strings.Contains(second.Task, "Continue from part 1; read the file first") {
		t.Errorf("second half lacks the read-first line:\n%s", second.Task)
	}
	if !strings.Contains(second.Task, "Rename the parser helpers in one pass.") {
		t.Errorf("second half lost the header:\n%s", second.Task)
	}
	if strings.Contains(first.Task, "part 2") {
		t.Errorf("first half should not carry the part 2 preamble:\n%s", first.Task)
	}
	if len(second.DependsOn) != 1 || second.DependsOn[0] != "a" {
		t.Errorf("second half depends on %v, want [a]", second.DependsOn)
	}
	for _, s := range out {
		if s.Role != "code" || len(s.Files) != 1 || s.Files[0] != "parse.go" {
			t.Errorf("split half lost role or file: %+v", s)
		}
		if s.TaskID != "t7" {
			t.Errorf("split half lost taskId linkage: %q", s.TaskID)
		}
	}
}

func TestW37SPSplitPlanStillResolves(t *testing.T) {
	tasks := []subTask{
		{ID: "a", Task: w37spItems(9), Role: "code", Files: []string{"parse.go"}},
		{ID: "b", Task: "review the result", Role: "explore", DependsOn: []string{"a"}},
	}
	out, _ := splitOversizedCode(tasks, 8)
	if _, err := planDispatch(out); err != nil {
		t.Fatalf("split plan should resolve: %v", err)
	}
	if got := out[len(out)-1].DependsOn; len(got) != 2 || got[0] != "a" || got[1] != "a-2" {
		t.Errorf("dependent should wait for both halves, got %v", got)
	}
}

func TestW37SPNormalTasksAreUnchanged(t *testing.T) {
	cases := map[string]subTask{
		"eight items":         {ID: "a", Task: w37spItems(8), Role: "code", Files: []string{"parse.go"}},
		"explore, nine items": {ID: "a", Task: w37spItems(9), Role: "explore", Files: []string{"parse.go"}},
		"two target files":    {ID: "a", Task: w37spItems(9), Role: "code", Files: []string{"parse.go", "lex.go"}},
		"no target file":      {ID: "a", Task: w37spItems(9), Role: "code"},
		"indented items only": {ID: "a", Task: strings.ReplaceAll(w37spItems(9), "\n", "\n  "), Role: "code", Files: []string{"parse.go"}},
	}
	for name, task := range cases {
		out, notes := splitOversizedCode([]subTask{task}, 8)
		if len(out) != 1 || len(notes) != 0 {
			t.Errorf("%s: split when it should not (%d tasks, notes %v)", name, len(out), notes)
			continue
		}
		if out[0].Task != task.Task || out[0].ID != task.ID || len(out[0].DependsOn) != 0 {
			t.Errorf("%s: task changed: %+v", name, out[0])
		}
	}
}

func TestW37SPRangedSingleFileCountsAsOneTarget(t *testing.T) {
	task := subTask{ID: "a", Task: w37spItems(9), Role: "code", Files: []string{"parse.go:1-50", "parse.go:90-120"}}
	out, _ := splitOversizedCode([]subTask{task}, 8)
	if len(out) != 2 {
		t.Fatalf("two ranges of one file should split, got %d tasks", len(out))
	}
}

func TestW37SPRespectsTheTaskLimit(t *testing.T) {
	tasks := []subTask{{ID: "a", Task: w37spItems(9), Role: "code", Files: []string{"parse.go"}}}
	for i := 0; i < 7; i++ {
		tasks = append(tasks, subTask{ID: fmt.Sprintf("e%d", i), Task: "look", Role: "explore"})
	}
	out, notes := splitOversizedCode(tasks, 8)
	if len(out) != 8 {
		t.Fatalf("a full dispatch should not grow, got %d", len(out))
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "not split") {
		t.Errorf("want a not-split note, got %v", notes)
	}
}

func TestW37SPGeneratedIDGetsAnExplicitIDSoPartsCanLink(t *testing.T) {
	task := subTask{Task: w37spItems(9), Role: "code", Files: []string{"parse.go"}}
	out, _ := splitOversizedCode([]subTask{task}, 8)
	if len(out) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(out))
	}
	if out[0].ID == "" || out[1].DependsOn[0] != out[0].ID {
		t.Fatalf("second half must depend on the first by id: %+v", out)
	}
	if _, err := planDispatch(out); err != nil {
		t.Fatalf("plan should resolve: %v", err)
	}
}
