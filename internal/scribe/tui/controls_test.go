package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func TestControlMarks(t *testing.T) {
	th := theme.Default()
	cases := map[string][2]string{
		"on":       {Toggle(th, true, false), "[●━] ON"},
		"off":      {Toggle(th, false, false), "[━●] OFF"},
		"checked":  {Checkbox(th, true), "[✓]"},
		"empty":    {Checkbox(th, false), "[ ]"},
		"selected": {Radio(th, true), "(●)"},
		"other":    {Radio(th, false), "( )"},
		"button":   {Button(th, "Save", ButtonDefault, true), "Save"},
	}
	for name, c := range cases {
		if !strings.Contains(c[0], c[1]) {
			t.Fatalf("%s: %q lacks %q", name, c[0], c[1])
		}
	}
}

func TestFocusedToggleHasMarker(t *testing.T) {
	th := theme.Default()
	if got := Toggle(th, true, true); !strings.Contains(got, "▸ ") {
		t.Fatalf("focused toggle has no marker: %q", got)
	}
	if got := Toggle(th, true, false); strings.Contains(got, "▸") {
		t.Fatalf("unfocused toggle has a marker: %q", got)
	}
}
