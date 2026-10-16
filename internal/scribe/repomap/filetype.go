package repomap

import (
	"path"
	"strings"
)

// File categories for the find_symbol type filter (spec 04 section 2.6). A
// category is decided from the path and file name alone, so it costs nothing
// at query time and needs no index.
const (
	catTest      = "test"
	catComponent = "component"
	catConfig    = "config"
	catTypes     = "types"
	catStyle     = "style"
)

// FileCategory returns the category of a repo-relative path: test, component,
// config, types, style, or "" when none applies.
func FileCategory(p string) string {
	base := strings.ToLower(path.Base(p))
	dir := strings.ToLower(path.Dir(p))
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch {
	case isTestPath(p):
		return catTest
	case ext == ".css" || ext == ".scss" || ext == ".sass" || ext == ".less" || ext == ".styl" ||
		strings.Contains(base, ".module.") || strings.Contains(dir, "/styles") || strings.Contains(dir, "/theme"):
		return catStyle
	case stem == "types" || stem == "type" || strings.HasSuffix(stem, ".types") || strings.HasSuffix(stem, ".d") && ext == ".ts" ||
		strings.HasSuffix(stem, "_types") || strings.HasSuffix(stem, "types") && ext == ".go" || strings.HasSuffix(dir, "/types"):
		return catTypes
	case stem == "config" || strings.HasSuffix(stem, ".config") || strings.HasSuffix(stem, "_config") ||
		strings.HasPrefix(base, "config.") || ext == ".yaml" || ext == ".yml" || ext == ".toml" || ext == ".ini" ||
		ext == ".env" || base == "dockerfile" || base == "makefile" || strings.HasPrefix(base, ".") && ext == "":
		return catConfig
	case ext == ".tsx" || ext == ".jsx" || ext == ".vue" || ext == ".svelte" || strings.Contains(dir, "/components"):
		return catComponent
	}
	return ""
}

// isTestPath reports whether p is a test file by the naming rules used for
// test linking (testlinks.go) plus common test directories.
func isTestPath(p string) bool {
	base := path.Base(p)
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if _, ok := testSubjectBase(base, false); ok {
		return true
	}
	if strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".spec") || strings.HasPrefix(name, "test_") {
		return true
	}
	for _, d := range strings.Split(path.Dir(p), "/") {
		if d == "test" || d == "tests" || d == "__tests__" || d == "spec" {
			return true
		}
	}
	return false
}

// kindAliases maps the kind names a model is likely to type to the kinds the
// extractor emits. Keys are lower-case and already stripped of '-' and ' '.
var kindAliases = map[string]string{
	"function": "function", "functions": "function", "func": "function", "fn": "function",
	"method": "method", "methods": "method",
	"class": "class", "classes": "class",
	"interface": "interface", "interfaces": "interface", "iface": "interface", "protocol": "interface",
	"type": "type", "types": "type", "alias": "type", "typealias": "type", "typedef": "type",
	"enum": "enum", "enums": "enum", "enumeration": "enum",
	"trait": "trait", "traits": "trait",
	"struct": "struct", "structs": "struct", "structure": "struct",
	"var": "var", "variable": "var", "variables": "var",
	"const": "const", "constant": "const", "constants": "const",
	"module": "module", "modules": "module", "namespace": "module",
}

// NormalizeKind maps a requested kind to the extractor's vocabulary. It takes
// near misses ("Functions", "fn", "tuples") and returns "" with ok false for
// anything it does not know.
func NormalizeKind(k string) (string, bool) {
	k = strings.ToLower(strings.TrimSpace(k))
	k = strings.NewReplacer("-", "", " ", "", "_", "").Replace(k)
	if k == "" {
		return "", true
	}
	n, ok := kindAliases[k]
	return n, ok
}

// kindMatches reports whether a symbol kind passes a normalized kind filter.
// "function" also takes methods, because a model asking for functions usually
// wants methods too.
func kindMatches(symKind, want string) bool {
	if want == "" {
		return true
	}
	if want == "function" {
		return isFuncKind(symKind)
	}
	return symKind == want
}
