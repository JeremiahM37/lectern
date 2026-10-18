package lsp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type candidate struct {
	name string
	args []string
}

// serverSpec describes how to find and start the language server for one
// language. Candidates are tried in order.
type serverSpec struct {
	Language   string
	Candidates []candidate
	Install    string // how to install the first candidate
	// Role is empty for the primary server, which answers navigation, rename
	// and formatting. roleLint marks a secondary server: it adds diagnostics
	// and code actions beside the primary and never answers navigation.
	Role string
}

// roleLint marks a secondary (lint) server. Each language has at most one.
const roleLint = "lint"

// key names the running server for this spec at root, in the form
// Manager.sessions and the health records use. A primary keeps the plain
// language and root key so existing callers are unchanged.
func (s *serverSpec) key(root string) string {
	k := s.Language + "\x00" + root
	if s.Role != "" {
		k += "\x00" + s.Role
	}
	return k
}

// lintSpecs is the secondary-server catalogue. Only the first candidate that is
// installed starts, so one language never runs two linters over the same
// files and doubles every diagnostic. The biome and ESLint entries are the
// ones the spec names for TypeScript; ruff is the Python linter.
var lintSpecs = map[string]*serverSpec{
	"typescript": {
		Language: "typescript",
		Role:     roleLint,
		Candidates: []candidate{
			{"biome", []string{"lsp-proxy"}},
			{"vscode-eslint-language-server", []string{"--stdio"}},
		},
		Install: "npm install -g @biomejs/biome   (or: npm install -g vscode-langservers-extracted for ESLint)",
	},
	"python": {
		Language:   "python",
		Role:       roleLint,
		Candidates: []candidate{{"ruff", []string{"server"}}},
		Install:    "pip install ruff",
	},
}

// serverSpecs is the catalog, keyed by language.
var serverSpecs = map[string]*serverSpec{
	"go": {
		Language:   "go",
		Candidates: []candidate{{"gopls", []string{"serve"}}},
		Install:    "go install golang.org/x/tools/gopls@latest",
	},
	"typescript": {
		Language: "typescript",
		Candidates: []candidate{
			{"typescript-language-server", []string{"--stdio"}},
			{"vtsls", []string{"--stdio"}},
		},
		Install: "npm install -g typescript typescript-language-server",
	},
	"python": {
		Language: "python",
		Candidates: []candidate{
			{"pyright-langserver", []string{"--stdio"}},
			{"basedpyright-langserver", []string{"--stdio"}},
			{"pylsp", nil},
		},
		Install: "npm install -g pyright   (or: pip install python-lsp-server)",
	},
	"rust": {
		Language:   "rust",
		Candidates: []candidate{{"rust-analyzer", nil}},
		Install:    "rustup component add rust-analyzer",
	},
	"cpp": {
		Language:   "cpp",
		Candidates: []candidate{{"clangd", nil}},
		Install:    "install clangd (for example: apt install clangd)",
	},
	"java": {
		Language:   "java",
		Candidates: []candidate{{"jdtls", nil}},
		Install:    "install Eclipse JDT Language Server and put its jdtls launcher on PATH",
	},
	"ruby": {
		Language:   "ruby",
		Candidates: []candidate{{"ruby-lsp", nil}},
		Install:    "gem install ruby-lsp",
	},
	"lua": {
		Language:   "lua",
		Candidates: []candidate{{"lua-language-server", nil}},
		Install:    "install lua-language-server from https://github.com/LuaLS/lua-language-server/releases",
	},
	"zig": {
		Language:   "zig",
		Candidates: []candidate{{"zls", nil}},
		Install:    "install zls from https://github.com/zigtools/zls/releases",
	},
	"csharp": {
		Language:   "csharp",
		Candidates: []candidate{{"csharp-ls", nil}},
		Install:    "dotnet tool install -g csharp-ls",
	},
	"php": {
		Language:   "php",
		Candidates: []candidate{{"intelephense", []string{"--stdio"}}},
		Install:    "npm install -g intelephense",
	},
	"bash": {
		Language:   "bash",
		Candidates: []candidate{{"bash-language-server", []string{"start"}}},
		Install:    "npm install -g bash-language-server",
	},
	"yaml": {
		Language:   "yaml",
		Candidates: []candidate{{"yaml-language-server", []string{"--stdio"}}},
		Install:    "npm install -g yaml-language-server",
	},
	"elixir": {
		Language:   "elixir",
		Candidates: []candidate{{"elixir-ls", nil}, {"expert", []string{"--stdio"}}},
		Install:    "install ElixirLS from https://github.com/elixir-lsp/elixir-ls",
	},
}

