package render

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func TestMain(m *testing.M) {
	// Tests assert on real escape codes, so force a colour profile.
	lipgloss.SetColorProfile(termenv.TrueColor)
	icons.SetNerd(false)
	os.Exit(m.Run())
}

func plain(s string) string { return ansi.Strip(s) }

func md(src string, width int) string {
	return plain(Markdown(src, width, theme.Default(), false))
}

func maxWidth(s string) int {
	w := 0
	for _, l := range strings.Split(s, "\n") {
		if n := ansi.StringWidth(l); n > w {
			w = n
		}
	}
	return w
}

// ---- markdown ----

func TestParagraphWrapsWithinWidth(t *testing.T) {
	src := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron"
	out := md(src, 24)
	if maxWidth(out) > 24 {
		t.Fatalf("line wider than 24:\n%s", out)
	}
	if got := strings.Join(strings.Fields(out), " "); got != src {
		t.Errorf("words changed:\n got %q\nwant %q", got, src)
	}
	if strings.Count(out, "\n") < 2 {
		t.Errorf("expected several lines:\n%s", out)
	}
}

func TestWideRunesCountAsTwoCells(t *testing.T) {
	out := md("漢字漢字漢字漢字漢字漢字", 10)
	if maxWidth(out) > 10 {
		t.Fatalf("CJK overflow:\n%s", out)
	}
}

func TestBulletListHangingIndent(t *testing.T) {
	out := md("- one two three four five six seven\n- next", 14)
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "• ") {
		t.Fatalf("first line %q", lines[0])
	}
	if len(lines) < 3 || !strings.HasPrefix(lines[1], "  ") || strings.HasPrefix(lines[1], "  •") {
		t.Errorf("continuation not hanging-indented:\n%s", out)
	}
	if !strings.Contains(out, "• next") {
		t.Errorf("second item missing:\n%s", out)
	}
}

