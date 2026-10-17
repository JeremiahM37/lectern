package app

// Code map controls for the /repo-map modal (tui.MapController). The settings
// the modal edits are saved to the global config and read back from it: the
// summary mode, summary limit, auto-regenerate, token budget and the on/off
// switch. Nothing is kept only in this process, so a restart shows the same
// state the modal showed.

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// defaultSummaryLimit is how many files one model pass covers when the config
// names no limit.
const defaultSummaryLimit = 300

// mapUI mirrors the saved token budget for mapBudget (mapfresh10.go), which
// the code map renders read without a config in hand. The config is the source
// of truth: applyMapConfig seeds this at start and SetMapPrefs writes both.
var mapUI struct {
	mu     sync.Mutex
	budget int // 0 is auto
}

// Compile-time checks for the optional map capabilities.
var (
	_ tui.MapController = (*scribeBackend)(nil)
	_ tui.MapDetailer   = (*scribeBackend)(nil)
	_ tui.MapEnabler    = (*scribeBackend)(nil)
)

// errMapOverridden is returned when the global config was saved but a project
// file sets the same key, so the project value is the one in effect.
var errMapOverridden = errors.New("saved to the global config, but .scribe/config.json sets this too and that value is the one in use; change it there")

// mapSettings returns a copy of the merged settings.
func (a *App) mapSettings() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Cfg
}

// mapPrefsFrom turns saved settings into what the modal shows. Unset values
// take their defaults.
func mapPrefsFrom(c config.Config) tui.MapPrefs {
	p := tui.MapPrefs{
		Mode:         c.SemanticSummaries,
		SummaryLimit: c.SemanticSummaryLimit,
		AutoRegen:    config.Bool(c.SemanticAutoRegen, false),
		Budget:       c.RepoMapTokenBudget,
	}
	if p.Mode == "" {
		p.Mode = repomap.SummaryAST
	}
	if p.SummaryLimit <= 0 {
		p.SummaryLimit = defaultSummaryLimit
	}
	return p
}

// validMapMode reports whether mode names a summary mode the map knows.
func validMapMode(mode string) bool {
	switch mode {
	case "", repomap.SummaryAST, repomap.SummaryOff, repomap.SummarySynthetic, repomap.SummaryLLM, repomap.SummaryFull:
		return true
	}
	return false
}

// MapPrefs implements tui.MapController. It reads the saved settings.
func (b *scribeBackend) MapPrefs() tui.MapPrefs {
	return mapPrefsFrom(b.current().mapSettings())
}

// SetMapPrefs implements tui.MapController. It saves the settings to the global
// config, applies the values in effect to the running map and re-renders the
// map the agent is given.
func (b *scribeBackend) SetMapPrefs(p tui.MapPrefs) error {
	a := b.current()
	if a.ext.cmap == nil {
		return errors.New("the code map is not running in this session")
	}
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	if !validMapMode(mode) {
		return errors.New("unknown summary mode " + p.Mode)
	}
	if p.Budget < 0 || p.SummaryLimit < 0 {
		return errors.New("the budget and summary limit cannot be negative")
	}
	auto := p.AutoRegen
	want := config.Config{
		SemanticSummaries:    mode,
		SemanticSummaryLimit: p.SummaryLimit,
		SemanticAutoRegen:    &auto,
		RepoMapTokenBudget:   p.Budget,
	}
	if _, err := b.saveMapSettings(func(c *config.Config) {
		c.SemanticSummaries = want.SemanticSummaries
		c.SemanticSummaryLimit = want.SemanticSummaryLimit
		c.SemanticAutoRegen = want.SemanticAutoRegen
		c.RepoMapTokenBudget = want.RepoMapTokenBudget
	}); err != nil {
		return err
	}
	eff := b.MapPrefs()
	applyLiveMapPrefs(a, eff)
	if eff != mapPrefsFrom(want) {
		return errMapOverridden
	}
	return nil
}

// applyLiveMapPrefs gives the running map the settings in effect and re-renders
// the map the agent sees.
func applyLiveMapPrefs(a *App, p tui.MapPrefs) {
	a.ext.cmap.SetSummaryMode(p.Mode)
	a.ext.cmap.SetAutoRegen(p.AutoRegen)
	setMapBudget(p.Budget)
	a.mu.Lock()
	a.codeMap = a.ext.cmap.Render(budgetOr(p.Budget), nil)
	a.mu.Unlock()
}

// ClearMap implements tui.MapController.
func (b *scribeBackend) ClearMap() error {
	a := b.current()
	if a.ext.cmap == nil {
		return errors.New("the code map is not running in this session")
	}
	if err := a.ext.cmap.Clear(); err != nil {
		return err
	}
	a.mu.Lock()
	a.codeMap = ""
	a.mu.Unlock()
	return nil
}