type langInfo struct {
	key string // catalog key
	id  string // LSP languageId
}

// extLanguages maps file extensions to the server and languageId.
var extLanguages = map[string]langInfo{
	".go":   {"go", "go"},
	".ts":   {"typescript", "typescript"},
	".mts":  {"typescript", "typescript"},
	".cts":  {"typescript", "typescript"},
	".tsx":  {"typescript", "typescriptreact"},
	".js":   {"typescript", "javascript"},
	".mjs":  {"typescript", "javascript"},
	".cjs":  {"typescript", "javascript"},
	".jsx":  {"typescript", "javascriptreact"},
	".py":   {"python", "python"},
	".rs":   {"rust", "rust"},
	".c":    {"cpp", "c"},
	".h":    {"cpp", "cpp"},
	".cc":   {"cpp", "cpp"},
	".cpp":  {"cpp", "cpp"},
	".cxx":  {"cpp", "cpp"},
	".hpp":  {"cpp", "cpp"},
	".hh":   {"cpp", "cpp"},
	".java": {"java", "java"},
	".rb":   {"ruby", "ruby"},
	".lua":  {"lua", "lua"},
	".zig":  {"zig", "zig"},
	".cs":   {"csharp", "csharp"},
	".php":  {"php", "php"},
	".sh":   {"bash", "shellscript"},
	".bash": {"bash", "shellscript"},
	".yaml": {"yaml", "yaml"},
	".yml":  {"yaml", "yaml"},
	".ex":   {"elixir", "elixir"},
	".exs":  {"elixir", "elixir"},
}

// languageForPath returns the catalog entry for a file, if one exists.
func languageForPath(path string) (langInfo, bool) {
	info, ok := extLanguages[strings.ToLower(filepath.Ext(path))]
	return info, ok
}

// SupportedExtensions reports whether a language server is catalogued for path.
func SupportedExtensions(path string) bool {
	_, ok := languageForPath(path)
	return ok
}

// NotInstalledError means no candidate server for a language is available.
type NotInstalledError struct {
	Language   string
	Candidates []string
	Install    string
}

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("no language server for %s is installed (looked for %s). Install it with: %s",
		e.Language, strings.Join(e.Candidates, ", "), e.Install)
}

func notInstalled(spec *serverSpec) *NotInstalledError {
	names := make([]string, 0, len(spec.Candidates))
	for _, c := range spec.Candidates {
		names = append(names, c.name)
	}
	return &NotInstalledError{Language: spec.Language, Candidates: names, Install: spec.Install}
}

// findServer returns the first candidate that can be found, with its args.
func findServer(spec *serverSpec, root string) (string, []string, bool) {
	for _, c := range spec.Candidates {
		if bin, ok := lookBinary(c.name, root); ok {
			return bin, c.args, true
		}
	}
	return "", nil, false
}

// lookBinary searches PATH, ~/go/bin, ~/.local/bin, the Mason bin directory
// (where Neovim's installer puts language servers) and node_modules/.bin in
// root and its ancestors.
func lookBinary(name, root string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".local", "share", "nvim", "mason", "bin"))
	}
	for d := root; ; d = filepath.Dir(d) {
		dirs = append(dirs, filepath.Join(d, "node_modules", ".bin"))
		if filepath.Dir(d) == d {
			break
		}
	}
	for _, d := range dirs {
		for _, cand := range []string{name, name + ".exe"} {
			fi, err := os.Stat(filepath.Join(d, cand))
			if err != nil || fi.IsDir() {
				continue
			}
			if runtime.GOOS == "windows" || fi.Mode()&0o111 != 0 {
				return filepath.Join(d, cand), true
			}
		}
	}
	return "", false
}
