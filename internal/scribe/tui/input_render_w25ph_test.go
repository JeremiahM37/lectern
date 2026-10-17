package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// frameAt is the first frame whose elapsed time (frame × frameGap) reaches d.
func frameAt(d time.Duration) int {
	return int((d + frameGap - 1) / frameGap)
}

func TestIdleTipRotatesEveryTwelveSeconds(t *testing.T) {
	if placeholderTipEvery != 12*time.Second {
		t.Fatalf("placeholderTipEvery = %v, want 12s (spec 01 §4.2)", placeholderTipEvery)
	}
	n := len(idleTips)
	// Walk two full cycles. At each boundary k×12 s the tip must advance,
	// and the frame just before it must still show the previous tip.
	for k := 0; k <= 2*n; k++ {
		at := frameAt(time.Duration(k) * placeholderTipEvery)
		if got, want := idleTip(at), idleTips[k%n]; got != want {
			t.Fatalf("at %d×12s (frame %d): tip %q, want %q", k, at, got, want)
		}
		if k == 0 {
			continue
		}
		before := at - 1
		if got, want := idleTip(before), idleTips[(k-1)%n]; got != want {
			t.Fatalf("just before %d×12s (frame %d): tip %q, want %q", k, before, got, want)
		}
	}
}

func TestIdleTipHoldsWithinOneSlot(t *testing.T) {
	first := idleTip(0)
	for elapsed := time.Duration(0); elapsed < placeholderTipEvery; elapsed += 500 * time.Millisecond {
		if got := idleTip(frameAt(elapsed)); got != first {
			t.Fatalf("at %v the tip changed to %q before 12 s", elapsed, got)
		}
	}
}

func TestPlaceholderRotatesOnlyWhenIdle(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	tb := m.cur()
	th := m.th

	// Idle and empty: the tip moves with the frame.
	a, _ := inputPlaceholderAt(tb, th, 0)
	b, _ := inputPlaceholderAt(tb, th, frameAt(placeholderTipEvery))
	if a == b {
		t.Fatalf("idle placeholder did not rotate: %q at both 0 s and 12 s", a)
	}
	if a != idleTips[0] {
		t.Fatalf("first idle placeholder = %q, want the original copy %q", a, idleTips[0])
	}
}

func TestPlaceholderPriorityKeepsHigherRules(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	th := m.th

	// Busy wins over the idle rotation at every frame.
	tb := m.cur()
	tb.busy = true
	for _, f := range []int{0, frameAt(placeholderTipEvery), frameAt(7 * placeholderTipEvery)} {
		got, _ := inputPlaceholderAt(tb, th, f)
		if got != "'/' for commands · or steer by sending a new message" {
			t.Fatalf("busy placeholder at frame %d = %q", f, got)
		}
	}

	// Checkpoint browsing (idle) wins at every frame, and names the rewind.
	tb.busy = false
	tb.cps = []CheckpointInfo{{Turn: 3}, {Turn: 7}}
	tb.browse = 1
	for _, f := range []int{0, frameAt(placeholderTipEvery), frameAt(5 * placeholderTipEvery)} {
		got, _ := inputPlaceholderAt(tb, th, f)
		if !strings.Contains(got, "rewind to checkpoint #7") {
			t.Fatalf("browsing placeholder at frame %d = %q", f, got)
		}
	}

	// Browsing with a turn running falls through to the busy text, not the
	// rewind prompt, and still does not rotate.
	tb.busy = true
	got, _ := inputPlaceholderAt(tb, th, frameAt(placeholderTipEvery))
	if got != "'/' for commands · or steer by sending a new message" {
		t.Fatalf("busy-while-browsing placeholder = %q", got)
	}

	// Back to live: the rotation resumes.
	tb.busy = false
	tb.browse = -1
	a, _ := inputPlaceholderAt(tb, th, 0)
	c, _ := inputPlaceholderAt(tb, th, frameAt(placeholderTipEvery))
	if a == c {
		t.Fatalf("idle rotation stopped after leaving browse: %q", a)
	}
}

func TestIdleTipsAreAccurate(t *testing.T) {
	known := map[string]bool{}
	for _, c := range allCommands() {
		known["/"+c.Name] = true
		for _, a := range c.Aliases {
			known["/"+a] = true
		}
	}
	seen := map[string]bool{}
	for _, tip := range idleTips {
		if seen[tip] {
			t.Fatalf("duplicate idle tip %q", tip)
		}
		seen[tip] = true
		if n := utf8.RuneCountInString(tip); n > 70 {
			t.Fatalf("tip %q is %d runes, too long for a narrow input", tip, n)
		}
		for _, word := range strings.Fields(tip) {
			if len(word) > 1 && strings.HasPrefix(word, "/") && !known[strings.TrimRight(word, ".,")] {
				t.Fatalf("tip %q names command %q, which does not exist", tip, word)
			}
		}
	}
}

func TestInputPlaceholderKeepsFirstTipAtFrameZero(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	got, _ := inputPlaceholder(m.cur(), m.th)
	if got != idleTips[0] {
		t.Fatalf("inputPlaceholder at frame 0 = %q, want %q", got, idleTips[0])
	}
}
