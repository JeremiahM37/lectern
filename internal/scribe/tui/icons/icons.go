// Package icons maps Scribe's icon names to glyphs.
//
// Two sets exist: Nerd Font glyphs (Material Design and Font Awesome private-use
// codepoints) and ASCII or Unicode-symbol fallbacks. ASCII is the default.
// Nerd mode is switched on by SetNerd, or detected with DetectNerd.
package icons

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

type entry struct {
	nerd  rune
	ascii string
}

var nerdOn atomic.Bool

// Status and tree glyphs that do not change with the icon set.
const (
	Check     = "✓"
	Cross     = "✗"
	Bang      = "!"
	Ring      = "○"
	Dot       = "●"
	Target    = "◎"
	Arrow     = "→"
	Middle    = "·"
	TreeFirst = "┌ "
	TreeMid   = "├ "
	TreeLast  = "└ "
	TreeBar   = "│ "
)

// SetNerd turns Nerd Font glyphs on or off.
func SetNerd(on bool) { nerdOn.Store(on) }

// NerdEnabled reports whether Nerd Font glyphs are in use.
func NerdEnabled() bool { return nerdOn.Load() }

// Icon returns the glyph for name in the active set. Missing names return the
// name itself. A name with no Nerd glyph falls back to its ASCII value.
func Icon(name string) string {
	e, ok := table[name]
	if !ok {
		return name
	}
	if nerdOn.Load() && e.nerd != 0 {
		return string(e.nerd)
	}
	if e.ascii != "" {
		return e.ascii
	}
	return name
}

// Nerd returns the Nerd Font glyph for name regardless of the active set, and
// false when there is none.
func Nerd(name string) (string, bool) {
	e, ok := table[name]
	if !ok || e.nerd == 0 {
		return "", false
	}
	return string(e.nerd), true
}

// ASCII returns the fallback glyph for name regardless of the active set.
func ASCII(name string) (string, bool) {
	e, ok := table[name]
	if !ok || e.ascii == "" {
		return "", false
	}
	return e.ascii, true
}

// DetectNerd guesses whether the terminal can show Nerd Font glyphs. It is true
// when a Nerd Font file is installed in a font directory, or when the terminal
// is one that usually ships with a Nerd-capable font setup (kitty, WezTerm,
// Alacritty, Hyper, iTerm, Ghostty, JetBrains). Config should override this.
func DetectNerd() bool {
	if hasNerdFontFile() {
		return true
	}
	tp := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	for _, t := range []string{"kitty", "wezterm", "alacritty", "hyper", "iterm", "ghostty"} {
		if strings.Contains(tp, t) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(os.Getenv("TERMINAL_EMULATOR")), "jetbrains")
}

const nerdFontFile = "SymbolsNerdFont-Regular.ttf"

func hasNerdFontFile() bool {
	var dirs []string
	dirs = append(dirs, filepath.Join(config.Home(), "fonts"))
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "fonts"), filepath.Join(home, "Library", "Fonts"))
	}
	dirs = append(dirs, "/usr/share/fonts", "/usr/local/share/fonts")
	for _, d := range dirs {
		if findShallow(d, nerdFontFile, 3) {
			return true
		}
	}
	return false
}

// findShallow looks for name under dir, at most depth directories down.
func findShallow(dir, name string, depth int) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return fs.SkipDir
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			if strings.Count(rel, string(filepath.Separator)) >= depth && rel != "." {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == name {
			found = true
		}
		return nil
	})
	return found
}

// providerEntry is a provider logo: Nerd glyph (optional) and ASCII fallback.
type providerEntry struct {
	nerd  string
	ascii string
}

// providers keys are provider ids. Nerd glyphs are the ones the spec's
// provider table names; anything else uses the ASCII mark.
var providers = map[string]providerEntry{
	"anthropic":      {"\U000F1719", "A"},
	"openai":         {"\U000F09D1", "O"},
	"google":         {"\U000F02AD", "G"},
	"deepseek":       {"\U000F09D1", "D"},
	"mistral":        {"\U000F0AC8", "M"},
	"minimax":        {"\U000F0AC8", "M"},
	"vercel_gateway": {"\U000F048D", "☁"},
	"llmgateway":     {"\U000F048D", "☁"},
	"proxy":          {"\U000F0306", "⛨"},
	"openrouter":     {"", "⊕"},
	"copilot":        {"", "CP"},
	"bedrock":        {"", "B"},
	"fireworks":      {"", "F"},
	"github-models":  {"", "GH"},
	"groq":           {"", "Q"},
	"lmstudio":       {"", "L"},
	"nim":            {"", "N"},
	"ollama":         {"", "O"},
	"opencode-go":    {"", "GO"},
	"opencode-zen":   {"", "Z"},
	"xai":            {"", "X"},
	"custom":         {"", "◇"},
}

// ProviderIcon returns the logo for a provider id: Nerd glyph in Nerd mode when
// one is known, else the ASCII mark, else "●".
func ProviderIcon(id string) string {
	p, ok := providers[id]
	if !ok {
		return Dot
	}
	if nerdOn.Load() && p.nerd != "" {
		return p.nerd
	}
	if p.ascii != "" {
		return p.ascii
	}
	return Dot
}

// toolIcons maps a tool name to the icon name the tool row uses.
var toolIcons = map[string]string{
	"read": "file", "edit_file": "pencil", "multi_edit": "pencil", "undo_edit": "pencil",
	"list_dir": "changes", "shell": "terminal", "grep": "search", "glob": "changes",
	"dispatch": "explore", "web_search": "globe", "fetch_page": "file", "memory": "bookmark",
	"skills": "skills", "editor": "pencil", "git": "git", "navigate": "arrow_right",
	"analyze": "search", "rename_symbol": "rename", "move_symbol": "arrow_right",
	"rename_file": "arrow_right", "refactor": "wrench", "project": "terminal",
	"discover_pattern": "search", "editor_panel": "pencil", "plan": "plan", "write_plan": "plan",
	"update_plan_step": "check", "ask_user": "question", "task_list": "plan",
	"code_execution": "smithy", "soul_vision": "image", "soul_grep": "search", "soul_find": "search",
	"soul_analyze": "repomap", "soul_impact": "repomap", "ast_edit": "morph", "_nudge": "lightning",
}

// ToolIcon returns the glyph for a tool row. Unknown tools get the wrench.
func ToolIcon(tool string) string {
	if name, ok := toolIcons[tool]; ok {
		return Icon(name)
	}
	return Icon("wrench")
}
