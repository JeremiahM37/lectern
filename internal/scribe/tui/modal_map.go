package tui

// The code map view (/map, /repo-map): counts, a language breakdown, build
// progress, rebuild, and the summary and budget settings from spec 02 5.1.7.

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// MapDetail is what the modal shows beyond MapStats. A backend that implements
// MapDetailer reports it; without one the rows are left out.
type MapDetail struct {
	Enabled    bool   // the saved on/off setting; it applies at the next start
	Running    bool   // the map is running in this session
	DBBytes    int64  // cache file size; 0 when the map is in memory
	Summaries  int    // model summaries that still match their file
	Summarizer string // model used for summaries; "" when none is configured
	Warning    string // partial-coverage note from the last build
}

// MapDetailer is implemented by a backend that can report MapDetail.
type MapDetailer interface {
	MapDetail() MapDetail
}

// MapEnabler saves the code map's on/off setting. Turning it on or off takes
// effect the next time Scribe starts, because the map is built at startup.
type MapEnabler interface {
	SetMapEnabled(on bool) error
}

func init() {
	run := func(m *Model, args string) tea.Cmd {
		if strings.TrimSpace(args) == "rebuild" {
			return m.rebuildMap()
		}
		m.pushModal(newMapView(m))
		return nil
	}
	// /repo-map is an alias of /map (commands_catalog.go) and opens this modal too.
	overrideCommands(&command{Name: "map", Args: "[rebuild]", Desc: "Code map: counts, languages, rebuild", Group: "Intelligence", Run: run})
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

// Focus rows in the settings block.
const (
	mapRowMode = iota
	mapRowLimit
	mapRowBudget
	mapRowEnabled
	mapRows
)

type mapView struct {
	m     *Model
	th    theme.Theme
	be    Backend
	note  string
	prefs MapPrefs // what the modal shows and will apply
	saved MapPrefs // what the backend last reported
	// enabled is the on/off row; savedEnabled is what the backend reported.
	enabled      bool
	savedEnabled bool
	focus        int
	// clearArmed and sumsArmed hold a destructive action until its key is
	// pressed a second time.
	clearArmed bool
	sumsArmed  bool
}

func newMapView(m *Model) *mapView {
	v := &mapView{m: m, th: m.th, be: m.cur().be}
	v.prefs = defaultMapPrefs()
	if ctl, ok := v.be.(MapController); ok {
		v.prefs = ctl.MapPrefs()
		if v.prefs.Mode == "" {
			v.prefs.Mode = "ast"
		}
		if v.prefs.SummaryLimit == 0 {
			v.prefs.SummaryLimit = 300
		}
	}
	v.saved = v.prefs
	v.enabled = true
	if det, ok := v.be.(MapDetailer); ok {
		v.enabled = det.MapDetail().Enabled
	}
	v.savedEnabled = v.enabled
	return v
}

// Title implements Modal.
func (v *mapView) Title() string { return "Code map" }

// Icon implements Iconed.
func (v *mapView) Icon() string { return "repomap" }

// Footer implements Footered.
func (v *mapView) Footer() string {
	return "1-5 mode · ←→ step · a auto · e on/off · Enter apply · G C X summaries · Esc close"
}

// Size implements Sized.
func (v *mapView) Size(cols, rows int) (int, int) {
	return min(cols-4, 84), min(rows-4, 26)
}

// controller returns the backend's map controller, or nil.
func (v *mapView) controller() MapController {
	ctl, _ := v.be.(MapController)
	return ctl
}

// Update implements Modal.
func (v *mapView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	key := km.String()
	if key != "X" {
		v.clearArmed = false
	}
	if key != "C" {
		v.sumsArmed = false
	}
	switch key {
	case "esc", "q":
		return v, nil, true
	case "r":
		if v.be.MapStats().Building {
			v.note = "Already building."
			return v, nil, false
		}
		v.be.RebuildMap()
		v.note = "Rebuilding the code map…"
	case "R":
		v.note = "Refreshed."
	case "up", "k":
		v.focus = (v.focus + mapRows - 1) % mapRows
	case "down", "j", "tab":
		v.focus = (v.focus + 1) % mapRows
	case "left", "h":
		v.step(-1)
	case "right", "l":
		v.step(1)
	case "1", "2", "3", "4", "5":
		v.prefs.Mode = mapModes[int(key[0]-'1')]
		v.focus = mapRowMode
	case "a":
		v.prefs.AutoRegen = !v.prefs.AutoRegen
	case "e":
		v.enabled = !v.enabled
	case "enter":
		v.apply()
	case "G":
		v.regenerate()
	case "C":
		if !v.sumsArmed {
			v.sumsArmed = true
			v.note = "Press C again to clear every model summary."
			return v, nil, false
		}
		v.sumsArmed = false
		if ctl := v.controller(); ctl == nil {
			v.note = unsupportedNote
		} else if err := ctl.ClearSummaries(); err != nil {
			v.note = "Could not clear summaries: " + err.Error()
		} else {
			v.note = "Model summaries cleared."
		}
	case "X":
		if v.be.MapStats().Building {
			v.note = "Already building."
			return v, nil, false
		}
		if !v.clearArmed {
			v.clearArmed = true
			v.note = "Press X again to clear the code map. A rebuild restores it."
			return v, nil, false
		}
		v.clearArmed = false
		if ctl := v.controller(); ctl == nil {
			v.note = unsupportedNote
		} else if err := ctl.ClearMap(); err != nil {
			v.note = "Could not clear the map: " + err.Error()
		} else {
			v.note = "Code map cleared. Press r to rebuild it."
		}
	}
	return v, nil, false
}

const unsupportedNote = "This build cannot change the code map from here."

// step moves the focused setting one place. The mode row takes 1 to 5 instead.
func (v *mapView) step(dir int) {
	switch v.focus {
	case mapRowMode:
		i := mapIndexOf(mapModes, v.prefs.Mode) + dir
		v.prefs.Mode = mapModes[(i+len(mapModes))%len(mapModes)]
	case mapRowLimit:
		i := mapIndexOf(mapLimits, v.prefs.SummaryLimit) + dir
		v.prefs.SummaryLimit = mapLimits[(i+len(mapLimits))%len(mapLimits)]
	case mapRowBudget:
		i := mapIndexOf(mapBudgets, v.prefs.Budget) + dir
		v.prefs.Budget = mapBudgets[(i+len(mapBudgets))%len(mapBudgets)]
	case mapRowEnabled:
		v.enabled = !v.enabled
	}
}

// apply sends the staged settings to the backend.
func (v *mapView) apply() {
	ctl := v.controller()
	if ctl == nil {
		v.note = unsupportedNote
		return
	}
	if err := ctl.SetMapPrefs(v.prefs); err != nil {
		v.note = "Could not apply: " + err.Error()
		return
	}
	v.saved = v.prefs
	v.note = "Applied."
	if v.enabled == v.savedEnabled {
		return
	}
	en, ok := v.be.(MapEnabler)
	if !ok {
		v.enabled = v.savedEnabled
		v.note = unsupportedNote
		return
	}
	if err := en.SetMapEnabled(v.enabled); err != nil {
		// A project file may still hold the value in use, so show that one.
		if det, ok := v.be.(MapDetailer); ok {
			v.enabled = det.MapDetail().Enabled
			v.savedEnabled = v.enabled
		} else {
			v.enabled = v.savedEnabled
		}
		v.note = "Could not save the on/off setting: " + err.Error()
		return
	}
	v.savedEnabled = v.enabled
	v.note = "Applied. The on/off change takes effect the next time Scribe starts."
}

// regenerate asks the backend for model summaries in the background.
func (v *mapView) regenerate() {
	ctl := v.controller()
	switch {
	case ctl == nil:
		v.note = unsupportedNote
	case v.prefs.Mode != "llm" && v.prefs.Mode != "full":
		v.note = "Summaries are written in llm or full mode. Press 4 or 5 first."
	default:
		if err := ctl.RegenerateSummaries(); err != nil {
			v.note = "Could not regenerate: " + err.Error()
			return
		}
		v.note = "Regenerating summaries in the background."
	}
}

// View implements Modal.
func (v *mapView) View(w, h int) string {
	th := v.th
	s := v.be.MapStats()
	label := func(k string) string { return fg(th.TextDim).Render(padRight(k, 10)) }
	value := func(n int) string { return fg(th.TextPrimary).Render(thousands(n)) }
	det, hasDet := v.be.(MapDetailer)
	var d MapDetail
	if hasDet {
		d = det.MapDetail()
	}

	var out []string
	out = append(out,
		label("Files")+value(s.Files),
		label("Symbols")+value(s.Symbols),
		label("Edges")+value(s.Edges),
		"",
	)
	switch {
	case hasDet && !d.Running:
		out = append(out, label("Status")+fg(th.TextDim).Render("off"),
			fg(th.TextFaint).Render("The code map is not running in this session. The on/off setting applies at the next start."))
	case s.Building:
		frac := clampFrac(s.Progress)
		out = append(out, label("Building")+fg(th.Brand).Render(bar(frac, min(30, w-20)))+" "+fg(th.TextMuted).Render(fmt.Sprintf("%d%%", int(frac*100+0.5))))
	case s.Files == 0 && s.Symbols == 0:
		out = append(out, fg(th.TextFaint).Render("The code map is empty. Press r to build it."))
	default:
		out = append(out, label("Status")+fg(th.Success).Render("ready"))
	}
	if hasDet && d.Running {
		out = append(out, detailLines(d, label, th, w)...)
	}
	out = append(out, "")
	out = append(out, v.settingsLines()...)
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

// settingsLines renders the three staged settings, with the focused one marked.
func (v *mapView) settingsLines() []string {
	th := v.th
	mark := func(row int) string {
		if v.focus == row {
			return fg(th.Brand).Render("› ")
		}
		return "  "
	}
	opt := func(cur string, all []string, name func(string) string) string {
		var parts []string
		for _, o := range all {
			if o == cur {
				parts = append(parts, fg(th.Brand).Bold(true).Render("["+name(o)+"]"))
			} else {
				parts = append(parts, fg(th.TextDim).Render(name(o)))
			}
		}
		return strings.Join(parts, " ")
	}
	var limits []string
	for _, n := range mapLimits {
		limits = append(limits, fmt.Sprint(n))
	}
	var budgets []string
	for _, n := range mapBudgets {
		budgets = append(budgets, fmt.Sprint(n))
	}
	budgetCur := "0"
	if v.prefs.Budget > 0 {
		budgetCur = fmt.Sprint(v.prefs.Budget)
	}
	budgetName := func(s string) string {
		if s == "0" {
			return "auto"
		}
		return s
	}
	auto := "off"
	if v.prefs.AutoRegen {
		auto = "on"
	}
	enabled := "off"
	if v.enabled {
		enabled = "on"
	}
	lines := []string{
		mark(mapRowMode) + padRight("Summaries", 12) + opt(v.prefs.Mode, mapModes, identity) + fg(th.TextFaint).Render("  1-5"),
		mark(mapRowLimit) + padRight("Limit", 12) + opt(fmt.Sprint(v.prefs.SummaryLimit), limits, identity) + fg(th.TextFaint).Render("  llm and full only"),
		mark(mapRowBudget) + padRight("Budget", 12) + opt(budgetCur, budgets, budgetName) + fg(th.TextFaint).Render("  tokens"),
		"  " + padRight("Auto-regen", 12) + fg(th.TextPrimary).Render(auto) + fg(th.TextFaint).Render("  a"),
		mark(mapRowEnabled) + padRight("Enabled", 12) + fg(th.TextPrimary).Render(enabled) + fg(th.TextFaint).Render("  e  applies at next start"),
	}
	if v.prefs != v.saved || v.enabled != v.savedEnabled {
		lines = append(lines, fg(th.Amber).Render("  Not applied yet. Press Enter to apply."))
	}
	return lines
}

// detailLines are the cache, summary and warning rows of a running map.
func detailLines(d MapDetail, label func(string) string, th theme.Theme, w int) []string {
	primary := func(s string) string { return fg(th.TextPrimary).Render(s) }
	faint := func(s string) string { return fg(th.TextFaint).Render(s) }
	cache := "in memory"
	if d.DBBytes > 0 {
		cache = humanBytes(d.DBBytes)
	}
	ai := fmt.Sprintf("%d files", d.Summaries)
	if d.Summarizer == "" {
		ai += faint("  no model configured for summaries")
	} else {
		ai += faint("  via " + d.Summarizer)
	}
	lines := []string{
		label("Cache") + primary(cache),
		label("AI summary") + primary(ai),
	}
	if d.Warning != "" {
		lines = append(lines, label("Warning")+faint(render.Truncate(d.Warning, max(4, w-12))))
	}
	return lines
}

func identity(s string) string { return s }

func clampFrac(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
