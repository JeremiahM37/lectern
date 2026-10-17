package tui

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// swapClipboard captures what copyToTerminal writes, for one test.
func swapClipboard(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := clipboardOut
	clipboardOut = &buf
	t.Cleanup(func() { clipboardOut = old })
	return &buf
}

// pinGOOS makes install hints deterministic for the test.
func pinGOOS(t *testing.T, v string) {
	t.Helper()
	old := goos
	goos = v
	t.Cleanup(func() { goos = old })
}

func TestSetupTabForKeyw19diag(t *testing.T) {
	if tab, ok := setupTabForKey("1"); !ok || tab != setupTools {
		t.Errorf("key 1 = %v, %v; want Tools", tab, ok)
	}
	if tab, ok := setupTabForKey("2"); !ok || tab != setupFonts {
		t.Errorf("key 2 = %v, %v; want Fonts", tab, ok)
	}
	for _, k := range []string{"3", "a", "r", "i", "enter", ""} {
		if _, ok := setupTabForKey(k); ok {
			t.Errorf("key %q must not switch tabs", k)
		}
	}
}

func TestSetupFooterListsOnlyLiveKeysw19diag(t *testing.T) {
	if f := setupFooter(setupFonts, true); !strings.Contains(f, "Space toggle") || strings.Contains(f, "install") {
		t.Errorf("fonts footer = %q", f)
	}
	if f := setupFooter(setupTools, true); !strings.Contains(f, "a copy installs") || !strings.Contains(f, "r check again") {
		t.Errorf("tools footer with missing tools = %q", f)
	}
	if f := setupFooter(setupTools, false); strings.Contains(f, "a copy") || strings.Contains(f, "r check") {
		t.Errorf("tools footer with nothing missing should not offer recheck or copy: %q", f)
	}
}

func TestSetupTabLinesDispatchesw19diag(t *testing.T) {
	th := theme.Default()
	ps := []prereqStatus{}
	tools := strings.Join(setupTabLines(th, setupTools, ps, false, "linux", 80), "\n")
	if !strings.Contains(tools, "Checking this machine") {
		t.Errorf("Tools tab should render the prerequisite list:\n%s", tools)
	}
	fonts := strings.Join(setupTabLines(th, setupFonts, ps, true, "linux", 80), "\n")
	if !strings.Contains(fonts, "Nerd Font icons") || !strings.Contains(fonts, "icons      ") || !strings.Contains(fonts, "on") {
		t.Errorf("Fonts tab should show the saved choice:\n%s", fonts)
	}
	if strings.Contains(fonts, "Checking this machine") {
		t.Errorf("Fonts tab must not show the Tools list")
	}
}

func TestFontLinesShowChoiceAndDetectionw19diag(t *testing.T) {
	th := theme.Default()
	off := strings.Join(fontLines(th, false, false, 80), "\n")
	if !strings.Contains(off, "off") || !strings.Contains(off, "not found") {
		t.Errorf("off and undetected:\n%s", off)
	}
	on := strings.Join(fontLines(th, true, true, 80), "\n")
	if !strings.Contains(on, "on") || !strings.Contains(on, "found") || strings.Contains(on, "not found") {
		t.Errorf("on and detected:\n%s", on)
	}
}

// Copy puts one install command per missing tool on the clipboard and runs
// nothing. The note says so.
func TestCopyInstallsCopiesWithoutRunningw19diag(t *testing.T) {
	fakeMachine(t, "rg")
	pinGOOS(t, "linux")
	buf := swapClipboard(t)
	w := &wizard{prereqs: checkPrereqs()}
	w.copyInstalls()

	if !strings.Contains(w.note, "Nothing was run") {
		t.Errorf("note should say nothing ran, got %q", w.note)
	}
	if !strings.Contains(w.note, "Copied") {
		t.Errorf("note should report the copy, got %q", w.note)
	}
	// OSC 52 carries the text base64-encoded between the escape and BEL.
	out := buf.String()
	start := strings.Index(out, "\x1b]52;c;")
	end := strings.LastIndex(out, "\a")
	if start < 0 || end < 0 {
		t.Fatalf("no OSC 52 sequence written: %q", out)
	}
	text, err := base64.StdEncoding.DecodeString(out[start+len("\x1b]52;c;") : end])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(text)), "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "sudo apt install git") {
		t.Errorf("Git is missing, so its command should be first: %q", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, "ripgrep") {
			t.Errorf("ripgrep is installed and must not be copied: %q", lines)
		}
	}
}

func TestCopyInstallsWhenAllInstalledw19diag(t *testing.T) {
	var have []string
	for _, p := range prereqTable {
		have = append(have, p.Bin)
	}
	fakeMachine(t, have...)
	pinGOOS(t, "linux")
	buf := swapClipboard(t)
	w := &wizard{prereqs: checkPrereqs()}
	w.copyInstalls()
	if w.note != "Everything on this list is installed" {
		t.Errorf("note = %q", w.note)
	}
	if buf.Len() != 0 {
		t.Errorf("nothing is missing, yet %d bytes went to the clipboard", buf.Len())
	}
}

// The diagnose popup shows the routing rows after the index rows, and a
// backend without the routing probe shows none.
type routeDiagBackend struct{}

func (routeDiagBackend) Diagnose() []DiagnoseCheck {
	return []DiagnoseCheck{{Area: "Git", Name: "repository", State: "ok"}}
}

func (routeDiagBackend) DiagnoseIndex() []DiagnoseCheck {
	return []DiagnoseCheck{{Area: "Parsers", Name: "go", State: "ok"}}
}

type routeDiagWithRouting struct{ routeDiagBackend }

func (routeDiagWithRouting) DiagnoseRouting() []DiagnoseCheck {
	return []DiagnoseCheck{{Area: "Model routing", Name: "explore", State: "ok", Detail: "x"}}
}

func TestDiagnoseShowsRoutingAfterIndexw19diag(t *testing.T) {
	c := combinedDiagnoser{base: routeDiagBackend{}, index: routeDiagWithRouting{}}
	got := c.Diagnose()
	var areas []string
	for _, d := range got {
		areas = append(areas, d.Area)
	}
	want := []string{"Git", "Parsers", "Model routing"}
	if strings.Join(areas, ",") != strings.Join(want, ",") {
		t.Errorf("areas = %v, want %v", areas, want)
	}
}

func TestDiagnoseWithoutRoutingProbew19diag(t *testing.T) {
	c := combinedDiagnoser{base: routeDiagBackend{}, index: routeDiagBackend{}}
	for _, d := range c.Diagnose() {
		if d.Area == "Model routing" {
			t.Errorf("a backend without the probe must not add routing rows: %+v", d)
		}
	}
}
