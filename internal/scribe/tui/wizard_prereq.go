package tui

// Prerequisites the setup wizard checks before it lets the user go on.
// Git is required: checkpoints, undo and the git tools cannot work without
// it. The other tools make Scribe faster but are optional.

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// prereq is one tool the wizard looks for on PATH.
type prereq struct {
	Name     string
	Bin      string
	Required bool
	Why      string
	// Install is the command to run, by GOOS. A missing key means the
	// generic hint.
	Install map[string]string
	// Lang marks a language server. Servers are shown on one line, not one
	// row each, so the welcome step stays within a 24-row terminal.
	Lang bool
}

var prereqTable = []prereq{
	{Name: "Git", Bin: "git", Required: true, Why: "checkpoints, undo and the git tools need it",
		Install: map[string]string{
			"linux":   "sudo apt install git",
			"darwin":  "xcode-select --install   (or: brew install git)",
			"windows": "winget install --id Git.Git",
		}},
	{Name: "ripgrep", Bin: "rg", Why: "faster grep and code search",
		Install: map[string]string{
			"linux":   "sudo apt install ripgrep",
			"darwin":  "brew install ripgrep",
			"windows": "winget install BurntSushi.ripgrep.MSVC",
		}},
	{Name: "fd", Bin: "fd", Why: "faster file search",
		Install: map[string]string{
			"linux":   "sudo apt install fd-find",
			"darwin":  "brew install fd",
			"windows": "winget install sharkdp.fd",
		}},
	{Name: "lazygit", Bin: "lazygit", Why: "the git menu's full-screen view (g)",
		Install: map[string]string{
			"linux":   "download a release from github.com/jesseduffield/lazygit",
			"darwin":  "brew install lazygit",
			"windows": "winget install JesseDuffield.lazygit",
		}},
	// Language servers give code intelligence. Each one is optional; the
	// list mirrors the first candidate of each language in lsp/servers.go.
	{Name: "gopls", Bin: "gopls", Lang: true, Why: "Go",
		Install: map[string]string{"linux": "go install golang.org/x/tools/gopls@latest"}},
	{Name: "typescript-language-server", Bin: "typescript-language-server", Lang: true, Why: "TypeScript and JavaScript",
		Install: map[string]string{"linux": "npm install -g typescript typescript-language-server"}},
	{Name: "pyright-langserver", Bin: "pyright-langserver", Lang: true, Why: "Python",
		Install: map[string]string{"linux": "npm install -g pyright"}},
	{Name: "rust-analyzer", Bin: "rust-analyzer", Lang: true, Why: "Rust",
		Install: map[string]string{"linux": "rustup component add rust-analyzer"}},
}

// prereqStatus is a prereq with the result of looking for it.
type prereqStatus struct {
	prereq
	path string // where it was found; empty when missing
}

// lookPath is exec.LookPath, a variable so tests can fake a machine without
// Git.
var lookPath = exec.LookPath

// checkPrereqs looks for every tool in prereqTable, in table order.
func checkPrereqs() []prereqStatus {
	out := make([]prereqStatus, 0, len(prereqTable))
	for _, p := range prereqTable {
		st := prereqStatus{prereq: p}
		if path, err := lookPath(p.Bin); err == nil {
			st.path = path
		}
		out = append(out, st)
	}
	return out
}

// missingRequired returns the names of required tools that were not found.
func missingRequired(ps []prereqStatus) []prereqStatus {
	var out []prereqStatus
	for _, p := range ps {
		if p.Required && p.path == "" {
			out = append(out, p)
		}
	}
	return out
}

// installHint is the command that installs p on this platform.
func installHint(p prereq, goos string) string {
	if c, ok := p.Install[goos]; ok {
		return c
	}
	return "install it with your package manager"
}

// prereqLines renders the prerequisite table for the welcome step.
func prereqLines(th theme.Theme, ps []prereqStatus, goos string, width int) []string {
	out := []string{fgb(th.TextPrimary).Render("Checking this machine"), ""}
	var langs []string
	for _, p := range ps {
		if p.Lang {
			if p.path != "" {
				langs = append(langs, p.Name)
			}
			continue
		}
		switch {
		case p.path != "":
			out = append(out, fg(th.Success).Render("  ✓ "+p.Name)+fg(th.TextMuted).Render("  found"))
		case p.Required:
			out = append(out, fg(th.Error).Render("  ✗ "+p.Name)+fg(th.TextMuted).Render("  missing: "+p.Why))
			out = append(out, fg(th.TextMuted).Render(render.Wrap("      install: "+installHint(p.prereq, goos), width)))
		default:
			out = append(out, fg(th.TextMuted).Render("  – "+p.Name+"  not found (optional): "+p.Why))
		}
	}
	// One line for the language servers: those found, else a hint.
	switch {
	case len(langs) > 0:
		out = append(out, fg(th.Success).Render(render.Wrap("  ✓ Language servers  "+strings.Join(langs, ", "), width)))
	default:
		out = append(out, fg(th.TextMuted).Render(render.Wrap("  – Language servers  none found (optional): code intelligence for Go, TypeScript, Python or Rust", width)))
	}
	return out
}

// missingHints is "name: install command" for every tool the wizard did not
// find, for the welcome step's "i" key. It only shows the commands; nothing
// is run.
func missingHints(ps []prereqStatus, goos string) []string {
	var out []string
	for _, p := range ps {
		if p.path == "" {
			out = append(out, p.Name+": "+installHint(p.prereq, goos))
		}
	}
	return out
}

// requiredMessage is what the wizard shows when a required tool is missing.
func requiredMessage(missing []prereqStatus, goos string) string {
	var names []string
	for _, p := range missing {
		names = append(names, p.Name)
	}
	return fmt.Sprintf("%s is required before Scribe can run. Install it (%s), then press r to check again.",
		strings.Join(names, ", "), installHint(missing[0].prereq, goos))
}

// goos is runtime.GOOS; a variable so tests can pick a platform.
var goos = runtime.GOOS