func TestNestedListsUseDeeperBullets(t *testing.T) {
	out := md("- a\n  - b\n    - c", 40)
	for _, want := range []string{"• a", "◦ b", "▪ c"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// tight list: nested items follow the parent directly, with no blank line
	if strings.Contains(out, "\n\n") {
		t.Errorf("tight list has blank lines:\n%s", out)
	}
}

func TestOrderedAndTaskLists(t *testing.T) {
	out := md("1. first\n2. second\n\n- [ ] todo\n- [x] done", 40)
	for _, want := range []string{"1. first", "2. second", "☐ todo", "☑ done"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestHeadingH1HasRule(t *testing.T) {
	lines := strings.Split(md("# Title here\n\nbody", 40), "\n")
	if lines[0] != "Title here" {
		t.Fatalf("heading line %q", lines[0])
	}
	if strings.Trim(lines[1], "─") != "" || len([]rune(lines[1])) != len("Title here") {
		t.Errorf("H1 rule %q", lines[1])
	}
}

func TestInlineMarkupStrippedOfMarkers(t *testing.T) {
	out := md("a **bold** and *em* and ~~gone~~ and `code` end", 80)
	if out != "a bold and em and gone and code end" {
		t.Errorf("inline rendering = %q", out)
	}
}

func TestEscapesAndLiteralStars(t *testing.T) {
	out := md(`5 * 3 = 15 and \*literal\*`, 80)
	if out != "5 * 3 = 15 and *literal*" {
		t.Errorf("got %q", out)
	}
}

func TestLinkShowsURLAndEmitsOSC8(t *testing.T) {
	raw := Markdown("[the spec](https://example.com/spec)", 80, theme.Default(), false)
	if !strings.Contains(raw, "\x1b]8;;https://example.com/spec\x1b\\") {
		t.Errorf("no OSC 8 hyperlink start in %q", raw)
	}
	if got := plain(raw); got != "the spec (https://example.com/spec)" {
		t.Errorf("plain link = %q", got)
	}
}

func TestBareURLIsLinkified(t *testing.T) {
	raw := Markdown("see https://example.com/x.", 80, theme.Default(), false)
	if !strings.Contains(raw, "\x1b]8;;https://example.com/x\x1b\\") {
		t.Errorf("trailing period should not be part of the link: %q", raw)
	}
}

func TestBlockQuoteRail(t *testing.T) {
	out := md("> quoted text that is long enough to wrap around the rail here\n> second", 24)
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "│ ") {
			t.Errorf("quote line without rail: %q", l)
		}
		if ansi.StringWidth(l) > 24 {
			t.Errorf("quote line too wide: %q", l)
		}
	}
}

func TestCodeFenceLabelAndBody(t *testing.T) {
	out := md("```go\nx := 1\n```\nafter", 40)
	lines := strings.Split(out, "\n")
	if lines[0] != "  go" {
		t.Errorf("language label = %q", lines[0])
	}
	if !strings.Contains(out, "x := 1") {
		t.Errorf("code body missing:\n%s", out)
	}
	if strings.Contains(out, "```") {
		t.Errorf("fence markers leaked:\n%s", out)
	}
	if !strings.Contains(out, "after") {
		t.Errorf("text after the fence missing")
	}
}

func TestStreamingUnclosedFenceRunsToEnd(t *testing.T) {
	src := "intro\n\n```python\nprint('hi')\nfor i in range(3"
	got := plain(Markdown(src, 40, theme.Default(), true))
	if !strings.Contains(got, "print('hi')") || !strings.Contains(got, "range(3") {
		t.Errorf("streaming fence lost code:\n%s", got)
	}
	if strings.Contains(got, "```") {
		t.Errorf("fence marker shown:\n%s", got)
	}
}

func TestStreamingUnclosedEmphasisStaysLiteral(t *testing.T) {
	got := plain(Markdown("this is **bold without an end", 80, theme.Default(), true))
	if got != "this is **bold without an end" {
		t.Errorf("got %q", got)
	}
	got = plain(Markdown("partial [link](https://x.y", 80, theme.Default(), true))
	if !strings.Contains(got, "partial") {
		t.Errorf("partial link dropped text: %q", got)
	}
}

func TestSystemReminderStripped(t *testing.T) {
	got := md("keep <system-reminder>secret note</system-reminder> this", 80)
	if strings.Contains(got, "secret") || !strings.Contains(got, "keep") || !strings.Contains(got, "this") {
		t.Errorf("reminder handling wrong: %q", got)
	}
	streaming := plain(Markdown("visible <system-reminder>half", 80, theme.Default(), true))
	if strings.Contains(streaming, "half") || strings.Contains(streaming, "system-reminder") {
		t.Errorf("open streaming reminder leaked: %q", streaming)
	}
	partial := plain(Markdown("visible <system-re", 80, theme.Default(), true))
	if partial != "visible" {
		t.Errorf("partial opening tag leaked: %q", partial)
	}
}

func TestTableBordersAndAlignment(t *testing.T) {
	out := md("| Name | Count |\n|:-----|------:|\n| read | 3 |\n| edit_file | 12 |", 60)
	for _, want := range []string{"╭", "┬", "╮", "├", "┼", "┤", "╰", "┴", "╯"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	lines := strings.Split(out, "\n")
	w := ansi.StringWidth(lines[0])
	for _, l := range lines {
		if ansi.StringWidth(l) != w {
			t.Errorf("ragged table row %q (want width %d)", l, w)
		}
	}
	// right-aligned count column: "3" sits at the right edge of its cell
	if !strings.Contains(out, "│     3 │") {
		t.Errorf("count not right aligned:\n%s", out)
	}
}

func TestTableFitsNarrowWidth(t *testing.T) {
	long := strings.Repeat("word ", 30)
	out := md("| a | b |\n|---|---|\n| "+long+" | x |", 30)
	if maxWidth(out) > 30 {
		t.Fatalf("table wider than 30:\n%s", out)
	}
}

func TestHorizontalRule(t *testing.T) {
	out := md("above\n\n---\n\nbelow", 20)
	if !strings.Contains(out, strings.Repeat("─", 20)) {
		t.Errorf("hr missing:\n%s", out)
	}
}

// ---- highlighter ----

func styled(th theme.Theme, c lipgloss.TerminalColor, text string) string {
	return lipgloss.NewStyle().Foreground(c).Render(text)
}

func TestHighlightTokenClassesGo(t *testing.T) {
	th := theme.Default()
	out := Highlight("func Run(n int) string { s := \"hi\" // note\n\treturn 42\n}", "go", th)
	checks := map[string]string{
		"keyword":  styled(th, th.SynKeyword, "func"),
		"function": styled(th, th.SynFunction, "Run"),
		"type":     styled(th, th.SynType, "int"),
		"string":   styled(th, th.SynString, "\"hi\""),
		"number":   styled(th, th.SynNumber, "42"),
		"comment":  lipgloss.NewStyle().Foreground(th.SynComment).Italic(true).Render("// note"),
	}
	for class, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("%s token not styled as expected; want %q in %q", class, want, out)
		}
	}
	if plain(out) != "func Run(n int) string { s := \"hi\" // note\n    return 42\n}" {
		t.Errorf("text changed: %q", plain(out))
	}
}

