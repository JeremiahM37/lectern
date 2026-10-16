package tui

// The skills browser (/skills and Ctrl+S). Three tabs: Search (a remote
// registry, when the backend has one), Installed (everything on disk) and
// Active (loaded into this project's AI context). A skill can be installed
// from a path or git URL (Ctrl+N), loaded or unloaded, and removed from disk
// (Ctrl+D on Installed).

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(&command{Name: "skills", Desc: "Browse, install and load skills", Group: "Intelligence",
		Run: func(m *Model, _ string) tea.Cmd { return m.openSkillBrowser() }})
}

// SkillDetail describes one installed skill.
type SkillDetail struct {
	Name, Description, Scope, Dir, Problem string // Scope: project | global
}

// SkillCatalog is optionally implemented by a Backend that can describe its
// skills. Without it the browser lists Backend.Skills() by name only.
type SkillCatalog interface {
	SkillDetails() []SkillDetail
	// SkillBody returns the instructions a skill gives the model.
	SkillBody(name string) (string, error)
}

// SkillRemover is optionally implemented by a Backend that can delete an
// installed skill's directory. The browser unloads a skill before removing it.
type SkillRemover interface {
	RemoveSkill(name string) error
}

// RegistrySkill is one entry of a remote skill registry.
type RegistrySkill struct {
	ID       string // what the list shows, e.g. "owner/review"
	Source   string // what InstallSkill is given, e.g. a git URL
	Installs int
}

// SkillRegistry is optionally implemented by a Backend that can list and
// search a remote registry. Without it the Search tab says so.
type SkillRegistry interface {
	PopularSkills() ([]RegistrySkill, error)
	SearchSkills(query string) ([]RegistrySkill, error)
}

// skillSearchDelay is how long typing waits before a registry search runs.
const skillSearchDelay = 300 * time.Millisecond

// skillSearchDue fires once the query has been quiet for skillSearchDelay.
// A search whose seq is no longer current is dropped.
type skillSearchDue struct {
	seq int
	q   string
}

// skillTab is one tab of the browser.
type skillTab int

const (
	skillTabSearch skillTab = iota
	skillTabInstalled
	skillTabActive
	skillTabCount
)

var skillTabNames = [...]string{"Search", "Installed", "Active"}

// openSkillBrowser opens the browser. Without a registry it opens on
// Installed, since Search would have nothing to show.
func (m *Model) openSkillBrowser() tea.Cmd {
	be := m.cur().be
	b := &skillBrowser{m: m, be: be, th: m.th, byName: map[string]SkillDetail{}, loaded: map[string]bool{}, src: map[string]string{}}
	b.cat, _ = be.(SkillCatalog)
	b.reg, _ = be.(SkillRegistry)
	b.rem, _ = be.(SkillRemover)

	p := NewPicker(m.th, "Skills", "skills")
	p.KeepOpen = true
	p.TypeToFilter = true // letters, j and k included, always filter
	p.Preview = b.preview
	p.OnSelect = b.selectItem
	b.Picker = p

	b.tab = skillTabInstalled
	if b.reg != nil {
		b.tab = skillTabSearch
	}
	cmd := b.setTab(b.tab)
	m.pushModal(b)
	return cmd
}

// skillBrowser wraps the Picker. It owns the tabs, the registry search and
// the keys the Picker does not know, and passes everything else through.
type skillBrowser struct {
	*Picker
	m   *Model
	be  Backend
	th  theme.Theme
	cat SkillCatalog
	reg SkillRegistry
	rem SkillRemover

	tab     skillTab
	seq     int  // bumped on every query change; older searches are dropped
	pending bool // a registry search is waiting for the typing to pause
	popular []RegistrySkill
	havePop bool
	src     map[string]string // registry ID -> install source
	byName  map[string]SkillDetail
	loaded  map[string]bool
	onDisk  map[string]bool
}

