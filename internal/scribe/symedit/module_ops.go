package symedit

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// REAUDIT5 03#30 (spec 03 sections 3.4 and 5.4). These file-level actions
// edit the module graph of a TypeScript or JavaScript file: the import
// statements and the module path they name. They take no symbol. The module
// path goes in symbol, and any names go in new_code, comma separated.
//
// Only single-line statements are edited. A multi-line import is left alone,
// and an action that finds nothing to edit says so rather than guessing.
const (
	actAddNamedImport     = "add_named_import"
	actRemoveNamedImport  = "remove_named_import"
	actRemoveImport       = "remove_import"
	actSetModuleSpecifier = "set_module_specifier"
)

// moduleOpActions lists the file-level module actions.
var moduleOpActions = []string{actAddNamedImport, actRemoveNamedImport, actRemoveImport, actSetModuleSpecifier}

// isModuleOp says whether action is one of the module actions above.
func isModuleOp(action string) bool {
	for _, a := range moduleOpActions {
		if a == action {
			return true
		}
	}
	return false
}

// tsImportLine matches one single-line import statement: the keyword prefix,
// the bindings (default, namespace and braces), and the module path.
var tsImportLine = regexp.MustCompile(`^(\s*import\s+(?:type\s+)?)(.*?)\s*from\s*(['"])([^'"]+)['"]\s*(;?)\s*$`)

// tsExportFromLine matches a single-line re-export such as export { a } from 'x'.
var tsExportFromLine = regexp.MustCompile(`^(\s*export\s+.*?\s*from\s*)(['"])([^'"]+)(['"])(\s*;?\s*)$`)

// moduleOp applies one module action to a TypeScript or JavaScript file.
func (e *editor) moduleOp(text string, o op) (string, string, error) {
	if e.lang.name != "typescript" && e.lang.name != "javascript" {
		return "", "", fmt.Errorf("%s supports TypeScript and JavaScript, not %s", o.Action, e.lang.name)
	}
	mod := strings.TrimSpace(o.Symbol)
	if mod == "" {
		return "", "", fmt.Errorf("%s needs the module path in symbol, such as ./utils", o.Action)
	}
	switch o.Action {
	case actAddNamedImport:
		return e.addNamedImport(text, mod, splitNames(o.NewCode))
	case actRemoveNamedImport:
		return removeNamedImport(text, mod, splitNames(o.NewCode))
	case actRemoveImport:
		return removeImport(text, mod)
	case actSetModuleSpecifier:
		return setModuleSpecifier(text, mod, strings.TrimSpace(o.NewCode))
	}
	return "", "", fmt.Errorf("unknown module action %q", o.Action)
}

