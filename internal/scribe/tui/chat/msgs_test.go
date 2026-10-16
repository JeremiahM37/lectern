package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

func TestPlanExecutionMessageCollapses(t *testing.T) {
	v, c := newView(t, Options{})
	msg := "Execute this plan.\n\n# Auth rewrite\n- move tokens\n- drop cookies"
	v.AddUser(msg, 0, at(c), false)
	out := plain(v)
	if !strings.Contains(out, "Execute plan: Auth rewrite (5 lines)") {
		t.Fatalf("collapsed plan row missing:\n%s", out)
	}
	if strings.Contains(out, "move tokens") {
		t.Fatalf("collapsed plan still shows its body:\n%s", out)
	}
	if !strings.Contains(out, "^O") {
		t.Fatalf("collapsed plan row has no expand hint:\n%s", out)
	}
	v.ToggleExpand()
	if out := plain(v); !strings.Contains(out, "move tokens") {
		t.Fatalf("expanded plan message does not show its body:\n%s", out)
	}
}

func TestPlanExecutionTitleFallsBackToPlan(t *testing.T) {
	title, n, ok := planExecution("Execute this plan.\nno heading here")
	if !ok || title != "Plan" || n != 2 {
		t.Fatalf("got title %q lines %d ok %v", title, n, ok)
	}
	if _, _, ok := planExecution("please execute this plan"); ok {
		t.Fatalf("ordinary message treated as plan execution")
	}
}

func TestUserOriginSuffixShowsOnlyForNonLocal(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("from the phone", 0, at(c), false)
	v.msgs[len(v.msgs)-1].Origin = "telegram"
	v.AddUser("typed here", 0, at(c), false)
	out := plain(v)
	if !strings.Contains(out, "· telegram") {
		t.Fatalf("origin suffix missing:\n%s", out)
	}
	v.msgs[len(v.msgs)-1].Origin = "local"
	if out := plain(v); strings.Count(out, "· telegram") != 1 || strings.Contains(out, "· local") {
		t.Fatalf("local origin should not show:\n%s", out)
	}
}

func TestVerboseQuotesSystemReminders(t *testing.T) {
	src := "Done.\n<system-reminder>keep the diff small</system-reminder>\nNext."
	if got := quoteReminders(src); strings.Contains(got, "<system-reminder>") ||
		!strings.Contains(got, "> ⚙ **system reminder**") || !strings.Contains(got, "> keep the diff small") {
		t.Fatalf("quoteReminders = %q", got)
	}

	v, c := newView(t, Options{Verbose: true})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	text(v, src, c)
	finish(v, c, time.Second)
	if out := plain(v); !strings.Contains(out, "system reminder") || !strings.Contains(out, "keep the diff small") {
		t.Fatalf("verbose view did not show the reminder:\n%s", out)
	}

	auto, ac := newView(t, Options{})
	auto.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(ac)})
	text(auto, src, ac)
	finish(auto, ac, time.Second)
	if out := plain(auto); strings.Contains(out, "system reminder") || strings.Contains(out, "keep the diff small") {
		t.Fatalf("auto layout should hide the reminder:\n%s", out)
	}
}

func TestQuoteRemindersLeavesUnclosedBlocks(t *testing.T) {
	src := "text <system-reminder>still streaming"
	if got := quoteReminders(src); got != src {
		t.Fatalf("unclosed block changed: %q", got)
	}
}

func TestMergeToolsJoinsAdjacentRuns(t *testing.T) {
	a := &Segment{Kind: "tools", Calls: []*Call{{ID: "1"}}}
	b := &Segment{Kind: "tools", Calls: []*Call{{ID: "2"}, {ID: "3"}}}
	txt := &Segment{Kind: "text", Text: "between"}
	c := &Segment{Kind: "tools", Calls: []*Call{{ID: "4"}}}
	in := []*Segment{a, b, txt, c}

	out := mergeTools(in)
	if len(out) != 3 {
		t.Fatalf("got %d segments, want 3", len(out))
	}
	if n := len(out[0].Calls); n != 3 {
		t.Fatalf("merged run has %d calls, want 3", n)
	}
	if out[1] != txt || out[2] != c {
		t.Fatalf("non-tool segments were not kept in place")
	}
	if len(a.Calls) != 1 || len(in) != 4 {
		t.Fatalf("mergeTools changed its input")
	}
}

