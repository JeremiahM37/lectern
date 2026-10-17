package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func dkey(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// plain strips colour and layout codes so tests read what a user would see.
func plain(s string) string { return ansi.Strip(s) }

func TestAlertClosesOnEnterOrEscAndSwallowsOtherKeys(t *testing.T) {
	th := theme.Default()
	for _, k := range []string{"enter", "esc"} {
		dismissed := false
		m := &Model{}
		a := NewAlert(th, "Disk full", "Free some space.", "error").OnDismiss(func() { dismissed = true })
		m.pushModal(a)
		m.updateModal(dkey(k))
		if m.topModal() != nil {
			t.Fatalf("%s left the alert open", k)
		}
		if !dismissed {
			t.Fatalf("%s did not fire OnDismiss", k)
		}
	}
	a := NewAlert(th, "Note", "text", "info")
	if _, _, done := a.Update(dkey("x")); done {
		t.Fatal("an ordinary key closed the alert")
	}
}

func TestAlertLevelSetsIconAndBorder(t *testing.T) {
	th := theme.Default()
	cases := map[string]struct {
		icon   string
		border any
	}{
		"info":    {"info", th.Info},
		"warning": {"warning", th.Warning},
		"error":   {"fail", th.Error},
	}
	for level, want := range cases {
		a := NewAlert(th, "T", "m", level)
		if a.Icon() != want.icon {
			t.Fatalf("%s icon = %q, want %q", level, a.Icon(), want.icon)
		}
		if a.Border(th) != want.border {
			t.Fatalf("%s border mismatch", level)
		}
	}
	if got := plain(strings.Join(modalFrame(NewAlert(th, "Oops", "Nothing saved.", "error"), th, 80, 24), "\n")); !strings.Contains(got, "Nothing saved.") || !strings.Contains(got, "Enter/Esc close") {
		t.Fatalf("alert frame lacks message or footer:\n%s", got)
	}
}

func TestConfirmDangerDefaultsToNoAndSaysDelete(t *testing.T) {
	th := theme.Default()
	c := NewConfirm(th, "Delete it?", "This cannot be undone.", true, nil)
	if c.yes {
		t.Fatal("danger confirm should default to No")
	}
	view := plain(c.View(60, 5))
	if !strings.Contains(view, "▸ No") || !strings.Contains(view, "Yes, delete") {
		t.Fatalf("danger confirm buttons:\n%s", view)
	}
	if !strings.Contains(c.Footer(), "Y delete") {
		t.Fatalf("danger footer = %q", c.Footer())
	}
	plainC := NewConfirm(th, "Save?", "ok?", false, nil)
	if !strings.Contains(plain(plainC.View(60, 5)), "▸ Yes") || !strings.Contains(plainC.Footer(), "Y confirm") {
		t.Fatalf("plain confirm should default to Yes:\n%s", plain(plainC.View(60, 5)))
	}
}

func TestConfirmArrowsMoveTheMarker(t *testing.T) {
	th := theme.Default()
	yes := false
	c := NewConfirm(th, "Save?", "ok?", false, func() tea.Cmd { yes = true; return nil })
	c.Update(dkey("tab"))
	if c.yes {
		t.Fatal("tab should move the selection to No")
	}
	c.Update(dkey("enter"))
	if yes {
		t.Fatal("enter on No ran the yes action")
	}
}

func TestDialogSizesAreNamed(t *testing.T) {
	cases := []struct {
		s    DialogSize
		term int
		want int
	}{
		{DialogCompact, 120, 50},
		{DialogMedium, 120, 70},
		{DialogLarge, 120, 100},
		{DialogXLarge, 120, 116},
		{DialogXLarge, 200, 140},
		{DialogFull, 120, 120},
		{DialogCompact, 40, 36}, // never wider than the terminal less the margin
	}
	for _, c := range cases {
		if got := c.s.cols(c.term); got != c.want {
			t.Fatalf("size %d on %d cols = %d, want %d", c.s, c.term, got, c.want)
		}
	}
}

func TestPickerIsEightyByTwentyFourClampedToTheTerminal(t *testing.T) {
	p := NewPicker(theme.Default(), "Pick", "")
	if w, h := p.Size(120, 40); w != 80 || h != 24 {
		t.Fatalf("size on 120x40 = %dx%d, want 80x24", w, h)
	}
	if w, h := p.Size(60, 20); w != 56 || h != 16 {
		t.Fatalf("size on 60x20 = %dx%d, want 56x16", w, h)
	}
}

// stackCloser is a modal that records when it leaves the stack.
type stackCloser struct {
	name string
	log  *[]string
}

func (s *stackCloser) Update(tea.Msg) (Modal, tea.Cmd, bool) { return s, nil, false }
func (s *stackCloser) View(int, int) string                  { return s.name }
func (s *stackCloser) Title() string                         { return s.name }
func (s *stackCloser) OnClose()                              { *s.log = append(*s.log, s.name) }

func TestReplaceAndClearNotifyLeavingModals(t *testing.T) {
	var log []string
	m := &Model{}
	a := &stackCloser{name: "a", log: &log}
	b := &stackCloser{name: "b", log: &log}
	c := &stackCloser{name: "c", log: &log}
	m.pushModal(a)
	m.pushModal(b)
	m.replaceTop(c)
	if len(log) != 1 || log[0] != "b" {
		t.Fatalf("replace notified %v, want [b]", log)
	}
	if m.topModal() != c || len(m.modals) != 2 {
		t.Fatalf("replace left stack %d deep with top %v", len(m.modals), m.topModal())
	}
	m.clearModals()
	if len(m.modals) != 0 {
		t.Fatal("clear left modals behind")
	}
	if strings.Join(log, ",") != "b,c,a" {
		t.Fatalf("clear order = %v, want LIFO after replace", log)
	}
}

func pickerWith(items ...PickItem) *Picker {
	p := NewPicker(theme.Default(), "Commands", "")
	p.Items = items
	p.refilter()
	return p
}

func TestPickerSearchBoxIsThreeRowsWithPlaceholderAndCount(t *testing.T) {
	p := pickerWith(PickItem{Value: "a", Label: "alpha"}, PickItem{Value: "b", Label: "beta"}, PickItem{Value: "g", Label: "gamma"})
	box := plain(strings.Join(p.searchBox(40), "\n"))
	for _, want := range []string{"╭", "╰", "Search…", " 3 "} {
		if !strings.Contains(box, want) {
			t.Fatalf("search box lacks %q:\n%s", want, box)
		}
	}
	if len(p.searchBox(40)) != 3 {
		t.Fatalf("search box is %d rows, want 3", len(p.searchBox(40)))
	}
	p.query = "al"
	p.refilter()
	if box = plain(strings.Join(p.searchBox(40), "\n")); !strings.Contains(box, "al") || !strings.Contains(box, "1 / 3") {
		t.Fatalf("filtered box lacks query or M / N count:\n%s", box)
	}
}

func TestPickerDescriptionsShareAColumn(t *testing.T) {
	p := pickerWith(
		PickItem{Value: "a", Label: "/clear", Desc: "start over"},
		PickItem{Value: "b", Label: "/model provider/name", Desc: "switch model"},
		PickItem{Value: "c", Label: "/x", Desc: "short one"},
	)
	rows := p.renderRows(70)
	col := -1
	for _, r := range rows {
		line := plain(r)
		i := strings.Index(line, "start over")
		if i < 0 {
			i = strings.Index(line, "switch model")
		}
		if i < 0 {
			i = strings.Index(line, "short one")
		}
		if i < 0 {
			t.Fatalf("row lacks its description: %q", line)
		}
		w := ansi.StringWidth(line[:i])
		if col >= 0 && w != col {
			t.Fatalf("description columns differ: %d vs %d\n%s", col, w, plain(strings.Join(rows, "\n")))
		}
		col = w
	}
}

func TestPickerGroupCaretAndFlatWhileFiltering(t *testing.T) {
	p := pickerWith(
		PickItem{Value: "a", Label: "alpha", Group: "Session"},
		PickItem{Value: "b", Label: "beta", Group: "Session"},
		PickItem{Value: "c", Label: "gamma", Group: "Model"},
	)
	if got := plain(strings.Join(p.renderRows(60), "\n")); !strings.Contains(got, "▾ Session") || !strings.Contains(got, "▾ Model") {
		t.Fatalf("group headers lack the caret:\n%s", got)
	}
	p.query = "ga"
	p.refilter()
	if got := plain(strings.Join(p.renderRows(60), "\n")); strings.Contains(got, "▾") {
		t.Fatalf("filtered list still shows group headers:\n%s", got)
	}
}

func TestPickerEmptyAndSelectionCounter(t *testing.T) {
	p := pickerWith()
	p.query = "zzz"
	p.refilter()
	if got := plain(p.View(60, 12)); !strings.Contains(got, "· No results") {
		t.Fatalf("empty list lacks the empty text:\n%s", got)
	}

	var items []PickItem
	for i := 0; i < 40; i++ {
		items = append(items, PickItem{Value: itoa(i), Label: "item " + itoa(i)})
	}
	long := pickerWith(items...)
	long.cursor = 4
	if got := plain(long.View(60, 10)); !strings.Contains(got, "5 / 40") {
		t.Fatalf("overflowing list lacks the sel / total counter:\n%s", got)
	}
	short := pickerWith(items[:3]...)
	if got := plain(short.View(60, 10)); strings.Contains(got, " / 3") {
		t.Fatalf("short list shows a counter:\n%s", got)
	}
}

func TestPickerActionKeysFireBeforeTextEntry(t *testing.T) {
	p := pickerWith(PickItem{Value: "a", Label: "alpha"})
	fired := 0
	p.OnKey = func(key string) (tea.Cmd, bool) {
		if key == "x" {
			fired++
			return nil, true
		}
		return nil, false
	}
	p.Update(dkey("x"))
	if fired != 1 || p.query != "" {
		t.Fatalf("action key fired %d times, query %q; the letter should not have been typed", fired, p.query)
	}
	p.Update(dkey("a"))
	if p.query != "a" {
		t.Fatalf("an unhandled letter should start the filter, query %q", p.query)
	}
}
