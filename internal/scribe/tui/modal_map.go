package tui

// The code map view (/map): counts, a language breakdown, build progress, rebuild.

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(&command{Name: "map", Args: "[rebuild]", Desc: "Code map: counts, languages, rebuild", Group: "Intelligence",
		Run: func(m *Model, args string) tea.Cmd {
			if strings.TrimSpace(args) == "rebuild" {
				return m.rebuildMap()
			}
			m.pushModal(&mapView{th: m.th, be: m.cur().be, m: m})
			return nil
		}})
}

// rebuildMap starts a rebuild unless one is already running.
func (m *Model) rebuildMap() tea.Cmd {
	be := m.cur().be
	if be.MapStats().Building {
		return flashCmd("info", "The code map is already building.")
	}
	be.RebuildMap()
	return flashCmd("info", "Rebuilding the code map…")
}

type mapView struct {
	m    *Model
	th   theme.Theme
	be   Backend
	note string
}

// Title implements Modal.
func (v *mapView) Title() string { return "Code map" }

// Icon implements Iconed.
func (v *mapView) Icon() string { return "repomap" }

// Footer implements Footered.
func (v *mapView) Footer() string { return "r rebuild · Esc close" }

// Size implements Sized.
func (v *mapView) Size(cols, rows int) (int, int) {
	return min(cols-4, 84), min(rows-4, 24)
}

// Update implements Modal.
func (v *mapView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	switch km.String() {
	case "esc", "q":
		return v, nil, true
	case "r":
		if v.be.MapStats().Building {
			v.note = "Already building."
			return v, nil, false
		}
		v.be.RebuildMap()
		v.note = "Rebuilding the code map…"
	}
	return v, nil, false
}

// View implements Modal.
func (v *mapView) View(w, h int) string {
	th := v.th
	s := v.be.MapStats()
	label := func(k string) string { return fg(th.TextDim).Render(padRight(k, 10)) }
	value := func(n int) string { return fg(th.TextPrimary).Render(thousands(n)) }

	var out []string
	out = append(out,
		label("Files")+value(s.Files),
		label("Symbols")+value(s.Symbols),
		label("Edges")+value(s.Edges),
		"",
	)
	switch {
	case s.Building:
		frac := clampFrac(s.Progress)
		out = append(out, label("Building")+fg(th.Brand).Render(bar(frac, min(30, w-20)))+" "+fg(th.TextMuted).Render(fmt.Sprintf("%d%%", int(frac*100+0.5))))
	case s.Files == 0 && s.Symbols == 0:
		out = append(out, fg(th.TextFaint).Render("The code map is empty. Press r to build it."))
	default:
		out = append(out, label("Status")+fg(th.Success).Render("ready"))
	}
	out = append(out, "")

	if len(s.Languages) > 0 {
		out = append(out, fgb(th.TextMuted).Render("Languages"))
		langs := make([]string, 0, len(s.Languages))
		maxN := 0
		for lang, n := range s.Languages {
			langs = append(langs, lang)
			maxN = max(maxN, n)
		}
		sort.Slice(langs, func(i, j int) bool {
			if s.Languages[langs[i]] != s.Languages[langs[j]] {
				return s.Languages[langs[i]] > s.Languages[langs[j]]
			}
			return langs[i] < langs[j]
		})
		total := 0
		for _, n := range s.Languages {
			total += n
		}
		nameW := 12
		countW := 8
		barW := max(4, w-nameW-countW-8)
		for _, lang := range langs {
			n := s.Languages[lang]
			frac := 0.0
			if maxN > 0 {
				frac = float64(n) / float64(maxN)
			}
			pct := 0
			if total > 0 {
				pct = n * 100 / total
			}
			out = append(out, padRight(lang, nameW)+fg(th.Brand).Render(bar(frac, barW))+" "+
				fg(th.TextPrimary).Render(padRight(thousands(n), countW-1))+fg(th.TextDim).Render(fmt.Sprintf(" %d%%", pct)))
		}
	}
	if v.note != "" {
		out = append(out, "", fg(th.TextMuted).Render(render.Truncate(v.note, w)))
	}
	return strings.Join(fitLines(out, w, h), "\n")
}

func clampFrac(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
