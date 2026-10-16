package tui

// Small form controls shared by modals and settings: a toggle, a checkbox, a
// radio mark and three button looks. Each returns one styled cell or row, so
// callers place them; none of them owns keys.

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Toggle draws an on/off switch: [●━] ON in success green, [━●] OFF dimmed.
// A focused toggle gets the ▸ marker in front.
func Toggle(th theme.Theme, on, focused bool) string {
	mark := "  "
	if focused {
		mark = fgb(th.BrandSecondary).Render("▸ ")
	}
	if on {
		return mark + fg(th.Success).Render(toggleText(true))
	}
	return mark + fg(th.TextDim).Render(toggleText(false))
}

// toggleText is the switch alone, without the focus marker: "[●━] ON" or
// "[━●] OFF". Settings rows draw their toggle value with it.
func toggleText(on bool) string {
	if on {
		return "[●━] ON"
	}
	return "[━●] OFF"
}

// Checkbox draws [✓] when checked and [ ] when not.
func Checkbox(th theme.Theme, checked bool) string {
	if checked {
		return fg(th.Success).Render("[✓]")
	}
	return fg(th.TextMuted).Render("[ ]")
}

// Radio draws (●) for the selected choice and ( ) for the others.
func Radio(th theme.Theme, selected bool) string {
	if selected {
		return fg(th.Brand).Render("(●)")
	}
	return fg(th.TextMuted).Render("( )")
}

// ButtonVariant is the look of a button.
type ButtonVariant int

const (
	ButtonDefault ButtonVariant = iota // brand fill when focused
	ButtonDanger                       // error fill when focused
	ButtonGhost                        // text only, no fill
)

// Button draws a one-line button. Focused, a default or danger button is
// filled and a ghost button turns bold; unfocused, all three are muted text.
func Button(th theme.Theme, label string, v ButtonVariant, focused bool) string {
	if !focused {
		return fg(th.TextMuted).Render(" " + label + " ")
	}
	switch v {
	case ButtonDanger:
		return lipgloss.NewStyle().Bold(true).Foreground(th.BgApp).Background(th.Error).Render(" " + label + " ")
	case ButtonGhost:
		return fgb(th.TextPrimary).Render(" " + label + " ")
	}
	return lipgloss.NewStyle().Bold(true).Foreground(th.BgApp).Background(th.Brand).Render(" " + label + " ")
}
