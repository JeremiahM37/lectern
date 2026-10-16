package chat

import (
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// Diff styles for an edit's expanded box (Options.DiffStyle).
const (
	DiffDefault    = "default"    // unified body, numbered, in a box
	DiffSideBySide = "sidebyside" // old and new columns, in a box
	DiffCompact    = "compact"    // one line when the row is collapsed
)

// compactEdit is the one-line form of a finished edit, `✎ path +N -M`. It is
// shown in place of the diff while the row is collapsed, when the layout asks
// for collapsed diffs (CollapseDiffs or the compact style). Ctrl+O or a click
// opens the full box. Returns "" when the row has no compact form.
func (v *View) compactEdit(c *Call) string {
	if !v.opt.CollapseDiffs && v.opt.DiffStyle != DiffCompact {
		return ""
	}
	if !isEditTool(c.Tool) || c.Status != "done" || c.Result == nil || c.Result.IsError || c.Result.Diff == "" {
		return ""
	}
	di := parseDiff(c.Result.Diff)
	th := v.th
	line := fg(th.Brand, icons.Icon("pencil")) + " " + fg(th.TextPrimary, v.relPath(pathOf(c)))
	if di.added > 0 {
		line += " " + fg(th.Success, fmt.Sprintf("+%d", di.added))
	}
	if di.removed > 0 {
		line += " " + fg(th.Error, fmt.Sprintf("-%d", di.removed))
	}
	return "  " + line
}

// sideBySideBody draws a diff as two columns inside its own box. The box
// comes from the render package, which also owns the split layout.
func (v *View) sideBySideBody(unified string, cw int) []string {
	box := render.Diff(unified, maxInt(cw-2, 12), v.th, render.DiffOpts{SideBySide: true, LineNumbers: true})
	var out []string
	for _, l := range splitLines(box) {
		out = append(out, "  "+l)
	}
	return out
}

// SetDiffView changes how edit diffs are drawn. style is one of the Diff
// constants (anything else means the default). It applies to every row,
// finished or not, on the next build.
func (v *View) SetDiffView(style string, collapse bool) {
	switch style {
	case DiffSideBySide, DiffCompact:
	default:
		style = DiffDefault
	}
	if v.opt.DiffStyle == style && v.opt.CollapseDiffs == collapse {
		return
	}
	v.opt.DiffStyle = style
	v.opt.CollapseDiffs = collapse
	v.gen++
}
