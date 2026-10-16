package app

// Code map controls for the /repo-map modal (tui.MapController). The summary
// mode and the auto-regenerate choice live on the repomap.Map. The rendered
// budget and summary limit are kept here for this process only: they are not
// written to the config yet, and the startup and rebuild renders still use
// repomap.Budget(0).

import (
	"context"
	"errors"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

var mapUI struct {
	mu     sync.Mutex
	budget int // 0 is auto
	limit  int // files per summary pass
}

// MapPrefs implements tui.MapController.
func (b *scribeBackend) MapPrefs() tui.MapPrefs {
	a := b.current()
	mapUI.mu.Lock()
	p := tui.MapPrefs{Mode: "ast", SummaryLimit: 300, Budget: mapUI.budget}
	if mapUI.limit > 0 {
		p.SummaryLimit = mapUI.limit
	}
	mapUI.mu.Unlock()
	if a.ext.cmap != nil {
		p.Mode = a.ext.cmap.SummaryMode()
		p.AutoRegen = a.ext.cmap.AutoRegen()
	}
	return p
}

// SetMapPrefs implements tui.MapController. It applies the mode and budget at
// once and re-renders the map the agent is given.
func (b *scribeBackend) SetMapPrefs(p tui.MapPrefs) error {
	a := b.current()
	if a.ext.cmap == nil {
		return errors.New("the code map is not running in this session")
	}
	if !a.ext.cmap.SetSummaryMode(p.Mode) {
		return errors.New("unknown summary mode " + p.Mode)
	}
	a.ext.cmap.SetAutoRegen(p.AutoRegen)
	mapUI.mu.Lock()
	mapUI.budget, mapUI.limit = p.Budget, p.SummaryLimit
	mapUI.mu.Unlock()
	a.mu.Lock()
	a.codeMap = a.ext.cmap.Render(budgetOr(p.Budget), nil)
	a.mu.Unlock()
	return nil
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
// asks the model again in the background, then re-renders the map.
func (b *scribeBackend) RegenerateSummaries() error {
	a := b.current()
	if a.ext.cmap == nil {
		return errors.New("the code map is not running in this session")
	}
	if err := a.ext.cmap.ClearSummaries(); err != nil {
		return err
	}
	mapUI.mu.Lock()
	limit := mapUI.limit
	mapUI.mu.Unlock()
	if limit <= 0 {
		limit = 300
	}
	go func() {
		n, err := a.ext.cmap.SummarizeMissing(context.Background(), limit)
		if err != nil || n == 0 {
			return
		}
		mapUI.mu.Lock()
		budget := mapUI.budget
		mapUI.mu.Unlock()
		a.mu.Lock()
		a.codeMap = a.ext.cmap.Render(budgetOr(budget), nil)
		a.mu.Unlock()
	}()
	return nil
}

// budgetOr returns the chosen budget, or the conversation default when auto.
func budgetOr(budget int) int {
	if budget > 0 {
		return budget
	}
	return repomap.Budget(0)
}

var _ tui.MapController = (*scribeBackend)(nil)