// Update implements Modal.
func (b *skillBrowser) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case skillSearchDue:
		if msg.seq != b.seq || b.tab != skillTabSearch {
			return b, nil, false
		}
		return b, b.search(msg.seq, msg.q), false
	case tea.KeyMsg:
		switch msg.String() {
		case "tab":
			return b, b.setTab((b.tab + 1) % skillTabCount), false
		case "shift+tab":
			return b, b.setTab((b.tab + skillTabCount - 1) % skillTabCount), false
		case "ctrl+d":
			return b, b.removeOrUnload(), false
		case "ctrl+o":
			b.viewInstructions()
			return b, nil, false
		case "ctrl+n":
			b.askInstallSource()
			return b, nil, false
		}
	}
	before := b.query
	_, cmd, done := b.Picker.Update(msg)
	if b.query != before {
		cmd = tea.Batch(cmd, b.queryChanged())
	}
	b.refreshHints()
	return b, cmd, done
}

// setTab switches tabs. The query and the cursor start over on every switch.
func (b *skillBrowser) setTab(t skillTab) tea.Cmd {
	b.tab = t
	b.query, b.searching, b.pending = "", false, false
	b.cursor, b.off = 0, 0
	b.Picker.Loading = false
	var cmd tea.Cmd
	switch t {
	case skillTabSearch:
		cmd = b.showPopular()
	default:
		b.reloadLocal()
	}
	b.refreshHints()
	return cmd
}

// refreshHints sets the tab strip, footer and empty text for the current tab.
func (b *skillBrowser) refreshHints() {
	var strip []string
	for i, n := range skillTabNames {
		if skillTab(i) == b.tab {
			strip = append(strip, "["+n+"]")
		} else {
			strip = append(strip, n)
		}
	}
	blurb := strings.Join(strip, "   ")
	if b.tab == skillTabInstalled {
		blurb += "  ·  add to .scribe/skills/ or ~/.claude/skills/"
	}
	b.Picker.Blurb = blurb
	filtering := strings.TrimSpace(b.query) != ""
	switch b.tab {
	case skillTabSearch:
		b.Picker.Hint = "↑↓ nav · ⏎ install · ^N from path · tab switch · esc close"
		switch {
		case b.reg == nil:
			b.Picker.Empty = "The skill registry is not available here. ^N installs from a path or git URL."
		case filtering && b.pending:
			b.Picker.Empty = "searching..."
		case filtering:
			b.Picker.Empty = "No results."
		default:
			b.Picker.Empty = "No popular skills."
		}
	case skillTabInstalled:
		b.Picker.Hint = "↑↓ nav · ⏎ load · ^D remove · ^O instructions · tab switch · esc close"
		b.Picker.Empty = "No skills installed. Add: .scribe/skills/<name>/SKILL.md  ·  ~/.claude/skills/<name>/"
		if filtering {
			b.Picker.Empty = "No matching skills."
		}
	case skillTabActive:
		b.Picker.Hint = "↑↓ nav · ⏎ unload · ^D unload · ^O instructions · tab switch · esc close"
		b.Picker.Empty = "No active skills. Load one from the Installed tab."
		if filtering {
			b.Picker.Empty = "No matching skills."
		}
	}
}

// queryChanged runs after a keystroke changed the query. The Picker has
// already refiltered the local tabs. Search waits for a pause in typing.
func (b *skillBrowser) queryChanged() tea.Cmd {
	if b.tab != skillTabSearch {
		return nil
	}
	b.seq++
	q := strings.TrimSpace(b.query)
	if q == "" {
		b.pending = false
		b.Picker.SetItems(nil)
		return b.showPopular()
	}
	b.pending = true
	b.Picker.SetItems(nil)
	seq := b.seq
	return tea.Tick(skillSearchDelay, func(time.Time) tea.Msg { return skillSearchDue{seq: seq, q: q} })
}

