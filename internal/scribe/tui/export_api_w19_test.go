package tui

import (
	"strings"
	"testing"
)

// The fake backend has no request dump, so save reports the backend's answer
// rather than toggling the dump.
func TestExportAPISaveExportsInsteadOfToggling(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	before := fakeAPIDumpOn
	f := flashOf(t, m.exportAPI(" save"))
	if fakeAPIDumpOn != before {
		t.Fatal("save must not toggle the dump")
	}
	if f.text == "" || strings.Contains(f.text, "dump on") || strings.Contains(f.text, "dump off") {
		t.Fatalf("save flashed %q (%s)", f.text, f.kind)
	}
}
