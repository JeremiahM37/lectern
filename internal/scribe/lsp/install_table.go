package lsp

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Install methods. A runnable method is one the /lsp install command can run
// itself after the user confirms. Manual servers are shown as text only.
const (
	InstallMethodGo     = "go"
	InstallMethodNpm    = "npm"
	InstallMethodManual = ""
)

// runnableInstalls maps a server binary to the one install command this
// package will run for it. Everything else in serverSpecs is manual.
var runnableInstalls = map[string]struct {
	method string
	argv   []string
}{
	"gopls":                      {InstallMethodGo, []string{"go", "install", "golang.org/x/tools/gopls@latest"}},
	"typescript-language-server": {InstallMethodNpm, []string{"npm", "install", "-g", "typescript", "typescript-language-server"}},
	"pyright-langserver":         {InstallMethodNpm, []string{"npm", "install", "-g", "pyright"}},
}

// ServerInstall is one language server the install command knows about. It is
// the first candidate of a language's catalogue entry, which is the one
// serverSpecs.Install describes.
type ServerInstall struct {
	Language string   // catalogue key, for example "go" or "typescript"
	Server   string   // binary the server is found by, for example "gopls"
	Method   string   // InstallMethodGo, InstallMethodNpm or InstallMethodManual
	Argv     []string // the exact command for a runnable method; nil when manual
	Manual   string   // how to install by hand; set for every entry
}

// Runnable reports whether this command can run the install itself.
func (s ServerInstall) Runnable() bool { return len(s.Argv) > 0 }

// Command is the install command as the user would type it.
func (s ServerInstall) Command() string {
	if s.Runnable() {
		return strings.Join(s.Argv, " ")
	}
	return s.Manual
}

// Tool is the program a runnable install starts ("go" or "npm"), or "" when
// the install is manual.
func (s ServerInstall) Tool() string {
	if s.Runnable() {
		return s.Argv[0]
	}
	return ""
}

// InstallCatalog lists the install entries for the given catalogue languages,
// in the order given. Languages with no catalogued server are skipped.
func InstallCatalog(languages []string) []ServerInstall {
	var out []ServerInstall
	for _, lang := range languages {
		if e, ok := installEntry(lang); ok {
			out = append(out, e)
		}
	}
	return out
}

// ServerInstallFor finds the entry for a server binary name ("gopls") or a
// catalogue language key ("go"). Names are matched across every language.
func ServerInstallFor(name string) (ServerInstall, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ServerInstall{}, false
	}
	langs := make([]string, 0, len(serverSpecs))
	for lang := range serverSpecs {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		e, _ := installEntry(lang)
		if e.Server == name {
			return e, true
		}
	}
	return installEntry(name)
}

// FindOnPath reports the first candidate of a language's server that exec
// can find on PATH. It looks only at PATH, not at the manager's extra
// directories, because "installed" in the install listing means on PATH.
func FindOnPath(language string) (name, path string, ok bool) {
	spec, known := serverSpecs[language]
	if !known {
		return "", "", false
	}
	for _, c := range spec.Candidates {
		if p, ok := Find(c.name); ok {
			return c.name, p, true
		}
	}
	return "", "", false
}

// installEntry builds the entry for one catalogue language.
func installEntry(language string) (ServerInstall, bool) {
	spec, ok := serverSpecs[language]
	if !ok || len(spec.Candidates) == 0 {
		return ServerInstall{}, false
	}
	e := ServerInstall{
		Language: language,
		Server:   spec.Candidates[0].name,
		Method:   InstallMethodManual,
		Manual:   spec.Install,
	}
	if r, ok := runnableInstalls[e.Server]; ok {
		e.Method = r.method
		e.Argv = append([]string(nil), r.argv...)
	}
	return e, true
}

// Find returns the path of an executable: first on PATH, then in the Go bin
// directories where `go install` puts tools (GOBIN, GOPATH/bin, ~/go/bin), so a
// server installed by `go install` counts as installed even when that folder
// is not on PATH.
func Find(name string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	var dirs []string
	if b := os.Getenv("GOBIN"); b != "" {
		dirs = append(dirs, b)
	}
	if g := os.Getenv("GOPATH"); g != "" {
		dirs = append(dirs, filepath.Join(g, "bin"))
	}
	if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, "go", "bin"))
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}
