package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// fadeTheme is a truecolour theme: black background, white text.
func fadeTheme() theme.Theme {
	return theme.Theme{
		BgApp:         lipgloss.Color("#000000"),
		TextPrimary:   lipgloss.Color("#ffffff"),
		TextSecondary: lipgloss.Color("#ffffff"),
		TextDim:       lipgloss.Color("#808080"),
		TextFaint:     lipgloss.Color("#404040"),
	}
}

// noTruecolourTheme has a transparent background, so there is nothing to blend.
func noTruecolourTheme() theme.Theme {
	th := fadeTheme()
	th.BgApp = lipgloss.NoColor{}
	return th
}

// streamingView returns a view and one assistant record with one open text
// segment, both stamped as opened at t0.
func streamingView(t *testing.T, t0 time.Time) (*View, *Record, *Segment) {
	t.Helper()
	v := New(Options{})
	seg := &Segment{Kind: "text", Open: true, Text: strings.Repeat("lorem ipsum dolor sit amet ", 20)}
	rec := &Record{Role: roleAssistant, Segments: []*Segment{seg}}
	v.msgs = []*Record{rec}
	v.Tick(t0)
	if seg.openedAt != t0 {
		t.Fatalf("openedAt = %v, want %v", seg.openedAt, t0)
	}
	return v, rec, seg
}

// closeAt marks the segment closed and stamps the close at t.
func closeAt(v *View, rec *Record, seg *Segment, t time.Time) {
	seg.Open = false
	rec.Done = true
	v.Tick(t)
}

func colourOf(th theme.Theme) lipgloss.TerminalColor { return th.TextPrimary }

func TestFadeInStartsAtBackgroundAndEndsAtStreamOpacity_w25fade(t *testing.T) {
	t0 := time.Unix(2000, 0)
	v, _, seg := streamingView(t, t0)
	th := fadeTheme()

	// t=0: alpha 0, so the text is drawn in the background colour.
	if got := colourOf(textThemeAt(th, seg, t0)); got != lipgloss.Color("#000000") {
		t.Errorf("fade-in t=0 colour = %v, want #000000", got)
	}
	// Mid-fade (150 ms): outQuad(0.5) = 0.75, so alpha = 0.65 * 0.75 = 0.4875,
	// which rounds to 124 (0x7c).
	mid := t0.Add(150 * time.Millisecond)
	v.Tick(mid)
	if got := colourOf(textThemeAt(th, seg, mid)); got != lipgloss.Color("#7c7c7c") {
		t.Errorf("fade-in mid colour = %v, want #7c7c7c", got)
	}
	// End of fade-in (300 ms): the streaming opacity, 0.65 * 255 = 166 (0xa6).
	end := t0.Add(300 * time.Millisecond)
	v.Tick(end)
	if got := colourOf(textThemeAt(th, seg, end)); got != lipgloss.Color("#a6a6a6") {
		t.Errorf("fade-in end colour = %v, want #a6a6a6", got)
	}
	// It holds at streaming opacity after the fade, and is not fading any more.
	later := t0.Add(2 * time.Second)
	v.Tick(later)
	if got := colourOf(textThemeAt(th, seg, later)); got != lipgloss.Color("#a6a6a6") {
		t.Errorf("steady streaming colour = %v, want #a6a6a6", got)
	}
	if seg.fading(later) {
		t.Error("open segment still fading after its fade-in")
	}
}

func TestFadeOutRunsFromStreamOpacityToFullStrength_w25fade(t *testing.T) {
	t0 := time.Unix(3000, 0)
	v, rec, seg := streamingView(t, t0)
	th := fadeTheme()

	// Streamed for a second, then closed: the fade starts at 0.65.
	closed := t0.Add(time.Second)
	seg.Open = false
	rec.Done = true
	v.Tick(closed)
	if seg.closedAt != closed {
		t.Fatalf("closedAt = %v, want %v", seg.closedAt, closed)
	}
	if got := colourOf(textThemeAt(th, seg, closed)); got != lipgloss.Color("#a6a6a6") {
		t.Errorf("fade-out t=0 colour = %v, want #a6a6a6", got)
	}
	// Mid-fade (125 ms): 0.65 + 0.35 * outQuad(0.5) = 0.9125, which rounds to
	// 233 (0xe9).
	mid := closed.Add(125 * time.Millisecond)
	v.Tick(mid)
	if got := colourOf(textThemeAt(th, seg, mid)); got != lipgloss.Color("#e9e9e9") {
		t.Errorf("fade-out mid colour = %v, want #e9e9e9", got)
	}
	// End (250 ms): full strength, the theme's own colour.
	end := closed.Add(250 * time.Millisecond)
	v.Tick(end)
	if got := textThemeAt(th, seg, end); got != th {
		t.Errorf("fade-out end theme = %+v, want the theme unchanged", got)
	}
}

func TestFadeOutFromMidFadeInStartsWhereItStopped_w25fade(t *testing.T) {
	// Closing during the fade-in starts the fade-out from the opacity the text
	// had reached, not from the streaming opacity, so it does not jump.
	t0 := time.Unix(3500, 0)
	v, rec, seg := streamingView(t, t0)
	closed := t0.Add(150 * time.Millisecond)
	seg.Open = false
	rec.Done = true
	v.Tick(closed)
	th := fadeTheme()
	// At close the alpha is 0.4875 (see the fade-in test), so the colour there
	// must match the fade-in mid value.
	if got := colourOf(textThemeAt(th, seg, closed)); got != lipgloss.Color("#7c7c7c") {
		t.Errorf("colour at close = %v, want #7c7c7c", got)
	}
}

