package console

import (
	"fmt"
	"testing"
)

func recentTestDashboard() *dashboard {
	m := newDashboard(New("http://unused", ""), nil)
	m.section = 0
	m.rows = []row{{"id": float64(1), "name": "session"}}
	m.filter()
	return m
}

func projectField(t *testing.T, m *dashboard) field {
	t.Helper()
	if m.form == nil {
		t.Fatal("form did not open")
	}
	for _, f := range m.form.fields {
		if f.Key == "project_id" {
			return f
		}
	}
	t.Fatal("form has no project field")
	return field{}
}

func optionValues(f field) []string {
	out := make([]string, 0, len(f.Options))
	for _, c := range f.Options {
		out = append(out, c.Value)
	}
	return out
}

func TestRecentProjectsPersistAcrossDashboardInstances(t *testing.T) {
	isolateConsoleConfig(t)
	m := newDashboard(New("http://example.com/deck", ""), nil)
	m.loadPreferences()
	if m.preferencePath == "" {
		t.Fatal("preferences unavailable")
	}
	m.projects = []row{{"id": float64(7), "name": "Site"}, {"id": float64(9), "name": "API"}}
	// A confirmed new session remembers its project; a confirmed project shell
	// moves a different project to the front.
	m.Update(resultMsg{label: "Create session", data: []byte(`{"id":1,"project_id":9}`)})
	m.Update(resultMsg{label: "Create blank shell", data: []byte(`{"id":2,"project_id":7}`)})
	if !sameInt64s(m.recentProjects, []int64{7, 9}) {
		t.Fatalf("recency not recorded in order: %v", m.recentProjects)
	}
	reloaded := newDashboard(New("http://example.com/deck", "other-token"), nil)
	reloaded.loadPreferences()
	if !sameInt64s(reloaded.recentProjects, []int64{7, 9}) || reloaded.scratchNewSession {
		t.Fatalf("recency did not survive reload: %v scratch=%v", reloaded.recentProjects, reloaded.scratchNewSession)
	}
}

func TestRecentProjectsAreDedupedAndBounded(t *testing.T) {
	m := recentTestDashboard()
	m.projects = []row{{"id": float64(7), "name": "Site"}}
	for i := int64(1); i <= 12; i++ {
		m.rememberProject(i)
	}
	if len(m.recentProjects) != maxRecentProjects {
		t.Fatalf("recency list not bounded: %v", m.recentProjects)
	}
	if m.recentProjects[0] != 12 {
		t.Fatalf("newest project is not first: %v", m.recentProjects)
	}
	m.rememberProject(5)
	count := 0
	for _, id := range m.recentProjects {
		if id == 5 {
			count++
		}
	}
	if count != 1 || m.recentProjects[0] != 5 {
		t.Fatalf("re-used project not promoted exactly once: %v", m.recentProjects)
	}
}

func TestOrderProjectsUsesRecencyThenStableOrderAndSkipsStale(t *testing.T) {
	projects := []row{{"id": float64(1)}, {"id": float64(2)}, {"id": float64(3)}, {"id": float64(4)}}
	got := orderProjects(projects, []int64{3, 99, 1})
	want := []float64{3, 1, 2, 4}
	if len(got) != len(want) {
		t.Fatalf("ordering changed the project count: %v", got)
	}
	for i, r := range got {
		if id(r) != fmt.Sprintf("%v", want[i]) {
			t.Fatalf("order[%d]=%s want %v", i, id(r), want[i])
		}
	}
	// A deleted/stale ID is ignored, and an empty recency keeps the given order.
	if same := orderProjects(projects, nil); len(same) != len(projects) || id(same[0]) != "1" {
		t.Fatalf("empty recency reordered projects: %v", same)
	}
}

