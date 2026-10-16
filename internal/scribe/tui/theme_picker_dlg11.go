package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// themePickerKeys is the footer of the /theme picker. Tab, M, D and B change
// the saved theme style; the picker keeps its letter keys for search.
const themePickerKeys = "↑↓ preview · Enter keep · Esc revert · Tab/M/D/B style"

// themeVariantWord is "light" or "dark" for a palette.
func themeVariantWord(th theme.Theme) string {
	if th.Variant == "light" {
		return "light"
	}
	return "dark"
}

// themeStyleLine describes the saved style, shown above the theme list.
func (m *Model) themeStyleLine() string {
	c := fields7(m.cur().be)
	on := "off"
	if config.Bool(c.ThemeTransparent, false) {
		on = "on"
	}
	msg, diff := 100, 100
	if c.ThemeMessageOpacity != nil {
		msg = *c.ThemeMessageOpacity
	}
	if c.ThemeDiffOpacity != nil {
		diff = *c.ThemeDiffOpacity
	}
	border := c.ThemeBorder
	if border == "" {
		border = "default"
	}
	return fmt.Sprintf("Style: transparent %s · message %d%% · diff %d%% · border %s", on, msg, diff, border)
}

// themePickerKey changes the saved theme style from the /theme picker. It
// only acts when the picker is not filtering, so typing a theme name still
// searches. Each change is saved to the global config and applied at once.
func (m *Model) themePickerKey(p *Picker, key string) (tea.Cmd, bool) {
	if p.searching || p.query != "" {
		return nil, false
	}
	be := m.cur().be
	setter, ok := be.(ConfigSetter7)
	if !ok {
		return nil, false
	}
	c := fields7(be)
	var err error
	switch key {
	case "tab":
		err = setter.SetField7("theme_transparent", !config.Bool(c.ThemeTransparent, false), false)
	case "m":
		err = setter.SetField7("theme_message_opacity", nextSavedOpacity(c.ThemeMessageOpacity), false)
	case "d":
		err = setter.SetField7("theme_diff_opacity", nextSavedOpacity(c.ThemeDiffOpacity), false)
	case "b":
		err = setter.SetField7("theme_border", nextBorderSaved(c.ThemeBorder), false)
	default:
		return nil, false
	}
	if err != nil {
		m.setFlash("err", "Could not save the theme style: "+err.Error())
		return nil, true
	}
	m.applyThemeStyle()
	p.Blurb = m.themeStyleLine()
	return nil, true
}

// nextSavedOpacity steps a saved opacity through config.ThemeOpacitySteps.
func nextSavedOpacity(cur *int) int {
	steps := config.ThemeOpacitySteps
	now := 100
	if cur != nil {
		now = *cur
	}
	for i, v := range steps {
		if v == now {
			return steps[(i+1)%len(steps)]
		}
	}
	return steps[0]
}

// nextBorderSaved steps the border strength; "" and "default" are the same.
func nextBorderSaved(cur string) string {
	if cur == "" {
		cur = config.ThemeBorders[0]
	}
	next := config.ThemeBorders[(listIndex(config.ThemeBorders, cur)+1)%len(config.ThemeBorders)]
	if next == config.ThemeBorders[0] {
		return ""
	}
	return next
}

// showAlert opens a dismiss-only error dialog over the screen. Use it for a
// failure the user asked for and must notice; the flash line still shows it.
func (m *Model) showAlert(title, text string) {
	m.pushModal(NewAlert(m.th, title, text, "error"))
}
