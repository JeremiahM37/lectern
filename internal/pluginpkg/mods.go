package pluginpkg

import (
	"errors"
	"regexp"
	"strings"

	"github.com/dop251/goja/parser"
)

var (
	modImportRe   = regexp.MustCompile(`(?m)^\s*import\s*[\w{*'"]`)
	modExportFnRe = regexp.MustCompile(`(?m)^(\s*)export\s+(default\s+)?(async\s+)?function\s*(register)?\s*\(`)
	modDefaultRe  = regexp.MustCompile(`(?m)^(\s*)export\s+default\s+`)
	modExportRe   = regexp.MustCompile(`(?m)^(\s*)export\s+(const|let|var|function|async|class)\b`)
	modRegisterRe = regexp.MustCompile(`(?m)(^|[^\w$.])(function\s+register\s*\(|(var|let|const)\s+register\s*=)`)
)

// ModScript turns a mod's module source into the classic script both
// runtimes evaluate: one that declares a global register function. A mod is
// one file, so a static import is refused rather than left to fail at run
// time. It only parses the code; nothing runs here, which is what lets a
// preview validate a mod before anyone has consented to it.
func ModScript(src string) (string, error) {
	if modImportRe.MatchString(src) {
		return "", errors.New("a mod is one file and cannot import; bundle it first (esbuild --bundle --format=esm)")
	}
	out := modExportFnRe.ReplaceAllStringFunc(src, func(m string) string {
		sub := modExportFnRe.FindStringSubmatch(m)
		indent, def, async, named := sub[1], sub[2], sub[3], sub[4]
		if def == "" && named == "" {
			return m // export function other(…): stripped below
		}
		return indent + async + "function register("
	})
	out = modDefaultRe.ReplaceAllString(out, "${1}var register = ")
	out = modExportRe.ReplaceAllString(out, "${1}${2}")
	if !modRegisterRe.MatchString(out) {
		return "", errors.New("a mod must define register: export function register(on, options) { … }")
	}
	if _, err := parser.ParseFile(nil, "mod.js", out, 0); err != nil {
		return "", errors.New("does not parse: " + firstLine(err.Error()))
	}
	return out, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

func surfaceName(s string) string {
	if s == "cli" {
		return "terminal console"
	}
	return "browser"
}
