package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// manyHealth is a Health Check long enough to scroll at 80x20 and 80x30. Each
// probe wraps its reason over several rows, and the last backend has a warn
// with a fix, so the summary counts are not all passes.
func manyHealth() []HealthBackend {
	var out []HealthBackend
	for i := 0; i < 6; i++ {
		var ps []HealthProbe
		for j := 0; j < 4; j++ {
			ps = append(ps, HealthProbe{
				Name:   fmt.Sprintf("probe-%d-%d-long-name", i, j),
				State:  "pass",
				Reason: "answering on https://aiserver.tail878d9e.ts.net:8443/api/whoami and the reason runs long",
				Ms:     12,
			})
		}
		out = append(out, HealthBackend{Name: fmt.Sprintf("Backend %d", i), Probes: ps})
	}
	out[5].Probes[0].State = "warn"
	out[5].Probes[0].Fix = "commit or stash when you want a clean baseline for /diff"
	return out
}

// checkPinned renders the popup at cols x rows, scrolled by off rows, and
// fails unless the panel fits the screen, the summary is the first row under
// the border and appears nowhere else, and no row is wider than the screen.
func checkPinned(t *testing.T, cols, rows, off int) {
	t.Helper()
	p := &healthPopup{th: theme.Default(), backends: manyHealth(), off: off}
	lines := modalFrame(p, theme.Default(), cols, rows)
	where := fmt.Sprintf("%dx%d off=%d", cols, rows, off)
	if len(lines) > rows {
		t.Fatalf("%s: panel is %d rows, taller than the screen", where, len(lines))
	}
	const summary = "23 pass · 1 warn · 0 fail"
	if got := ansi.Strip(lines[1]); !strings.Contains(got, summary) {
		t.Fatalf("%s: summary is not the first row under the border, row is %q", where, got)
	}
	for i, l := range lines {
		if strings.Contains(ansi.Strip(l), summary) && i != 1 {
			t.Fatalf("%s: summary repeats on row %d:\n%s", where, i, strings.Join(lines, "\n"))
		}
		if w := ansi.StringWidth(l); w > cols {
			t.Fatalf("%s: row %d is %d cells wide", where, i, w)
		}
	}
}

// At 80x30 the summary stays on the first row however far the body is
// scrolled, including past the last probe.
func TestHealthPopupSummaryPinnedAt80x30(t *testing.T) {
	for _, off := range []int{0, 1, 10, 25, 200} {
		checkPinned(t, 80, 30, off)
	}
}

// At 80x20 the body gets fewer rows and the summary stays pinned the same way.
func TestHealthPopupSummaryPinnedAt80x20(t *testing.T) {
	for _, off := range []int{0, 1, 10, 25, 200} {
		checkPinned(t, 80, 20, off)
	}
}

// The panel never has more body rows than fit under the summary: its body is
// the height minus the summary row, and every body row is a row of the probes.
func TestHealthPopupBodyFitsUnderSummary(t *testing.T) {
	p := &healthPopup{th: theme.Default(), backends: manyHealth(), off: 7}
	for _, h := range []int{12, 16, 20} {
		out := p.View(52, h-2)
		lines := strings.Split(out, "\n")
		if len(lines) != h-2 {
			t.Fatalf("view at height %d gave %d rows, want %d", h, len(lines), h-2)
		}
		if !strings.Contains(ansi.Strip(lines[0]), "pass") {
			t.Fatalf("height %d: first row is not the summary: %q", h, ansi.Strip(lines[0]))
		}
	}
}
