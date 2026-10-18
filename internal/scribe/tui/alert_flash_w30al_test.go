package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failAlert sets the flash line and opens one alert, and a repeat of the same
// failure does not stack a second alert.
func TestFailAlertSetsFlashAndOneAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.failAlert("Could not save the theme style: disk full")
	m.failAlert("Could not save the theme style: disk full")

	if m.flash != "Could not save the theme style: disk full" || m.flashKind != "err" {
		t.Fatalf("flash = %q (%s), want the error line", m.flash, m.flashKind)
	}
	a := topAlert(t, m)
	if a.level != "error" || a.title != flashErrAlertTitle {
		t.Fatalf("alert level %q title %q", a.level, a.title)
	}
	if n := errorAlerts(m); n != 1 {
		t.Fatalf("error alerts = %d, want 1 after a repeat", n)
	}
}

// A real save failure from /editor (the setting could not be written) reaches
// the user as an alert. The config home is pointed at a regular file, so the
// save cannot create its directory.
func TestEditorSettingSaveFailureOpensAlert(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIBE_HOME", blocker)

	m, _ := newTestModel(t, 120, 30)
	m.runCommand("/editor sync on")

	if m.flashKind != "err" || !strings.HasPrefix(m.flash, "Could not save the editor setting: ") {
		t.Fatalf("flash = %q (%s), want the save error", m.flash, m.flashKind)
	}
	if n := errorAlerts(m); n != 1 {
		t.Fatalf("error alerts = %d, want 1 for a failed save", n)
	}
}

// Usage and invalid-value messages are typing mistakes: the flash line is
// enough, so the call site opens no alert.
func TestNewTabsUnavailableStaysFlashOnly(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.newBackend = nil
	m.openNewTab()

	if m.flash != "New tabs are not available here" || m.flashKind != "err" {
		t.Fatalf("flash = %q (%s)", m.flash, m.flashKind)
	}
	if n := errorAlerts(m); n != 0 {
		t.Fatalf("error alerts = %d, want 0 for a flash-only hint", n)
	}
}