// search asks the registry in the background. The result is applied only if
// no keystroke has come since.
func (b *skillBrowser) search(seq int, q string) tea.Cmd {
	reg := b.reg
	return asyncModal(b, func() func() (tea.Cmd, bool) {
		res, err := reg.SearchSkills(q)
		return func() (tea.Cmd, bool) {
			if seq != b.seq || b.tab != skillTabSearch {
				return nil, false
			}
			b.pending = false
			if err != nil {
				b.Picker.SetItems(nil)
				b.Picker.Empty = "Search failed: " + err.Error()
				return nil, false
			}
			b.setRegistryItems(res)
			return nil, false
		}
	})
}

// showPopular fills the Search tab with the registry's popular list. It is
// fetched once per browser; later visits to the tab reuse it.
func (b *skillBrowser) showPopular() tea.Cmd {
	if b.reg == nil {
		b.Picker.SetItems(nil)
		return nil
	}
	if b.havePop {
		b.setRegistryItems(b.popular)
		return nil
	}
	b.Picker.SetItems(nil)
	b.Picker.Loading, b.Picker.Spin = true, "◌"
	reg := b.reg
	return asyncModal(b, func() func() (tea.Cmd, bool) {
		res, err := reg.PopularSkills()
		return func() (tea.Cmd, bool) {
			b.Picker.Loading = false
			if err != nil {
				b.Picker.Empty = "Could not load popular skills: " + err.Error()
				return nil, false
			}
			b.popular, b.havePop = res, true
			if b.tab == skillTabSearch && strings.TrimSpace(b.query) == "" {
				b.setRegistryItems(res)
			}
			return nil, false
		}
	})
}

// setRegistryItems shows registry entries on the Search tab. Skills already
// on disk get a check mark.
func (b *skillBrowser) setRegistryItems(res []RegistrySkill) {
	b.readOnDisk()
	items := make([]PickItem, 0, len(res))
	for _, r := range res {
		b.src[r.ID] = r.Source
		desc := ""
		if r.Installs > 0 {
			desc = groupDigits(r.Installs) + "↓"
		}
		items = append(items, PickItem{Value: r.ID, Label: r.ID, Desc: desc, Current: b.onDisk[r.ID]})
	}
	b.Picker.SetItems(items)
}

// readOnDisk records which skill names are installed.
func (b *skillBrowser) readOnDisk() {
	b.onDisk = map[string]bool{}
	for _, n := range b.be.Skills() {
		b.onDisk[n] = true
	}
}

// reloadLocal reads installed and loaded skills and rebuilds the Installed or
// Active list.
func (b *skillBrowser) reloadLocal() {
	var details []SkillDetail
	if b.cat != nil {
		details = b.cat.SkillDetails()
	} else {
		for _, n := range b.be.Skills() {
			details = append(details, SkillDetail{Name: n, Scope: "project"})
		}
	}
	sort.SliceStable(details, func(i, j int) bool {
		if details[i].Scope != details[j].Scope {
			return details[i].Scope == "project"
		}
		return details[i].Name < details[j].Name
	})
	b.byName = map[string]SkillDetail{}
	for _, d := range details {
		b.byName[d.Name] = d
	}
	b.loaded = map[string]bool{}
	active := b.be.ActiveSkills()
	for _, n := range active {
		b.loaded[n] = true
	}

	var items []PickItem
	for _, d := range details {
		if b.tab == skillTabActive && !b.loaded[d.Name] {
			continue
		}
		it := PickItem{Value: d.Name, Label: d.Name, Desc: oneLine(d.Description), Current: b.loaded[d.Name]}
		if b.tab == skillTabInstalled {
			it.Group = "Global skills"
			if d.Scope == "project" {
				it.Group = "Project skills"
			}
		}
		if d.Problem != "" {
			it.Icon = icons.Icon("warning")
			it.Color = b.th.Warning
			it.Desc = "problem: " + oneLine(d.Problem)
		}
		items = append(items, it)
	}
	// A loaded skill whose directory has gone still shows, by name.
	if b.tab == skillTabActive {
		for _, n := range active {
			if _, ok := b.byName[n]; !ok {
				items = append(items, PickItem{Value: n, Label: n, Desc: "not found on disk", Current: true})
			}
		}
	}
	b.Picker.SetItems(items)
}

