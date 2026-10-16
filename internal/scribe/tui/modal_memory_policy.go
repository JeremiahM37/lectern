package tui

// The memory settings sub-modal, opened with s from the memory browser. It
// edits where new memories are written, which stores automatic recall reads,
// and which settings file the choice is saved to.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// The rows of the settings form, and the values each row cycles through.
const (
	memPolicyWrite = iota
	memPolicyRead
	memPolicySaveTo
	memPolicyRows
)

var memPolicyWriteValues = []string{"project", "global", "none"}
var memPolicyReadValues = []string{"all", "project", "global", "none"}

// memPolicyForm edits a MemoryPolicy. onSaved runs after a successful save so
// the browser can refresh and say what was saved.
type memPolicyForm struct {
	th       theme.Theme
	be       MemoryPolicyBackend
	p        MemoryPolicy
	saveTo   string
	cursor   int
	note     string
	noteKind string
	onSaved  func(note string)
}

func newMemPolicyForm(th theme.Theme, be MemoryPolicyBackend, onSaved func(string)) *memPolicyForm {
	f := &memPolicyForm{th: th, be: be, p: be.MemoryPolicy(), onSaved: onSaved}
	f.p.WriteScope = orDefaultString(f.p.WriteScope, "project")
	f.p.ReadScope = orDefaultString(f.p.ReadScope, "all")
	// The project file is the natural place for a project's settings; without
	// a project open, the only file there is is the global one.
	f.saveTo = "global"
	if f.p.ProjectOpen {
		f.saveTo = "project"
	}
	return f
}

func orDefaultString(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// cycle moves a row's value by d through its choices.
func (f *memPolicyForm) cycle(d int) {
	switch f.cursor {
	case memPolicyWrite:
		f.p.WriteScope = nextValue(memPolicyWriteValues, f.p.WriteScope, d)
	case memPolicyRead:
		f.p.ReadScope = nextValue(memPolicyReadValues, f.p.ReadScope, d)
	case memPolicySaveTo:
		choices := []string{"global"}
		if f.p.ProjectOpen {
			choices = []string{"project", "global"}
		}
		f.saveTo = nextValue(choices, f.saveTo, d)
	}
}

// nextValue returns the entry d steps from cur in values, wrapping around.
func nextValue(values []string, cur string, d int) string {
	i := 0
	for j, v := range values {
		if v == cur {
			i = j
		}
	}
	return values[((i+d)%len(values)+len(values))%len(values)]
}

// Title implements Modal.
func (f *memPolicyForm) Title() string { return "Memory settings" }

// Icon implements Iconed.
func (f *memPolicyForm) Icon() string { return "memory" }

// Footer implements Footered.
func (f *memPolicyForm) Footer() string {
	return "↑↓ move · ←→ change · Ctrl+S save · Esc cancel"
}

// Size implements Sized.
func (f *memPolicyForm) Size(cols, rows int) (int, int) {
	return min(cols-4, 84), min(rows-4, 14)
}

// Update implements Modal.
func (f *memPolicyForm) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return f, nil, false
	}
	switch km.String() {
	case "esc":
		return f, nil, true
	case "up", "k", "shift+tab":
		f.cursor = clamp(f.cursor-1, 0, memPolicyRows-1)
	case "down", "j", "tab":
		f.cursor = clamp(f.cursor+1, 0, memPolicyRows-1)
	case "left", "h":
		f.cycle(-1)
	case "right", "l", "enter", " ":
		f.cycle(1)
	case "ctrl+s", "s":
		return f.save()
	}
	return f, nil, false
}

// save writes the settings and closes the form when that works.
func (f *memPolicyForm) save() (Modal, tea.Cmd, bool) {
	if err := f.be.SaveMemoryPolicy(f.p, f.saveTo); err != nil {
		f.note, f.noteKind = err.Error(), "err"
		return f, nil, false
	}
	note := fmt.Sprintf("Memory settings saved to the %s file", f.saveTo)
	if f.onSaved != nil {
		f.onSaved(note)
	}
	return f, flashCmd("ok", note), true
}

// rowText is one row: the label, the value and what the value means.
func (f *memPolicyForm) rowText(row int) (label, value, hint string) {
	switch row {
	case memPolicyWrite:
		hints := map[string]string{
			"project": "new memories go to this project",
			"global":  "new memories go to every project",
			"none":    "saves are refused until this is changed",
		}
		return "Write new memories to", f.p.WriteScope, hints[f.p.WriteScope]
	case memPolicyRead:
		hints := map[string]string{
			"all":     "recall reads both project and global memories",
			"project": "recall reads only this project",
			"global":  "recall reads only global memories",
			"none":    "nothing is recalled automatically",
		}
		return "Automatic recall reads", f.p.ReadScope, hints[f.p.ReadScope]
	}
	hints := map[string]string{
		"project": "applies to this project only",
		"global":  "applies to every project",
	}
	return "Save these settings to", f.saveTo + " file", hints[f.saveTo]
}

// View implements Modal.
func (f *memPolicyForm) View(w, h int) string {
	th := f.th
	var out []string
	for row := 0; row < memPolicyRows; row++ {
		marker := "  "
		label := fg(th.TextSecondary)
		if row == f.cursor {
			marker = fgb(th.BrandSecondary).Render("› ")
			label = fgb(th.TextPrimary)
		}
		_, value, hint := f.rowText(row)
		out = append(out,
			marker+label.Render(padRight(f.labelOf(row), 26))+fgb(th.Brand).Render(padRight(value, 12)),
			"    "+fg(th.TextDim).Render(render.Truncate(hint, max(8, w-6))))
	}
	out = append(out, "", fg(th.TextFaint).Render(render.Truncate(
		"Ctrl+S saves. The new values apply to the next save and recall.", w)))
	if f.note != "" {
		col := th.Error
		if f.noteKind == "ok" {
			col = th.Success
		}
		out = append(out, "", fg(col).Render(render.Truncate(f.note, w)))
	}
	return strings.Join(fitLines(out, w, h), "\n")
}

func (f *memPolicyForm) labelOf(row int) string {
	label, _, _ := f.rowText(row)
	return label
}
