package app

import (
	"path"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// refreshCodeMap draws the code map the conversation carries. The map is
// latched into the history, never the system prompt (agent/codemap_latch.go),
// so a change does not alter the system prompt. Providers that cache the
// prompt prefix (spec 03 §4.9) keep one snapshot until a compaction replaces
// the history, so the cached bytes do not move mid-session. Other providers
// get a fresh map whenever the map changes, and the agent latches only the
// changed lines. code_map gives a live view either way.
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
	caches := a.modelCachesPrompt()
	g := cm.Generation()
	a.mapMu.Lock()
	a.mu.Lock()
	drawn := a.codeMap != ""
	a.mu.Unlock()
	frozen := drawn && !a.mapStale && caches
	if g == a.mapGen || frozen {
		a.mapMu.Unlock()
		return
	}
	a.mapGen = g
	a.mapStale = false
	a.mapMu.Unlock()
	text := a.RenderMap(a.conversationTokens(), nil)
	a.mu.Lock()
	a.codeMap = text
	a.mu.Unlock()
}

// codeMapText is the map the conversation carries, for the agent's
// Options.CodeMap. It is "" until the first build has been drawn.
func (a *App) codeMapText() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.codeMap
}

// modelCachesPrompt reports whether the lead's model caches the prompt prefix,
// which decides whether the code map is frozen (refreshCodeMap).
func (a *App) modelCachesPrompt() bool {
	if a.Agent == nil {
		return false
	}
	return agent.PromptCaches(a.Agent.CurrentModel().Ref)
}

// conversationTokens is the size of the conversation the map is sized for
// (spec 04 §4.7). It is 0 before the first request, which takes the default.
func (a *App) conversationTokens() int {
	if a.Agent == nil {
		return 0
	}
	return a.Agent.ContextParts().Conversation
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

// frozenParts are the system prompt lines that read live project state.
type frozenParts struct{ commands, memory, git string }

// frozenPromptParts returns the parts for prompt kind sub, reading them with
// read the first time and after each compaction.
func (a *App) frozenPromptParts(sub string, read func() frozenParts) frozenParts {
	a.mapMu.Lock()
	p, ok := a.frozen[sub]
	a.mapMu.Unlock()
	if ok {
		return p
	}
	p = read()
	a.mapMu.Lock()
	if a.frozen == nil {
		a.frozen = map[string]frozenParts{}
	}
	a.frozen[sub] = p
	a.mapMu.Unlock()
	return p
}
