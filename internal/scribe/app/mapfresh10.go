package app

import (
	"path"
	"strings"
)

// refreshCodeMap draws the code map the system prompt carries. The map is a
// snapshot (spec 03 §4.9): it is drawn once, when the first build is done,
// and again only after a compaction replaced the history. Drawing it on
// every change would alter the system prompt mid-session, which breaks
// provider prompt caching; the prompt tells the model the map may be stale
// and to read a file before editing it, and code_map gives a live view.
func (a *App) refreshCodeMap() {
	cm := a.ext.cmap
	if cm == nil {
		return
	}
	select {
	case <-a.ext.mapOK:
	default:
		return
	}
	g := cm.Generation()
	a.mapMu.Lock()
	a.mu.Lock()
	drawn := a.codeMap != ""
	a.mu.Unlock()
	if g == a.mapGen || (drawn && !a.mapStale) {
		a.mapMu.Unlock()
		return
	}
	a.mapGen = g
	a.mapStale = false
	a.mapMu.Unlock()
	text := cm.Render(budgetOr(mapBudget()), nil)
	a.mu.Lock()
	a.codeMap = text
	a.mu.Unlock()
}

// recallFiles is the file list memory recall works from: the files this
// session edited, plus the files the code map showed the model that the
// prompt names by path or by a distinctive base name. Those are the files
// the model was just told about, so a memory about them is relevant.
func (a *App) recallFiles(prompt string) []string {
	files := a.editedFilesSnapshot()
	if a.ext.cmap == nil {
		return files
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f] = true
	}
	for _, f := range a.ext.cmap.RenderedPaths() {
		if seen[f] || !mentionsFile(prompt, f) {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	return files
}

// mentionsFile reports whether prompt names file: its full path, or its base
// name when that is distinctive enough (six or more characters, so "a.go"
// does not match every prompt that says "a").
func mentionsFile(prompt, file string) bool {
	if strings.Contains(prompt, file) {
		return true
	}
	base := path.Base(file)
	return len(base) >= 6 && strings.Contains(prompt, base)
}

// mapBudget is the token budget the user chose for the map (0 is auto).
func mapBudget() int {
	mapUI.mu.Lock()
	defer mapUI.mu.Unlock()
	return mapUI.budget
}