// ClearSummaries implements tui.MapController.
func (b *scribeBackend) ClearSummaries() error {
	a := b.current()
	if a.ext.cmap == nil {
		return errors.New("the code map is not running in this session")
	}
	return a.ext.cmap.ClearSummaries()
}

// RegenerateSummaries implements tui.MapController. It drops the summaries and
// asks the model again in the background.
func (b *scribeBackend) RegenerateSummaries() error {
	a := b.current()
	if a.ext.cmap == nil {
		return errors.New("the code map is not running in this session")
	}
	if err := a.ext.cmap.ClearSummaries(); err != nil {
		return err
	}
	go a.summarizeAndRender(a.ext.cmap, mapPrefsFrom(a.mapSettings()).SummaryLimit)
	return nil
}

// MapDetail implements tui.MapDetailer. Every value comes from memory, one
// stat call, or the configured model name.
func (b *scribeBackend) MapDetail() tui.MapDetail {
	a := b.current()
	d := tui.MapDetail{
		Enabled: config.Bool(a.mapSettings().RepoMap, true),
		Running: a.ext.cmap != nil,
	}
	if a.ext.cmap == nil {
		return d
	}
	st := a.ext.cmap.Status()
	d.DBBytes = st.DBBytes
	d.Summaries = st.Summaries
	d.Warning = st.Warning
	if model, ok := a.summarizerModel(); ok {
		d.Summarizer = model.Ref
	}
	return d
}

// SetMapEnabled implements tui.MapEnabler. The map is built at startup, so the
// change takes effect the next time Scribe starts.
func (b *scribeBackend) SetMapEnabled(on bool) error {
	if _, err := b.saveMapSettings(func(c *config.Config) {
		v := on
		c.RepoMap = &v
	}); err != nil {
		return err
	}
	if config.Bool(b.current().mapSettings().RepoMap, true) != on {
		return errMapOverridden
	}
	return nil
}

// saveMapSettings applies change to the global config file and reloads the
// merged settings. It writes the global file only, so a project file keeps its
// own values; the caller checks which value is in effect.
func (b *scribeBackend) saveMapSettings(change func(*config.Config)) (config.Config, error) {
	a := b.current()
	raw, err := loadConfigFile(configPathFor(a.Workdir, false))
	if err != nil {
		return config.Config{}, err
	}
	change(&raw)
	if err := b.saveAndReload(a, raw, false); err != nil {
		return config.Config{}, err
	}
	return a.mapSettings(), nil
}

// applyMapConfig gives a freshly opened map the saved settings. wire.go calls it
// right after repomap.Open, before the first build.
func applyMapConfig(a *App, m *repomap.Map) {
	c := a.mapSettings()
	m.SetSummaryMode(c.SemanticSummaries)
	m.SetAutoRegen(config.Bool(c.SemanticAutoRegen, false))
	setMapBudget(c.RepoMapTokenBudget)
}

// finishMapBuild runs after each map build, at startup and on a /repo-map
// rebuild. With auto-regenerate on, it asks the model for summaries of the
// files that have none. Only the llm and full modes show summaries, so the
// model is not called in the other modes.
func (a *App) finishMapBuild(m *repomap.Map) {
	if !autoRegenWanted(m) {
		return
	}
	go a.summarizeAndRender(m, mapPrefsFrom(a.mapSettings()).SummaryLimit)
}

// autoRegenWanted reports whether a finished build should ask the model for
// summaries: auto-regenerate is on and the mode shows summaries.
func autoRegenWanted(m *repomap.Map) bool {
	if m == nil || !m.AutoRegen() {
		return false
	}
	mode := m.SummaryMode()
	return mode == repomap.SummaryLLM || mode == repomap.SummaryFull
}

// summarizeAndRender asks the summarizer for up to limit missing summaries and
// re-renders the map when it stored any. It runs in the background.
func (a *App) summarizeAndRender(m *repomap.Map, limit int) {
	n, err := m.SummarizeMissing(context.Background(), limit)
	if err != nil || n == 0 {
		return
	}
	a.mu.Lock()
	a.codeMap = m.Render(budgetOr(mapBudget()), nil)
	a.mu.Unlock()
}

// setMapBudget updates the mirror mapBudget reads.
func setMapBudget(n int) {
	mapUI.mu.Lock()
	mapUI.budget = n
	mapUI.mu.Unlock()
}

// budgetOr returns the chosen budget, or the conversation default when auto.
func budgetOr(budget int) int {
	if budget > 0 {
		return budget
	}
	return repomap.Budget(0)
}