// refresh brings the active tab up to date after a change to the skills.
func (b *skillBrowser) refresh() {
	switch b.tab {
	case skillTabSearch:
		b.readOnDisk()
		for i := range b.Picker.Items {
			b.Picker.Items[i].Current = b.onDisk[b.Picker.Items[i].Value]
		}
		b.Picker.refilter()
	default:
		b.reloadLocal()
	}
}

// preview is the side pane: what the highlighted skill is and where it lives.
func (b *skillBrowser) preview(it PickItem) []string {
	th := b.th
	if b.tab == skillTabSearch {
		lines := []string{fgb(th.TextPrimary).Render(it.Value)}
		if src := b.src[it.Value]; src != "" {
			lines = append(lines, fg(th.TextDim).Render("source "+src))
		}
		state := "not installed"
		if b.onDisk[it.Value] {
			state = "installed"
		}
		return append(lines, fg(th.TextDim).Render("state  "+state), "", fg(th.TextFaint).Render("Enter installs it, asking where first."))
	}
	d, ok := b.byName[it.Value]
	if !ok {
		return nil
	}
	lines := []string{fgb(th.TextPrimary).Render(d.Name)}
	if d.Scope != "" {
		lines = append(lines, fg(th.TextDim).Render("scope  "+d.Scope))
	}
	if d.Dir != "" {
		lines = append(lines, fg(th.TextDim).Render("dir    "+d.Dir))
	}
	state := "not loaded"
	if b.loaded[d.Name] {
		state = "loaded into the AI context"
	}
	lines = append(lines, fg(th.TextDim).Render("context "+state))
	lines = append(lines, "")
	if d.Description != "" {
		lines = append(lines, wrapLines(d.Description, 44)...)
	} else {
		lines = append(lines, fg(th.TextFaint).Render("No description in SKILL.md."))
	}
	if d.Problem != "" {
		lines = append(lines, "", fg(th.Warning).Render("Problem: "+oneLine(d.Problem)))
	}
	lines = append(lines, "", fg(th.TextFaint).Render("ctrl+o shows the full instructions."))
	return lines
}

// selectItem is Enter: install on Search, load on Installed, unload on Active.
func (b *skillBrowser) selectItem(it PickItem) tea.Cmd {
	switch b.tab {
	case skillTabSearch:
		if b.onDisk[it.Value] {
			return flashCmd("info", fmt.Sprintf("Skill %q is already installed.", it.Value))
		}
		src := b.src[it.Value]
		if src == "" {
			src = it.Value
		}
		b.m.pushModal(installScopePicker(b.th, func(scope string) tea.Cmd {
			return installSkill(b.be, src, scope, b, b.refresh)
		}))
		return nil
	case skillTabInstalled:
		if err := b.be.SetSkillActive(it.Value, true); err != nil {
			return flashCmd("err", "Could not load "+it.Value+": "+err.Error())
		}
		b.refresh()
		return flashCmd("ok", fmt.Sprintf("Skill %q loaded into AI context.", it.Value))
	case skillTabActive:
		if err := b.be.SetSkillActive(it.Value, false); err != nil {
			return flashCmd("err", "Could not unload "+it.Value+": "+err.Error())
		}
		b.refresh()
		return flashCmd("ok", fmt.Sprintf("Skill %q unloaded from AI context.", it.Value))
	}
	return nil
}

