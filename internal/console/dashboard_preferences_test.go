package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateConsoleConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	return dir
}

func TestDashboardPreferencesRoundTripPerSectionAndServer(t *testing.T) {
	dir := isolateConsoleConfig(t)
	m := newDashboard(New("http://user:secret@EXAMPLE.com/deck/?token=secret#fragment", "bearer-secret"), nil)
	m.loadPreferences()
	if !strings.HasPrefix(m.preferencePath, dir) {
		t.Fatal("escaped isolated config")
	}
	for range 3 {
		m.Update(key("g"))
	}
	m.setGroupCollapsed("Work/Backend", true)
	m.setGroupCollapsed("\x00", true)
	m.switchSection(3)
	for range 2 {
		m.Update(key("g"))
	}
	m.switchSection(0)
	if m.grouping != 3 {
		t.Fatal("section switch lost grouping")
	}
	reloaded := newDashboard(New("http://example.com/deck", "different-token"), nil)
	reloaded.loadPreferences()
	if reloaded.preferencePath != m.preferencePath || reloaded.grouping != 3 || !reloaded.collapsed["Work/Backend"] || !reloaded.collapsed["\x00"] {
		t.Fatal("view did not restore", reloaded)
	}
	if reloaded.attention || reloaded.ended || reloaded.query.Value() != "" {
		t.Fatal("transient filters persisted")
	}
	reloaded.switchSection(3)
	if reloaded.grouping != 2 {
		t.Fatal("task grouping not restored")
	}
	reloaded.Update(key("g"))
	if reloaded.grouping != 0 {
		t.Fatal("named groups offered for tasks")
	}
	other := newDashboard(New("http://example.com/another-deck", ""), nil)
	other.loadPreferences()
	if other.grouping != 0 || other.preferencePath == m.preferencePath {
		t.Fatal("server views mixed")
	}
	data, err := os.ReadFile(m.preferencePath)
	if err != nil || strings.Contains(string(data), "secret") || strings.Contains(string(data), "example.com") {
		t.Fatal("credentials or server address saved", err)
	}
	info, err := os.Stat(m.preferencePath)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal("preferences must be private", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(m.preferencePath), ".view-*"))
	if len(leftovers) != 0 {
		t.Fatal("temporary files left behind")
	}
}

func TestDashboardBadOrNewerPreferencesRemainIntact(t *testing.T) {
	for name, data := range map[string]string{"malformed": "{broken", "newer": `{"version":2}`, "oversized": strings.Repeat(" ", (1<<20)+1)} {
		t.Run(name, func(t *testing.T) {
			isolateConsoleConfig(t)
			path, err := dashboardPreferencePath("http://example.com")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			m := newDashboard(New("http://example.com", ""), nil)
			m.loadPreferences()
			m.Update(key("g"))
			got, _ := os.ReadFile(path)
			if string(got) != data || m.grouping != 1 || !strings.Contains(m.notice, "will not be saved") {
				t.Fatal("bad state overwrote data or blocked interaction")
			}
		})
	}
}

func TestDashboardRejectsInvalidGroupingAndCleansFailedWrite(t *testing.T) {
	isolateConsoleConfig(t)
	m := newDashboard(New("http://example.com", ""), nil)
	m.loadPreferences()
	if err := writeDashboardPreferences(m.preferencePath, dashboardPreferences{Version: 1, Grouping: map[string]int{"sessions": 99, "tasks": 3, "bogus": 1}}); err != nil {
		t.Fatal(err)
	}
	m.loadPreferences()
	if m.grouping != 0 || m.groupingBySection["tasks"] != 0 || m.groupingBySection["bogus"] != 0 {
		t.Fatal("invalid grouping accepted")
	}
	path := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeDashboardPreferences(path, dashboardPreferences{Version: 1}); err == nil {
		t.Fatal("directory replaced")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".view-*"))
	if len(leftovers) != 0 {
		t.Fatal("failed write leaked temporary state")
	}
}
