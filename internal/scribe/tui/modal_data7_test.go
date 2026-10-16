package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// data7Backend adds the wave 7 optional interfaces to the recording fake:
// soft delete, memory settings, diagnose, update check and MCP ping.
type data7Backend struct {
	*recBackend
	hidden      []MemoryItem
	policy      MemoryPolicy
	savedPolicy []string // "write=<v> read=<v> to=<file>"
	policyErr   error
	checks      []DiagnoseCheck
	update      UpdateStatus
	pingTook    time.Duration
	pingErr     error
	pinged      []string
}

func (d *data7Backend) HiddenMemories() []MemoryItem {
	return append([]MemoryItem(nil), d.hidden...)
}

func (d *data7Backend) HideMemory(id string) error {
	for i, it := range d.recBackend.mems {
		if it.ID == id {
			d.hidden = append(d.hidden, it)
			d.recBackend.mems = slices.Delete(d.recBackend.mems, i, i+1)
			return nil
		}
	}
	return errors.New("no memory")
}

func (d *data7Backend) RestoreMemory(id string) error {
	for i, it := range d.hidden {
		if it.ID == id {
			d.recBackend.mems = append(d.recBackend.mems, it)
			d.hidden = slices.Delete(d.hidden, i, i+1)
			return nil
		}
	}
	return errors.New("no hidden memory")
}

func (d *data7Backend) MemoryPolicy() MemoryPolicy { return d.policy }

func (d *data7Backend) SaveMemoryPolicy(p MemoryPolicy, saveTo string) error {
	if d.policyErr != nil {
		return d.policyErr
	}
	d.savedPolicy = append(d.savedPolicy, "write="+p.WriteScope+" read="+p.ReadScope+" to="+saveTo)
	return nil
}

func (d *data7Backend) Diagnose() []DiagnoseCheck { return d.checks }

func (d *data7Backend) CheckUpdate() UpdateStatus { return d.update }

func (d *data7Backend) PingMCP(name string) (time.Duration, error) {
	d.pinged = append(d.pinged, name)
	return d.pingTook, d.pingErr
}

func data7Fixture() *data7Backend {
	return &data7Backend{
		recBackend: memFixture(),
		policy:     MemoryPolicy{WriteScope: "project", ReadScope: "all", ProjectOpen: true},
	}
}

// keys sends each named key in order.
func keys(m *Model, names ...string) {
	for _, n := range names {
		send(m, n)
	}
}

func TestMemoryDHidesWithoutConfirmAndHiddenTabRestores(t *testing.T) {
	b := data7Fixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/memory")

	send(m, "d") // the first row in the project tab is the pinned convention
	if topTitle(m) != "Memory" {
		t.Fatalf("d on a hideable backend must not confirm, top=%q", topTitle(m))
	}
	if len(b.hidden) != 1 || b.hidden[0].ID != "p1" {
		t.Fatalf("hidden = %+v", b.hidden)
	}
	if len(b.deleted) != 0 {
		t.Fatalf("soft delete must not hard delete: %v", b.deleted)
	}
	mustContain(t, screen(m), "Hidden 1", "Restore it from the Hidden tab")

	keys(m, "tab", "tab") // project -> global -> hidden
	mustContain(t, screen(m), "Tests live next to the code", "r restore")
	send(m, "r")
	if len(b.hidden) != 0 {
		t.Fatalf("r on the hidden tab should restore: hidden=%+v", b.hidden)
	}
	mustContain(t, screen(m), "Restored")
}