// removeOrUnload is Ctrl+D. On Active it unloads at once. On Installed it
// asks first, since the skill's directory is deleted from disk; a yes unloads
// it and then removes it.
func (b *skillBrowser) removeOrUnload() tea.Cmd {
	it, ok := b.Picker.Selected()
	if !ok {
		return nil
	}
	name := it.Value
	switch b.tab {
	case skillTabActive:
		if err := b.be.SetSkillActive(name, false); err != nil {
			return flashCmd("err", "Could not unload "+name+": "+err.Error())
		}
		b.refresh()
		return flashCmd("ok", fmt.Sprintf("Skill %q unloaded.", name))
	case skillTabInstalled:
		if b.rem == nil {
			return flashCmd("err", "Removing skills is not available from this backend.")
		}
		where := "from disk"
		if b.loaded[name] {
			where = "from disk. It is unloaded from the AI context first"
		}
		b.m.pushModal(NewConfirm(b.th, "Remove skill",
			fmt.Sprintf("Remove %q %s? The skill's folder is deleted and cannot be restored here.", name, where), true,
			func() tea.Cmd { return b.removeInstalled(name) }).WithLabels("Remove", "Cancel"))
		return nil
	}
	return nil
}

// removeInstalled unloads and deletes an installed skill, once the person has
// said yes.
func (b *skillBrowser) removeInstalled(name string) tea.Cmd {
	if b.loaded[name] {
		if err := b.be.SetSkillActive(name, false); err != nil {
			return flashCmd("err", fmt.Sprintf("Failed to remove %q: %s", name, err.Error()))
		}
	}
	err := b.rem.RemoveSkill(name)
	b.refresh()
	if err != nil {
		return flashCmd("err", fmt.Sprintf("Failed to remove %q: %s", name, err.Error()))
	}
	return flashCmd("ok", fmt.Sprintf("Skill %q removed.", name))
}

// viewInstructions shows the full SKILL.md of the highlighted skill.
func (b *skillBrowser) viewInstructions() {
	it, ok := b.Picker.Selected()
	if !ok || b.tab == skillTabSearch {
		return
	}
	if b.cat == nil {
		b.m.pushModal(NewInfoView(b.th, it.Value, "skills", []string{"Skill details are not available from this backend."}))
		return
	}
	body, err := b.cat.SkillBody(it.Value)
	if err != nil {
		body = "Could not read this skill: " + err.Error()
	}
	b.m.pushModal(NewInfoView(b.th, it.Value, "skills", strings.Split(body, "\n")))
}

// askInstallSource is Ctrl+N: a path or git URL, then the scope.
func (b *skillBrowser) askInstallSource() {
	m, th := b.m, b.th
	m.pushModal(NewTextPrompt(th, "Install skill", "path or git URL, optionally #subdir", false, func(src string) tea.Cmd {
		m.pushModal(installScopePicker(th, func(scope string) tea.Cmd {
			return installSkill(b.be, src, scope, b, b.refresh)
		}))
		return nil
	}))
}

// groupDigits formats n with thousands separators, for install counts.
func groupDigits(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// installScopePicker asks where a skill goes, then calls onPick.
func installScopePicker(th theme.Theme, onPick func(scope string) tea.Cmd) *Picker {
	sp := NewPicker(th, "Install skill to", "skills")
	sp.Hint = "↑↓ move · Enter install · Esc cancel"
	sp.Items = []PickItem{
		{Value: "project", Label: "This project", Desc: ".scribe/skills/"},
		{Value: "global", Label: "Every project", Desc: "Scribe home skills folder"},
	}
	sp.refilter()
	sp.OnSelect = func(it PickItem) tea.Cmd { return onPick(it.Value) }
	return sp
}

// installSkill installs src into scope in the background. Its result lands on
// owner (the browser), which then reloads its list.
func installSkill(be Backend, src, scope string, owner Modal, reload func()) tea.Cmd {
	return asyncModal(owner, func() func() (tea.Cmd, bool) {
		name, err := be.InstallSkill(src, scope)
		return func() (tea.Cmd, bool) {
			if err != nil {
				return flashCmd("err", "Failed to install "+src+": "+err.Error()), false
			}
			reload()
			where := "this project"
			if scope == "global" {
				where = "every project"
			}
			return flashCmd("ok", fmt.Sprintf("Skill %q installed for %s.", name, where)), false
		}
	})
}