func TestFailedOrUnconfirmedLaunchDoesNotRecordRecency(t *testing.T) {
	isolateConsoleConfig(t)
	m := newDashboard(New("http://example.com/deck", ""), nil)
	m.loadPreferences()
	m.recentProjects = []int64{5}
	m.Update(resultMsg{label: "Create session", err: fmt.Errorf("connection lost")})
	m.Update(resultMsg{label: "Create blank shell", err: fmt.Errorf("connection lost")})
	// A 2xx body without an id is not a confirmed launch either.
	m.Update(resultMsg{label: "Create session", data: []byte(`{}`)})
	if !sameInt64s(m.recentProjects, []int64{5}) {
		t.Fatalf("failed launch changed recency: %v", m.recentProjects)
	}
	if m.scratchNewSession {
		t.Fatal("failed launch was recorded as an explicit Scratch choice")
	}
}

func TestMachineShellDoesNotRecordProjectOrScratch(t *testing.T) {
	isolateConsoleConfig(t)
	m := newDashboard(New("http://example.com/deck", ""), nil)
	m.loadPreferences()
	m.recentProjects = []int64{5}
	m.Update(resultMsg{label: "Create blank shell", data: []byte(`{"id":42,"target_id":3}`)})
	if !sameInt64s(m.recentProjects, []int64{5}) || m.scratchNewSession {
		t.Fatalf("machine shell conflated with a project: %v scratch=%v", m.recentProjects, m.scratchNewSession)
	}
}

func TestNewSessionFormDefaultsToLastProjectAndFallsBackWhenDeleted(t *testing.T) {
	m := recentTestDashboard()
	m.projects = []row{{"id": float64(7), "name": "Site"}, {"id": float64(9), "name": "API"}}
	m.recentProjects = []int64{9, 7}
	m.newForm()
	if got := projectField(t, m).Value; got != "9" {
		t.Fatalf("new session defaulted to %q, want the most recent project", got)
	}
	if vals := optionValues(projectField(t, m)); len(vals) < 3 || vals[1] != "9" || vals[2] != "7" {
		t.Fatalf("project choices not ordered by recency: %v", vals)
	}
	// The most recent project was deleted: fall back to the next recency entry.
	m.projects = []row{{"id": float64(7), "name": "Site"}}
	m.newForm()
	if got := projectField(t, m).Value; got != "7" {
		t.Fatalf("deleted recent project did not fall back: %q", got)
	}
	// Every remembered project is gone: degrade to the blank Scratch option.
	m.projects = nil
	m.newForm()
	if got := projectField(t, m).Value; got != "" {
		t.Fatalf("stale recency did not degrade to Scratch: %q", got)
	}
}

func TestExplicitScratchNewSessionKeepsScratchDefault(t *testing.T) {
	isolateConsoleConfig(t)
	m := recentTestDashboard()
	m.loadPreferences()
	m.projects = []row{{"id": float64(7), "name": "Site"}}
	m.recentProjects = []int64{7}
	m.Update(resultMsg{label: "Create session", data: []byte(`{"id":1}`)})
	if !m.scratchNewSession {
		t.Fatal("an ordinary session with no project was not remembered as Scratch")
	}
	m.newForm()
	if got := projectField(t, m).Value; got != "" {
		t.Fatalf("explicit Scratch did not keep the blank default: %q", got)
	}
}

func TestBlankShellOrdersProjectsByRecency(t *testing.T) {
	m := recentTestDashboard()
	m.targets = []row{{"id": float64(1), "name": "AIServer", "kind": "local"}}
	m.projects = []row{{"id": float64(7), "name": "Site"}, {"id": float64(9), "name": "API"}}
	m.recentProjects = []int64{9}
	m.newShellForm()
	if m.form == nil || len(m.form.fields) != 1 {
		t.Fatal("blank shell form changed shape")
	}
	field := m.form.fields[0]
	if got := field.Value; got != "project:9" {
		t.Fatalf("blank shell defaulted to %q, want the most recent project", got)
	}
	vals := optionValues(field)
	if len(vals) != 3 || vals[0] != "project:9" || vals[1] != "project:7" || vals[2] != "machine:1" {
		t.Fatalf("locations not ordered most-recent first then machines: %v", vals)
	}
}
