package render

import "testing"

func TestMarqueeFitsUnchanged(t *testing.T) {
	if got := Marquee("short", 10, 7); got != "short" {
		t.Errorf("fitting label changed: %q", got)
	}
	if got := Marquee("anything", 0, 3); got != "" {
		t.Errorf("zero width should be empty, got %q", got)
	}
}

func TestMarqueeHoldsScrollsAndRestarts(t *testing.T) {
	const s = "abcdefghij" // 10 runes
	const w = 6            // over = 4
	over := len([]rune(s)) - w
	period := over + 2*MarqueeHold
	at := func(tick int) string { return Marquee(s, w, tick) }

	if got := at(0); got != "abcdef" {
		t.Errorf("start %q", got)
	}
	if got := at(MarqueeHold - 1); got != "abcdef" {
		t.Errorf("end of start hold %q", got)
	}
	if got := at(MarqueeHold); got != "bcdefg" {
		t.Errorf("first step %q", got)
	}
	if got := at(MarqueeHold + over - 1); got != "efghij" {
		t.Errorf("reached end %q", got)
	}
	for tick := MarqueeHold + over; tick < period; tick++ {
		if got := at(tick); got != "efghij" {
			t.Errorf("tick %d should hold at the end, got %q", tick, got)
		}
	}
	if got := at(period); got != "abcdef" {
		t.Errorf("did not restart after %d ticks: %q", period, got)
	}
	if got := at(-period + MarqueeHold); got != "bcdefg" {
		t.Errorf("negative tick %q", got)
	}
}

func TestMarqueeCountsRunes(t *testing.T) {
	if got := Marquee("ᛁᚲᚠᛊᛏ", 3, 0); got != "ᛁᚲᚠ" {
		t.Errorf("rune window %q", got)
	}
}

func TestEditorOpenFrames(t *testing.T) {
	if len(EditorOpen) != 6 {
		t.Fatalf("EditorOpen has %d frames, want 6", len(EditorOpen))
	}
	if Frame(EditorOpen, 5) != "███" {
		t.Errorf("final frame %q", Frame(EditorOpen, 5))
	}
}
