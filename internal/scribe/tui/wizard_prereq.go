package tui

// Prerequisites the setup wizard checks before it lets the user go on.
// Git is required: checkpoints, undo and the git tools cannot work without
// it. The other tools make Scribe faster but are optional.

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
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
		out = append(out, fg(th.Success).Render(hangWrap("  ✓ Language servers  "+strings.Join(langs, ", "), width, 4)))
	default:
		out = append(out, fg(th.TextMuted).Render(hangWrap("  – Language servers  none found (optional): code intelligence for Go, TypeScript, Python or Rust", width, 4)))
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

// setupTab is a tab of the setup guide on the welcome step. Tools lists the
// prerequisites; Fonts holds the Nerd Font choice, which is saved as
// nerd_font.
type setupTab int

const (
	setupTools setupTab = iota
	setupFonts
)

// setupTabForKey maps the 1 and 2 keys to their tabs. Any other key is not a
// tab key.
func setupTabForKey(k string) (setupTab, bool) {
	switch k {
	case "1":
		return setupTools, true
	case "2":
		return setupFonts, true
	}
	return setupTools, false
}

// setupFooter is the welcome footer for a tab. Only the keys that do
// something on that tab are listed.
func setupFooter(tab setupTab, missing bool) string {
	if tab == setupFonts {
		return "Space toggle · 1/2 tabs · → next · Esc close"
	}
	if missing {
		return "r check again · i install hints · a copy installs · 1/2 tabs · Esc close"
	}
	return "→ next · i install hints · 1/2 tabs · Esc close"
}

// setupTabLines renders the body of the active tab.
func setupTabLines(th theme.Theme, tab setupTab, ps []prereqStatus, nerd bool, goos string, width int) []string {
	if tab == setupFonts {
		return fontLines(th, nerd, nerdDetected(), width)
	}
	return prereqLines(th, ps, goos, width)
}

// nerdDetected runs icons.DetectNerd once. It walks the font directories, and
// the welcome step is redrawn on every key, so the answer is kept.
var nerdDetected = sync.OnceValue(icons.DetectNerd)

// fontLines renders the Fonts tab. The detection is a guess from the font
// directories and the terminal name, so the user's choice always wins.
func fontLines(th theme.Theme, nerd, detected bool, width int) []string {
	state, det := "off", "not found in a font directory or a known terminal"
	if nerd {
		state = "on"
	}
	if detected {
		det = "found"
	}
	return []string{
		fgb(th.TextPrimary).Render("Nerd Font icons"),
		"",
		fg(th.TextSecondary).Render("  icons      ") + fgb(th.TextPrimary).Render(state),
		fg(th.TextMuted).Render(render.Wrap("  detection  "+det, width)),
		"",
		fg(th.TextMuted).Render(render.Wrap("Space toggles the choice. Glyphs need a Nerd Font set in the terminal.", width)),
	}
}

// copyInstalls puts the install command for each missing tool on the
// clipboard, one per line, and says so in the note. It never runs them: an
// install needs sudo or the network, and a key press in a setup screen should
// not start either. The user reads the lines and runs what they choose.
func (w *wizard) copyInstalls() {
	var lines []string
	for _, p := range w.prereqs {
		if p.path == "" {
			lines = append(lines, installHint(p.prereq, goos))
		}
	}
	if len(lines) == 0 {
		w.note = "Everything on this list is installed"
		return
	}
	copyToTerminal(strings.Join(lines, "\n") + "\n")
	w.note = fmt.Sprintf("Copied %d install command(s) to the clipboard. Nothing was run.", len(lines))
}

// hangWrap wraps s to width and indents the continuation lines by indent
// spaces, so a wrapped row stays under its own bullet.
func hangWrap(s string, width, indent int) string {
	lines := strings.Split(render.Wrap(s, width), "\n")
	if len(lines) <= 1 {
		return s
	}
	pad := strings.Repeat(" ", indent)
	rest := render.Wrap(strings.Join(lines[1:], " "), max(10, width-indent))
	out := []string{lines[0]}
	for _, l := range strings.Split(rest, "\n") {
		out = append(out, pad+strings.TrimLeft(l, " "))
	}
	return strings.Join(out, "\n")
}