func TestStreamingReasoningShowsCountOnlyWhenMultiline(t *testing.T) {
	v, c := newView(t, Options{ShowReasoning: true})
	one := plainBuilder(v.renderReasoning(&Segment{Kind: "reasoning", Text: "one line", Open: true}, 80, c.now()))
	if strings.Contains(one, "line)") {
		t.Fatalf("single streaming line shows a count: %q", one)
	}
	two := plainBuilder(v.renderReasoning(&Segment{Kind: "reasoning", Text: "one\ntwo", Open: true}, 80, c.now()))
	if !strings.Contains(two, "(2 lines)") {
		t.Fatalf("two streaming lines show no count: %q", two)
	}
}

func plainBuilder(b builder) string {
	var sb strings.Builder
	for _, l := range b.lines {
		sb.WriteString(ansi.Strip(l))
		sb.WriteString("\n")
	}
	return sb.String()
}

func TestDripRestartsWhenTextShrinks(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := &Segment{Kind: "text", Text: strings.Repeat("word ", 40), Open: true}
	advanceSeg(s, base)
	for i := 1; i <= 20; i++ {
		advanceSeg(s, base.Add(time.Duration(i)*dripTick))
	}
	if s.shown == 0 {
		t.Fatalf("drip did not reveal anything before the shrink")
	}
	s.Text = "short"
	advanceSeg(s, base.Add(time.Second))
	if s.shown != 0 {
		t.Fatalf("drip kept its old position after the text shrank: shown=%d", s.shown)
	}
	advanceSeg(s, base.Add(2*time.Second))
	advanceSeg(s, base.Add(3*time.Second))
	if visibleText(s) != "short" {
		t.Fatalf("restarted drip did not finish the new text: %q", visibleText(s))
	}
}

func TestSafeCutNeverSplitsGraphemeOrEscape(t *testing.T) {
	// A combining accent must stay with its base letter.
	g := []rune("éx")
	if got := safeCut(g, 1, 0); got != 0 {
		t.Errorf("cut inside a grapheme: got %d, want 0", got)
	}
	// ZWJ emoji sequences stay whole.
	z := []rune("a\U0001F469‍\U0001F4BBb")
	if got := safeCut(z, 2, 0); got != 1 {
		t.Errorf("cut inside a ZWJ sequence: got %d, want 1", got)
	}

	// A CSI sequence is held back to whichever edge is nearer.
	e := []rune("a\x1b[31mred")
	if got := safeCut(e, 3, 0); got != 1 {
		t.Errorf("cut inside an escape: got %d, want 1", got)
	}
	if got := safeCut(e, 5, 0); got != 6 {
		t.Errorf("cut near the end of an escape: got %d, want 6", got)
	}
	// An escape still arriving is held back entirely.
	open := []rune("a\x1b[31")
	if got := safeCut(open, 3, 0); got != 1 {
		t.Errorf("unterminated escape revealed: got %d, want 1", got)
	}
	// Plain text is untouched.
	if got := safeCut([]rune("plain"), 3, 0); got != 3 {
		t.Errorf("plain cut moved: got %d", got)
	}
}

func TestDripNeverRevealsPartialEscape(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	text := "ok \x1b[38;5;208mtinted\x1b[0m done"
	s := &Segment{Kind: "text", Text: text, Open: true}
	advanceSeg(s, base)
	for i := 1; i <= 200; i++ {
		advanceSeg(s, base.Add(time.Duration(i)*dripTick))
		vis := visibleText(s)
		if idx := strings.LastIndex(vis, "\x1b"); idx >= 0 && !strings.ContainsRune(vis[idx:], 'm') {
			t.Fatalf("drip stopped after a bare ESC: %q", vis)
		}
	}
	if visibleText(s) != text {
		t.Fatalf("drip did not finish: %q", visibleText(s))
	}
}
