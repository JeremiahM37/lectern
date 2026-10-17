package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// dripAfter feeds text into an open segment, then runs the drip with calls
// spaced cadence apart until elapsed has passed, ending on a call at exactly
// elapsed. It returns how many runes were revealed.
func dripAfter(cadence, elapsed time.Duration) int {
	s := &Segment{Kind: "text", Open: true, Text: strings.Repeat("lorem ipsum dolor sit amet ", 100)}
	t0 := time.Unix(1000, 0)
	advanceSeg(s, t0)
	for at := cadence; at < elapsed; at += cadence {
		advanceSeg(s, t0.Add(at))
	}
	advanceSeg(s, t0.Add(elapsed))
	return s.shown
}

func TestDripRevealDoesNotDependOnFrameRate_w21drip(t *testing.T) {
	// The reveal is driven by elapsed time, so the same second of streaming
	// reveals the same text whether the app redraws every 16, 45 or 50 ms.
	want := dripAfter(time.Millisecond, time.Second)
	if want == 0 {
		t.Fatal("drip revealed nothing in a second")
	}
	for _, cadence := range []time.Duration{16 * time.Millisecond, 45 * time.Millisecond, 50 * time.Millisecond, 200 * time.Millisecond} {
		if got := dripAfter(cadence, time.Second); got != want {
			t.Errorf("cadence %v revealed %d runes in 1 s, want %d (same as 1 ms cadence)", cadence, got, want)
		}
	}
}

func TestDripStepsAreThirtyTwoMilliseconds_w21drip(t *testing.T) {
	// 32 ms is one step, so a single call after 320 ms has run ten steps: the
	// velocity has moved ten times, not once.
	s := &Segment{Kind: "text", Open: true, Text: strings.Repeat("word ", 400)}
	t0 := time.Unix(1000, 0)
	advanceSeg(s, t0)
	if s.shown != 0 {
		t.Fatalf("first call revealed %d runes before any step ran", s.shown)
	}
	advanceSeg(s, t0.Add(31*time.Millisecond))
	if s.shown != 0 {
		t.Errorf("31 ms revealed %d runes, want none before one full step", s.shown)
	}
	advanceSeg(s, t0.Add(320*time.Millisecond))
	if got := s.vel; got == dripFloor {
		t.Errorf("velocity did not move over ten steps: %v", got)
	}
	if s.dripAt != t0.Add(320*time.Millisecond) {
		t.Errorf("drip clock = %v, want it to land on 320 ms", s.dripAt.Sub(t0))
	}
}

func TestDripFullRevealWhenStreamFinishes_w21drip(t *testing.T) {
	text := "A long answer that is still arriving when the turn ends, with punctuation."
	s := &Segment{Kind: "text", Open: true, Text: text}
	t0 := time.Unix(1000, 0)
	advanceSeg(s, t0)
	advanceSeg(s, t0.Add(64*time.Millisecond))
	if visibleText(s) == text {
		t.Fatal("drip should still be holding text back while streaming")
	}
	// The turn ends: the segment closes and the drip must not hold anything back.
	s.Open = false
	advanceSeg(s, t0.Add(100*time.Millisecond))
	if got := visibleText(s); got != text {
		t.Errorf("after finish visible = %q, want the full text", got)
	}
}

func TestDripFinishWithoutTickShowsEverything_w21drip(t *testing.T) {
	text := "Closed before the drip ever ran."
	s := &Segment{Kind: "text", Open: false, Text: text}
	if got := visibleText(s); got != text {
		t.Errorf("closed segment visible = %q, want full text", got)
	}
}

func TestStreamingTextDimmedOnlyWhileOpen_w21drip(t *testing.T) {
	th := theme.Theme{
		BgApp:       lipgloss.Color("#000000"),
		TextPrimary: lipgloss.Color("#ffffff"),
		TextDim:     lipgloss.Color("#808080"),
	}
	open := textThemeFor(th, true)
	// 0.65 of the way from black to white is 166 (0xa6).
	if got := open.TextPrimary; got != lipgloss.Color("#a6a6a6") {
		t.Errorf("streaming TextPrimary = %v, want #a6a6a6", got)
	}
	if got := open.TextSecondary; got != lipgloss.Color("#a6a6a6") {
		t.Errorf("streaming TextSecondary = %v, want #a6a6a6", got)
	}
	if open.BgApp != th.BgApp || open.TextFaint != th.TextFaint {
		t.Error("streaming dimming changed tokens other than the body text colours")
	}

	settled := textThemeFor(th, false)
	if settled.TextPrimary != th.TextPrimary || settled.TextSecondary != th.TextSecondary {
		t.Errorf("settled text was dimmed: %v", settled.TextPrimary)
	}
}

func TestStreamingTextStepsToTextDimWithoutTruecolour_w21drip(t *testing.T) {
	cases := map[string]theme.Theme{
		"transparent background": {
			BgApp:       lipgloss.NoColor{},
			TextPrimary: lipgloss.Color("#ffffff"),
			TextDim:     lipgloss.Color("244"),
		},
		"ansi palette": {
			BgApp:       lipgloss.Color("0"),
			TextPrimary: lipgloss.Color("15"),
			TextDim:     lipgloss.Color("244"),
		},
	}
	for name, th := range cases {
		got := textThemeFor(th, true).TextPrimary
		if got != th.TextDim {
			t.Errorf("%s: streaming TextPrimary = %v, want TextDim %v", name, got, th.TextDim)
		}
	}
}