func TestMemoryHiddenTabDeleteForGoodAsksFirst(t *testing.T) {
	b := data7Fixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/memory")
	send(m, "d")
	keys(m, "tab", "tab")
	send(m, "d")
	if topTitle(m) != "Delete memory" {
		t.Fatalf("d on the hidden tab should confirm, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "for good")
	send(m, "n")
	if len(b.deleted) != 0 {
		t.Fatalf("answering no deleted: %v", b.deleted)
	}
	send(m, "d")
	send(m, "y")
	if len(b.deleted) != 1 || b.deleted[0] != "p1" {
		t.Fatalf("deleted = %v", b.deleted)
	}
}

func TestMemoryHiddenTabEmptyStateSaysWhatItIs(t *testing.T) {
	b := data7Fixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/memory")
	keys(m, "tab", "tab")
	mustContain(t, screen(m), "Nothing is hidden")
}

func TestMemoryDWithoutHiderStillConfirms(t *testing.T) {
	m := openMemory(t, 140, 36, memFixture())
	send(m, "d")
	if topTitle(m) != "Delete memory" {
		t.Fatalf("a backend that cannot hide must confirm, top=%q", topTitle(m))
	}
}

func TestMemorySettingsFormCyclesAndSaves(t *testing.T) {
	b := data7Fixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/memory")
	send(m, "s")
	if topTitle(m) != "Memory settings" {
		t.Fatalf("s should open the settings form, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "Write new memories to", "Automatic recall reads", "Save these settings to")
	send(m, "right") // write scope: project -> global
	send(m, "down")
	send(m, "right") // read scope: all -> project
	send(m, "down")  // save-to row
	send(m, "ctrl+s")
	if topTitle(m) != "Memory" {
		t.Fatalf("a successful save closes the form, top=%q", topTitle(m))
	}
	want := []string{"write=global read=project to=project"}
	if !slices.Equal(b.savedPolicy, want) {
		t.Fatalf("saved = %v, want %v", b.savedPolicy, want)
	}
}

func TestMemorySettingsFormShowsSaveErrorAndStays(t *testing.T) {
	b := data7Fixture()
	b.policyErr = errors.New("disk full")
	m := modalModel(t, 140, 36, b)
	m.runCommand("/memory")
	send(m, "s")
	send(m, "ctrl+s")
	if topTitle(m) != "Memory settings" {
		t.Fatalf("a failed save keeps the form open, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "disk full")
}

func TestMemorySettingsGlobalFileWhenNoProject(t *testing.T) {
	b := data7Fixture()
	b.policy.ProjectOpen = false
	m := modalModel(t, 140, 36, b)
	m.runCommand("/memory")
	send(m, "s")
	send(m, "down")
	send(m, "down")
	send(m, "ctrl+s")
	if len(b.savedPolicy) != 1 || !strings.HasSuffix(b.savedPolicy[0], "to=global") {
		t.Fatalf("with no project the file is global: %v", b.savedPolicy)
	}
}

func TestDiagnosePopupRunsChecksAndShowsThem(t *testing.T) {
	b := data7Fixture()
	b.checks = []DiagnoseCheck{
		{Area: "Providers", Name: "anthropic", State: "ok", Detail: "key set"},
		{Area: "MCP servers", Name: "fs", State: "fail", Detail: "exit status 1"},
		{Area: "Disk", Name: "project filesystem", State: "warn", Detail: "0.5 GB free"},
	}
	m := modalModel(t, 100, 30, b)
	cmd := m.runCommand("/diagnose")
	if topTitle(m) != "Diagnose" {
		t.Fatalf("/diagnose should open the popup, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "Checking")
	if cmd == nil {
		t.Fatal("the popup should start the checks")
	}
	m.Update(cmd())
	mustContain(t, screen(m), "anthropic", "MCP servers", "exit status 1", "0.5 GB free")
}

func TestDiagnoseFallsBackToTheInfoView(t *testing.T) {
	m := modalModel(t, 100, 30, newRecBackend())
	m.runCommand("/diagnose")
	if topTitle(m) != "Diagnose" {
		t.Fatalf("top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "Code map")
}

func TestUpdateNoticeSaysWhenANewerReleaseIsOut(t *testing.T) {
	b := data7Fixture()
	b.update = UpdateStatus{Current: "2.10.1", Latest: "v2.11.0", Known: true, Newer: true}
	m := modalModel(t, 100, 30, b)
	cmd := m.runCommand("/update")
	if topTitle(m) != "Updates" {
		t.Fatalf("/update should open the notice, top=%q", topTitle(m))
	}
	m.Update(cmd())
	mustContain(t, screen(m), "Release v2.11.0 is out", "lectern update")
}

func TestUpdateNoticeCurrentAndErrorStates(t *testing.T) {
	b := data7Fixture()
	b.update = UpdateStatus{Current: "2.11.0", Latest: "v2.11.0", Known: true}
	m := modalModel(t, 100, 30, b)
	m.Update(m.runCommand("/update")())
	mustContain(t, screen(m), "newest release")

	b2 := data7Fixture()
	b2.update = UpdateStatus{Current: "2.11.0", Err: "dial tcp: no route"}
	m2 := modalModel(t, 100, 30, b2)
	m2.Update(m2.runCommand("/update")())
	mustContain(t, screen(m2), "Could not read the release list", "no route")
}

func TestUpdateNoticeWithoutCheckerSaysSo(t *testing.T) {
	m := modalModel(t, 100, 30, newRecBackend())
	cmd := m.runCommand("/update")
	if cmd == nil {
		t.Fatal("expected a flash")
	}
	if topTitle(m) == "Updates" {
		t.Fatal("a backend that cannot check must not open the notice")
	}
}

func TestServersPingAnswersAndDetailsShowIt(t *testing.T) {
	b := data7Fixture()
	b.servers = []ServerStatus{{Name: "fs", Kind: "mcp", State: "ready", Tools: 3}}
	b.pingTook = 42 * time.Millisecond
	m := modalModel(t, 120, 30, b)
	m.runCommand("/mcp")
	cmd := send(m, "p")
	if cmd == nil {
		t.Fatal("p should start a ping")
	}
	m.Update(cmd())
	if !slices.Equal(b.pinged, []string{"fs"}) {
		t.Fatalf("pinged = %v", b.pinged)
	}
	mustContain(t, screen(m), "fs answered in 42ms")

	send(m, "enter") // details
	if topTitle(m) != "MCP · fs" {
		t.Fatalf("enter should open the MCP details, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "Last ping", "42ms")
}

func TestServersPingFailureSaysNoAnswer(t *testing.T) {
	b := data7Fixture()
	b.servers = []ServerStatus{{Name: "fs", Kind: "mcp", State: "ready"}}
	b.pingErr = errors.New("deadline exceeded")
	m := modalModel(t, 120, 30, b)
	m.runCommand("/mcp")
	m.Update(send(m, "p")())
	mustContain(t, screen(m), "fs did not answer", "deadline exceeded")
}

func TestSessionSizeFormatting(t *testing.T) {
	cases := map[int64]string{
		0:           "0 B",
		900:         "900 B",
		2048:        "2 KB",
		1536 * 1024: "1.5 MB",
	}
	for n, want := range cases {
		if got := sessionSize(n); got != want {
			t.Errorf("sessionSize(%d) = %q, want %q", n, got, want)
		}
	}
}

var _ tea.Msg = diagnoseDone{}
