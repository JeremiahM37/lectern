package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/hooks"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// The checks /diagnose adds beyond providers, servers, git and disk: that the
// Scribe home is writable, that the hook settings parse, and that the project's
// languages have a language server row. Each returns promptly.

// homeCheck says whether the Scribe home exists and accepts a file. It writes a
// throwaway file and removes it at once.
func homeCheck() tui.DiagnoseCheck {
	home := config.Home()
	c := tui.DiagnoseCheck{Area: "Scribe home", Name: "writable", State: "ok", Detail: home}
	fi, err := os.Stat(home)
	if err != nil {
		c.State, c.Detail = "warn", home+" does not exist yet"
		c.Fix = "create the folder, or point SCRIBE_HOME at one that exists"
		return c
	}
	if !fi.IsDir() {
		c.State, c.Detail = "fail", home+" is not a folder"
		c.Fix = "move the file out of the way, or point SCRIBE_HOME at a folder"
		return c
	}
	f, err := os.CreateTemp(home, ".diagnose-*")
	if err != nil {
		c.State, c.Detail = "fail", oneLineStr(err.Error())
		c.Fix = "make the folder writable, or point SCRIBE_HOME at one that is"
		return c
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return c
}

// hooksChecks reads every settings file that can hold hooks and reports the
// ones that do not parse, then what the hook runner made of the rest.
func hooksChecks(root string) []tui.DiagnoseCheck {
	files := []string{config.GlobalPath()}
	if root != "" {
		files = append(files, config.ProjectPath(root),
			filepath.Join(root, ".claude", "settings.json"),
			filepath.Join(root, ".claude", "settings.local.json"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".claude", "settings.json"))
	}
	var out []tui.DiagnoseCheck
	bad := false
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil || len(b) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			bad = true
			out = append(out, tui.DiagnoseCheck{Area: "Hooks", Name: filepath.Base(p), State: "fail",
				Detail: "does not parse: " + oneLineStr(err.Error()),
				Fix:    "fix the JSON syntax in " + p})
		}
	}
	if bad {
		return out
	}
	h, err := hooks.NewFromDisk(root, "")
	if err != nil {
		return append(out, tui.DiagnoseCheck{Area: "Hooks", Name: "settings", State: "fail",
			Detail: oneLineStr(err.Error()), Fix: "fix the file named in the detail"})
	}
	c := tui.DiagnoseCheck{Area: "Hooks", Name: "settings", State: "ok",
		Detail: fmt.Sprintf("%d hook(s) configured", h.Count())}
	if h.Count() == 0 {
		c.State, c.Detail = "info", "none configured"
	}
	if w := h.Warnings(); len(w) > 0 {
		c.State, c.Detail = "warn", fmt.Sprintf("%d warning(s): %s", len(w), oneLineStr(w[0]))
		c.Fix = "see the warning, then fix the hook entry it names"
	}
	return append(out, c)
}

// lspGapChecks adds a row for each language the project uses that has no
// language server row yet: disabled in config, or not started yet.
func lspGapChecks(a *App, have []tui.LSPSession) []tui.DiagnoseCheck {
	seen := map[string]bool{}
	for _, s := range have {
		seen[s.Language] = true
	}
	var out []tui.DiagnoseCheck
	for _, lang := range lsp.DetectLanguages(a.Workdir) {
		if seen[lang] {
			continue
		}
		c := tui.DiagnoseCheck{Area: "Language servers", Name: lang, State: "info", Detail: "not started yet"}
		if slices.Contains(a.Cfg.DisabledLSPServers, lang) {
			c.Detail = "disabled in config"
		}
		out = append(out, c)
	}
	return out
}