func TestHighlightPythonCommentAndKeyword(t *testing.T) {
	th := theme.Default()
	out := Highlight("def f(x):  # doc\n    return None", "python", th)
	if !strings.Contains(out, styled(th, th.SynKeyword, "def")) {
		t.Errorf("python def not a keyword")
	}
	if !strings.Contains(out, lipgloss.NewStyle().Foreground(th.SynComment).Italic(true).Render("# doc")) {
		t.Errorf("python comment not styled")
	}
}

func TestHighlightJSONKeysVersusStrings(t *testing.T) {
	th := theme.Default()
	out := Highlight(`{"name": "x", "n": 3, "ok": true}`, "json", th)
	if !strings.Contains(out, styled(th, th.SynVariable, `"name"`)) {
		t.Errorf("json key not variable-styled")
	}
	if !strings.Contains(out, styled(th, th.SynString, `"x"`)) {
		t.Errorf("json value not string-styled")
	}
	if !strings.Contains(out, styled(th, th.SynKeyword, "true")) {
		t.Errorf("json true not keyword")
	}
}

func TestHighlightBlockCommentSpansLines(t *testing.T) {
	th := theme.Default()
	code := "/* start\nmiddle */ int x;"
	out := Highlight(code, "c", th)
	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[1], lipgloss.NewStyle().Foreground(th.SynComment).Italic(true).Render("middle */")) {
		t.Errorf("second line of block comment not comment-styled: %q", lines[1])
	}
}

