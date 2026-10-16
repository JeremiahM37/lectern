package tui

// /watchdog: the model stream thresholds. These are separate from /timeouts,
// which limits shell and project commands. Values are global, as the other
// direct settings are; the agent reads them when a session starts.

import (
	"fmt"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// watchdogField describes one threshold: its flat key, its label, the
// default in seconds, and the allowed range shown in its prompt.
type watchdogField struct {
	key, label, note string
	def, lo, hi      int
	choices          []int
}

// watchdogFields are the three stream thresholds, in the order they show.
var watchdogFields = []watchdogField{
	{key: "watchdog.first_token", label: "First token", note: "wait for first output",
		def: 180, lo: 30, hi: 600, choices: []int{60, 120, 180, 300, 600}},
	{key: "watchdog.inter_token", label: "Between chunks", note: "longest gap mid-answer",
		def: 120, lo: 30, hi: 600, choices: []int{30, 60, 120, 300, 600}},
	{key: "watchdog.step", label: "Whole model call", note: "start to finish",
		def: 900, lo: 120, hi: 1800, choices: []int{300, 600, 900, 1200, 1800}},
}

// watchdogValue is the current threshold in seconds for a field, or its
// default when unset.
func watchdogValue(w config.Watchdog, key string) int {
	var n int
	switch key {
	case "watchdog.first_token":
		n = w.FirstToken
	case "watchdog.inter_token":
		n = w.InterToken
	case "watchdog.step":
		n = w.Step
	}
	return n
}

// watchdogLabel shows a threshold as "3m" or "45s", with the default marked.
func watchdogLabel(n, def int) string {
	var s string
	if n%60 == 0 && n >= 60 {
		s = strconv.Itoa(n/60) + "m"
	} else {
		s = strconv.Itoa(n) + "s"
	}
	if n == 0 {
		return fmt.Sprintf("default (%s)", watchdogLabel(def, def))
	}
	return s
}

// openWatchdog is the /watchdog picker: one row per threshold, each with
// presets and a custom value, and a row that clears them all.
func (m *Model) openWatchdog() {
	p := NewPicker(m.th, "Stream Watchdog", "clock")
	p.KeepOpen = true
	p.Hint = "↑↓ move · Enter set · Esc close · shell commands are under /timeouts"
	rebuild := func() {
		w := fields7(m.cur().be).WatchdogTimeouts
		p.Items = nil
		for _, f := range watchdogFields {
			cur := watchdogValue(w, f.key)
			p.Items = append(p.Items, PickItem{Value: f.key, Label: f.label,
				Desc: f.note + " · " + watchdogLabel(cur, f.def), Current: cur != 0})
		}
		p.Items = append(p.Items, PickItem{Value: "reset", Label: "Reset to defaults", Desc: "clear all three thresholds"})
		p.SetItems(p.Items)
	}
	p.OnSelect = func(it PickItem) tea.Cmd {
		if it.Value == "reset" {
			for _, f := range watchdogFields {
				if err := m.saveGlobalFlat(f.key, nil); err != nil {
					return errFlash(err)
				}
			}
			rebuild()
			return flashCmd("ok", "Stream watchdog reset to defaults; applies to the next session")
		}
		f, ok := watchdogFieldFor(it.Value)
		if !ok {
			return nil
		}
		m.pushModal(m.watchdogPicker(f, rebuild))
		return nil
	}
	rebuild()
	m.pushModal(p)
}

// watchdogPicker offers the presets for one threshold, plus a custom entry.
func (m *Model) watchdogPicker(f watchdogField, done func()) *Picker {
	p := NewPicker(m.th, f.label, "clock")
	p.Hint = fmt.Sprintf("↑↓ move · Enter set · Esc close · %d to %d seconds", f.lo, f.hi)
	cur := watchdogValue(fields7(m.cur().be).WatchdogTimeouts, f.key)
	for _, c := range f.choices {
		p.Items = append(p.Items, PickItem{Value: strconv.Itoa(c), Label: watchdogLabel(c, f.def),
			Current: c == cur, Desc: fmt.Sprintf("%d seconds", c)})
	}
	p.Items = append(p.Items,
		PickItem{Value: "default", Label: fmt.Sprintf("Default (%s)", watchdogLabel(f.def, f.def)), Current: cur == 0},
		PickItem{Value: "custom", Label: "Custom…", Desc: fmt.Sprintf("%d to %d seconds, or 3m", f.lo, f.hi)})
	p.SetItems(p.Items)
	p.OnSelect = func(it PickItem) tea.Cmd {
		var value any = it.Value
		switch it.Value {
		case "default":
			value = nil
		case "custom":
			return m.askWatchdogValue(f, done)
		}
		if err := m.saveGlobalFlat(f.key, value); err != nil {
			return errFlash(err)
		}
		done()
		return flashCmd("ok", f.label+": "+watchdogLabelFor(value, f))
	}
	return p
}

// askWatchdogValue reads a custom threshold. A value outside the range is
// refused with the range, and the prompt stays open for a second try.
func (m *Model) askWatchdogValue(f watchdogField, done func()) tea.Cmd {
	m.pushModal(NewTextPrompt(m.th, f.label, fmt.Sprintf("seconds, %d to %d, or 3m", f.lo, f.hi), false, func(v string) tea.Cmd {
		if err := m.saveGlobalFlat(f.key, v); err != nil {
			return errFlash(err)
		}
		done()
		return flashCmd("ok", f.label+" set; applies to the next session")
	}))
	return nil
}

func watchdogFieldFor(key string) (watchdogField, bool) {
	for _, f := range watchdogFields {
		if f.key == key {
			return f, true
		}
	}
	return watchdogField{}, false
}

// watchdogLabelFor names a saved value for the confirmation flash.
func watchdogLabelFor(v any, f watchdogField) string {
	if v == nil {
		return "default"
	}
	n, err := strconv.Atoi(fmt.Sprint(v))
	if err != nil {
		return fmt.Sprint(v)
	}
	return watchdogLabel(n, f.def)
}
