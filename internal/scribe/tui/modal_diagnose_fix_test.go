package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// A warn or fail row shows its one-line fix under it, so the user knows what
// to do. An ok row has no fix line even when one is set.
func TestDiagnosePopupShowsFixHints(t *testing.T) {
	p := &diagnosePopup{th: theme.Default(), checks: []DiagnoseCheck{
		{Area: "Git", Name: "git", State: "fail", Detail: "not found on PATH", Fix: "install git and put it on PATH"},
		{Area: "Git", Name: "repository", State: "ok", Detail: "branch main", Fix: "not shown"},
	}}
	out := p.View(100, 20)
	if !strings.Contains(out, "→ install git and put it on PATH") {
		t.Fatalf("fail row has no fix line:\n%s", out)
	}
	if strings.Contains(out, "not shown") {
		t.Fatalf("ok row shows its fix:\n%s", out)
	}
}