func TestLangFromPathAndAliases(t *testing.T) {
	cases := map[string]string{
		"a/b/c.go": "go", "x.tsx": "ts", "x.jsx": "ts", "x.py": "python", "x.rs": "rust",
		"x.yml": "yaml", "x.sh": "bash", "x.hpp": "cpp", "x.rb": "ruby", "x.sql": "sql",
		"x.html": "html", "x.scss": "css", "x.md": "markdown", "x.patch": "diff",
		"x.unknownext": "", "Makefile": "",
	}
	for path, want := range cases {
		if got := LangFromPath(path); got != want {
			t.Errorf("LangFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestHighlightUnknownLanguageIsPlain(t *testing.T) {
	out := Highlight("func x() {}", "klingon", theme.Default())
	if plain(out) != "func x() {}" {
		t.Errorf("text changed: %q", plain(out))
	}
}

// ---- diff ----

const smallDiff = "--- a/f.go\n+++ b/f.go\n@@ -3,3 +3,3 @@\n a\n-b\n+c\n d\n"

func TestDiffTitleAndCounts(t *testing.T) {
	out := plain(Diff(smallDiff, 50, theme.Default(), DiffOpts{}))
	first := strings.Split(out, "\n")[0]
	if !strings.Contains(first, "Edit ─ f.go +1 -1") {
		t.Errorf("title row = %q", first)
	}
	if maxWidth(out) != 50 {
		t.Errorf("box width = %d, want 50", maxWidth(out))
	}
}

func TestDiffLineNumberGutters(t *testing.T) {
	out := plain(Diff(smallDiff, 50, theme.Default(), DiffOpts{LineNumbers: true}))
	// Body rows sit between the box borders. Gutter is old, new, then sign.
	want := map[string]bool{
		"3 3   a": false, // context: old 3, new 3, blank sign
		"4   - b": false, // removed: old 4 only
		"  4 + c": false, // added: new 4 only
		"5 5   d": false,
	}
	for _, l := range strings.Split(out, "\n") {
		body := strings.TrimSuffix(strings.TrimPrefix(l, "│"), "│")
		body = strings.TrimRight(body, " ")
		if _, ok := want[body]; ok {
			want[body] = true
		}
	}
	for row, found := range want {
		if !found {
			t.Errorf("gutter row %q not found in:\n%s", row, out)
		}
	}
}

func TestDiffRowBackgrounds(t *testing.T) {
	th := theme.Default()
	raw := Diff(smallDiff, 50, th, DiffOpts{})
	bg := lipgloss.NewStyle().Background(th.DiffRemovedBg).Render("b")
	if !strings.Contains(raw, "\x1b[") || !strings.Contains(raw, "b") {
		t.Fatal("no styling")
	}
	// the removed row's background colour code appears on its content
	code := strings.TrimSuffix(strings.SplitN(bg, "b", 2)[0], "")
	if !strings.Contains(raw, code) {
		t.Errorf("removed row background %q not found", code)
	}
}

func TestDiffWordChangesMarkOnlyTheChangedWord(t *testing.T) {
	a, b := wordChanges("foo bar baz", "foo qux baz")
	if got := runesToString(a, "foo bar baz"); got != "bar" {
		t.Errorf("old changed = %q, want bar", got)
	}
	if got := runesToString(b, "foo qux baz"); got != "qux" {
		t.Errorf("new changed = %q, want qux", got)
	}
}

func runesToString(flags []bool, s string) string {
	var out []rune
	for i, r := range []rune(s) {
		if i < len(flags) && flags[i] {
			out = append(out, r)
		}
	}
	return string(out)
}

func TestDiffCollapsesContextRuns(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@ -1,25 +1,25 @@\n")
	for i := 0; i < 12; i++ {
		b.WriteString(" ctx\n")
	}
	b.WriteString("-old\n+new\n")
	for i := 0; i < 12; i++ {
		b.WriteString(" ctx\n")
	}
	out := plain(Diff(b.String(), 60, theme.Default(), DiffOpts{}))
	if !strings.Contains(out, "⋯ 10 unchanged lines") {
		t.Errorf("leading run not collapsed to 10:\n%s", out)
	}
	if strings.Count(out, "ctx") != 4 {
		t.Errorf("want 2 context lines each side, got %d:\n%s", strings.Count(out, "ctx"), out)
	}
}

func TestDiffInteriorRunKeepsTwoEachSide(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@ -1,12 +1,12 @@\n-a\n+b\n")
	for i := 0; i < 8; i++ {
		b.WriteString(" same\n")
	}
	b.WriteString("-c\n+d\n")
	out := plain(Diff(b.String(), 60, theme.Default(), DiffOpts{}))
	if !strings.Contains(out, "⋯ 4 unchanged lines") {
		t.Errorf("interior run not collapsed:\n%s", out)
	}
}

func TestDiffLargeIsSummarised(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@ -1,60 +1,60 @@\n")
	for i := 0; i < 60; i++ {
		b.WriteString("-x\n+y\n")
	}
	out := plain(Diff(b.String(), 60, theme.Default(), DiffOpts{}))
	if !strings.Contains(out, "120 lines changed") {
		t.Errorf("large diff not summarised:\n%s", out)
	}
}

func TestDiffCreationTitleAndCollapse(t *testing.T) {
	var b strings.Builder
	b.WriteString("--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,14 @@\n")
	for i := 1; i <= 14; i++ {
		b.WriteString("+line\n")
	}
	out := plain(Diff(b.String(), 60, theme.Default(), DiffOpts{}))
	if !strings.Contains(strings.Split(out, "\n")[0], "New ─ new.go +14") {
		t.Errorf("creation title = %q", strings.Split(out, "\n")[0])
	}
	if !strings.Contains(out, "⋯ 9 lines") {
		t.Errorf("creation diff not collapsed to first 3 and last 2:\n%s", out)
	}
}

func TestDiffSideBySideFitsWidth(t *testing.T) {
	out := plain(Diff(smallDiff, 60, theme.Default(), DiffOpts{SideBySide: true, LineNumbers: true}))
	if maxWidth(out) != 60 {
		t.Errorf("side by side width %d, want 60\n%s", maxWidth(out), out)
	}
	if !strings.Contains(out, "│") {
		t.Errorf("no column divider")
	}
}

func TestDiffMaxLines(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@ -1,10 +1,10 @@\n")
	for i := 0; i < 10; i++ {
		b.WriteString("+x\n")
	}
	out := plain(Diff(b.String(), 40, theme.Default(), DiffOpts{MaxLines: 4}))
	if !strings.Contains(out, "more lines") {
		t.Errorf("no truncation marker:\n%s", out)
	}
}

// ---- spinners and helpers ----

func TestSpinnerFrames(t *testing.T) {
	if Frame(Braille, 0) != "⠋" || Frame(Braille, 10) != "⠋" || Frame(Braille, -1) != "⠏" {
		t.Errorf("braille wrap wrong")
	}
	if len(Edit) != 14 || Edit[0] != "▏" || Edit[7] != "█" {
		t.Errorf("edit ping-pong wrong: %v", Edit)
	}
	if len(Rune) != 15 || len(Explore) != 4 || len(Dispatch) != 4 || len(Dots) != 5 {
		t.Errorf("spinner lengths wrong")
	}
	if Frame(nil, 3) != "" {
		t.Errorf("empty set should give empty frame")
	}
}

func TestHumanHelpers(t *testing.T) {
	tok := map[int]string{842: "842", 1234: "1.2k", 8400: "8.4k", 9949: "9.9k", 9950: "10k",
		88_000: "88k", 200_000: "200k", 999_960: "1.0M", 1_200_000: "1.2M", 1_500_000: "1.5M"}
	for n, want := range tok {
		if got := HumanTokens(n); got != want {
			t.Errorf("HumanTokens(%d) = %q, want %q", n, got, want)
		}
	}
	dur := map[time.Duration]string{
		850 * time.Millisecond: "850ms",
		12 * time.Second:       "12s",
		65 * time.Second:       "1m 05s",
		60 * time.Second:       "1m",
		3725 * time.Second:     "1h 02m",
		2 * time.Hour:          "2h",
	}
	for d, want := range dur {
		if got := HumanDuration(d); got != want {
			t.Errorf("HumanDuration(%v) = %q, want %q", d, got, want)
		}
	}
	if got := Elapsed(65 * time.Second); got != "1m 05s" {
		t.Errorf("Elapsed = %q", got)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if got := RelTime(now.Add(-5*time.Minute), now); got != "5m ago" {
		t.Errorf("RelTime = %q", got)
	}
	if got := RelTime(now.Add(-30*time.Second), now); got != "just now" {
		t.Errorf("RelTime short = %q", got)
	}
}

func TestTruncateWrapPad(t *testing.T) {
	if got := Truncate("hello world", 6); got != "hello…" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("short", 10); got != "short" {
		t.Errorf("Truncate no-op = %q", got)
	}
	colored := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render("hello world")
	if got := plain(Truncate(colored, 5)); got != "hell…" {
		t.Errorf("ANSI-aware truncate = %q", got)
	}
	if got := plain(Wrap("aaa bbb ccc", 7)); got != "aaa bbb\nccc" {
		t.Errorf("Wrap = %q", got)
	}
	if got := PadRight("ab", 5); got != "ab   " {
		t.Errorf("PadRight = %q", got)
	}
	if got := ansi.StringWidth(PadRight(colored, 20)); got != 20 {
		t.Errorf("PadRight ANSI width = %d", got)
	}
}