// splitNames reads "a, b as c, type D" into its items, without empty ones.
func splitNames(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// importParts splits the bindings of an import into the default and namespace
// parts (kept as text) and the names inside braces.
func importParts(bindings string) (rest string, names []string, hasBraces bool) {
	open := strings.Index(bindings, "{")
	closing := strings.LastIndex(bindings, "}")
	if open < 0 || closing < open {
		return strings.Trim(bindings, " ,"), nil, false
	}
	rest = strings.Trim(bindings[:open]+bindings[closing+1:], " ,")
	return rest, splitNames(bindings[open+1 : closing]), true
}

// joinImport rebuilds one import line from its parts.
func joinImport(prefix, rest string, names []string, mod, quote, semi string) string {
	var binds []string
	if rest != "" {
		binds = append(binds, rest)
	}
	if len(names) > 0 {
		binds = append(binds, "{ "+strings.Join(names, ", ")+" }")
	}
	return prefix + strings.Join(binds, ", ") + " from " + quote + mod + quote + semi
}

// importLine is one single-line import from the module, with its match groups.
type importLine struct {
	index int
	match []string
}

func findImports(lines []string, mod string) []importLine {
	var out []importLine
	for i, l := range lines {
		m := tsImportLine.FindStringSubmatch(l)
		if m != nil && m[4] == mod {
			out = append(out, importLine{index: i, match: m})
		}
	}
	return out
}

// addNamedImport adds names to the import from mod, or adds a new import line
// when there is none that can take them.
func (e *editor) addNamedImport(text, mod string, names []string) (string, string, error) {
	if len(names) == 0 {
		return "", "", fmt.Errorf("add_named_import needs the names in new_code, such as \"useState, useEffect\"")
	}
	lines := strings.Split(text, "\n")
	// An import that already has braces takes the names first; a default-only
	// import takes them after that, and a namespace import never does.
	imports := findImports(lines, mod)
	sort.SliceStable(imports, func(i, j int) bool {
		_, _, bi := importParts(imports[i].match[2])
		_, _, bj := importParts(imports[j].match[2])
		return bi && !bj
	})
	for _, im := range imports {
		rest, have, braces := importParts(im.match[2])
		if !braces && strings.Contains(rest, "*") {
			continue
		}
		var added []string
		for _, n := range names {
			if !containsName(have, n) {
				have = append(have, n)
				added = append(added, n)
			}
		}
		if len(added) == 0 {
			return text, fmt.Sprintf("already present: %s are already imported from %s", strings.Join(names, ", "), mod), nil
		}
		lines[im.index] = joinImport(im.match[1], rest, have, mod, im.match[3], im.match[5])
		return strings.Join(lines, "\n"), fmt.Sprintf("added %s to the import from %s", strings.Join(added, ", "), mod), nil
	}
	stmt := "import { " + strings.Join(names, ", ") + " } from '" + mod + "';"
	out, msg, err := e.lineAddImport(text, stmt, false)
	if err != nil {
		return "", "", err
	}
	return out, msg, nil
}

// removeNamedImport takes names out of the import from mod. An import left
// with nothing in it is removed whole.
func removeNamedImport(text, mod string, names []string) (string, string, error) {
	lines := strings.Split(text, "\n")
	imports := findImports(lines, mod)
	if len(imports) == 0 {
		return "", "", fmt.Errorf("no single-line import from %s to remove names from", mod)
	}
	removed := false
	var drop []int
	for _, im := range imports {
		rest, have, braces := importParts(im.match[2])
		if !braces {
			continue
		}
		var kept []string
		for _, h := range have {
			if containsName(names, h) {
				removed = true
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) == 0 && rest == "" {
			drop = append(drop, im.index)
			continue
		}
		lines[im.index] = joinImport(im.match[1], rest, kept, mod, im.match[3], im.match[5])
	}
	if !removed {
		return text, fmt.Sprintf("already absent: none of %s are imported from %s", strings.Join(names, ", "), mod), nil
	}
	lines = dropLines(lines, drop)
	return strings.Join(lines, "\n"), fmt.Sprintf("removed %s from the import from %s", strings.Join(names, ", "), mod), nil
}

// removeImport removes every single-line import from mod.
func removeImport(text, mod string) (string, string, error) {
	lines := strings.Split(text, "\n")
	found := findImports(lines, mod)
	if len(found) == 0 {
		return text, fmt.Sprintf("already absent: nothing is imported from %s", mod), nil
	}
	var drop []int
	for _, im := range found {
		drop = append(drop, im.index)
	}
	return strings.Join(dropLines(lines, drop), "\n"), fmt.Sprintf("removed %d import(s) from %s", len(found), mod), nil
}

// setModuleSpecifier points every single-line import or re-export of from at to.
func setModuleSpecifier(text, from, to string) (string, string, error) {
	if to == "" {
		return "", "", fmt.Errorf("set_module_specifier needs the new module path in new_code")
	}
	lines := strings.Split(text, "\n")
	changed := 0
	for i, l := range lines {
		if m := tsImportLine.FindStringSubmatch(l); m != nil && m[4] == from {
			lines[i] = joinImport(m[1], importRest(m[2]), importNames(m[2]), to, m[3], m[5])
			changed++
			continue
		}
		if m := tsExportFromLine.FindStringSubmatch(l); m != nil && m[3] == from {
			lines[i] = m[1] + m[2] + to + m[4] + m[5]
			changed++
		}
	}
	if changed == 0 {
		return "", "", fmt.Errorf("no single-line import or re-export from %s to change", from)
	}
	return strings.Join(lines, "\n"), fmt.Sprintf("changed %d statement(s) from %s to %s", changed, from, to), nil
}

// importRest and importNames read the bindings of an import line.
func importRest(bindings string) string {
	rest, _, _ := importParts(bindings)
	return rest
}

func importNames(bindings string) []string {
	_, names, _ := importParts(bindings)
	return names
}

// containsName reports whether list has name. The comparison is on the whole
// item, so "a as b" matches only "a as b", not "a".
func containsName(list []string, name string) bool {
	for _, x := range list {
		if strings.TrimSpace(x) == strings.TrimSpace(name) {
			return true
		}
	}
	return false
}

// dropLines removes the lines at the given indexes. indexes are ascending.
func dropLines(lines []string, drop []int) []string {
	if len(drop) == 0 {
		return lines
	}
	skip := map[int]bool{}
	for _, i := range drop {
		skip[i] = true
	}
	out := lines[:0:0]
	for i, l := range lines {
		if !skip[i] {
			out = append(out, l)
		}
	}
	return out
}