func TestFadeWithoutTruecolourStepsThroughTextFaintAndTextDim_w25fade(t *testing.T) {
	t0 := time.Unix(4000, 0)
	v, rec, seg := streamingView(t, t0)
	th := noTruecolourTheme()

	// Fade-in, first half: TextFaint.
	if got := colourOf(textThemeAt(th, seg, t0)); got != th.TextFaint {
		t.Errorf("fade-in t=0 = %v, want TextFaint", got)
	}
	// Fade-in, second half: TextDim.
	v.Tick(t0.Add(200 * time.Millisecond))
	if got := colourOf(textThemeAt(th, seg, t0.Add(200*time.Millisecond))); got != th.TextDim {
		t.Errorf("fade-in t=200ms = %v, want TextDim", got)
	}
	// Steady streaming: TextDim, as before the fades existed.
	v.Tick(t0.Add(2 * time.Second))
	if got := colourOf(textThemeAt(th, seg, t0.Add(2*time.Second))); got != th.TextDim {
		t.Errorf("steady streaming = %v, want TextDim", got)
	}

	// Fade-out: TextDim while it is still under full strength, then the theme.
	closed := t0.Add(2 * time.Second)
	seg.Open = false
	rec.Done = true
	v.Tick(closed)
	if got := colourOf(textThemeAt(th, seg, closed.Add(100*time.Millisecond))); got != th.TextDim {
		t.Errorf("fade-out mid = %v, want TextDim", got)
	}
	end := closed.Add(250 * time.Millisecond)
	v.Tick(end)
	if got := colourOf(textThemeAt(th, seg, end)); got != th.TextPrimary {
		t.Errorf("fade-out end = %v, want TextPrimary", got)
	}
}

func TestFadeDoesNotHoldTextBack_w25fade(t *testing.T) {
	// The fade only changes colour. The drip reveals text at its usual rate
	// while the fade-in runs, so the first words are never held back.
	t0 := time.Unix(5000, 0)
	v, _, seg := streamingView(t, t0)
	v.Tick(t0.Add(200 * time.Millisecond))
	if seg.shown == 0 {
		t.Error("no text revealed during the fade-in")
	}
	want := dripAfter(time.Millisecond, 200*time.Millisecond)
	if seg.shown != want {
		t.Errorf("reveal during fade-in = %d runes, want %d (same as with no fade)", seg.shown, want)
	}
}

func TestSettledSegmentsRequestNoRedraw_w25fade(t *testing.T) {
	t0 := time.Unix(6000, 0)
	v, rec, seg := streamingView(t, t0)
	closed := t0.Add(time.Second)
	seg.Open = false
	rec.Done = true
	v.Tick(closed)

	// Inside the fade-out window the record must be drawn fresh.
	v.Tick(closed.Add(100 * time.Millisecond))
	if !v.dynamic(0, rec) {
		t.Error("record in its fade-out window is not dynamic")
	}
	// Once the fade has ended it is settled: no redraw, and it can be cached.
	v.Tick(closed.Add(fadeOut))
	if v.dynamic(0, rec) {
		t.Error("settled finished record still requests redraws")
	}
	if seg.fading(closed.Add(fadeOut)) {
		t.Error("settled segment still reports fading")
	}
}

func TestSettledRecordStopsAfterFadeOutAndIsCached_w25fade(t *testing.T) {
	t0 := time.Unix(6500, 0)
	v, rec, seg := streamingView(t, t0)
	closed := t0.Add(time.Second)
	seg.Open = false
	rec.Done = true
	v.Tick(closed)
	v.SetSize(80, 40)

	v.Tick(closed.Add(100 * time.Millisecond))
	v.View()
	if rec.cache != nil {
		t.Error("record was cached while its fade-out was still running")
	}
	v.Tick(closed.Add(fadeOut + time.Millisecond))
	v.View()
	first := rec.cache
	if first == nil {
		t.Fatal("settled record was not cached after its fade-out")
	}
	v.Tick(closed.Add(fadeOut + 2*time.Second))
	v.View()
	if rec.cache != first {
		t.Error("settled record was re-rendered: its cache entry was replaced")
	}
}

func TestRestoredSegmentDoesNotFadeOnLoad_w25fade(t *testing.T) {
	// A transcript restored from JSON has text that was never seen open. It
	// must draw at full strength with no fade and no redraw.
	v := New(Options{})
	seg := &Segment{Kind: "text", Text: "restored answer"}
	rec := &Record{Role: roleAssistant, Done: true, Segments: []*Segment{seg}}
	v.msgs = []*Record{rec}
	t0 := time.Unix(7000, 0)
	v.Tick(t0)
	if !seg.closedAt.IsZero() {
		t.Errorf("restored segment was stamped closed at %v", seg.closedAt)
	}
	if got := textThemeAt(fadeTheme(), seg, t0); got != fadeTheme() {
		t.Errorf("restored segment colour changed: %+v", got)
	}
	if v.dynamic(0, rec) {
		t.Error("restored finished record requests redraws")
	}
}

func TestClosedStampIsNotMovedByLaterTicks_w25fade(t *testing.T) {
	t0 := time.Unix(8000, 0)
	v, rec, seg := streamingView(t, t0)
	closed := t0.Add(time.Second)
	seg.Open = false
	rec.Done = true
	v.Tick(closed)
	v.Tick(closed.Add(time.Second))
	if seg.closedAt != closed {
		t.Errorf("closedAt moved to %v, want %v", seg.closedAt, closed)
	}
}
