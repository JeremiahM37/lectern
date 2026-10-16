package symedit

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Python move_symbol. A top-level def, class or assignment moves to another
// module, and the "from X import name" statements that named it are rewritten
// in every other Python file. Relative imports keep their relative form.
// Module-style uses (import X, then X.name) cannot be rewritten by name, so
// they are reported as warnings.

// pyFromStmtRe matches a from-import statement in masked source. Group 1 is the
// indentation, 2 the leading dots, 3 the module, 4 the names.
var pyFromStmtRe = regexp.MustCompile(`(?m)^([ \t]*)from[ \t]+(\.*)([A-Za-z_][\w.]*|)[ \t]+import[ \t]+(\([^)]*\)|[^\n]*)`)

// pyTopDefRe matches a top-level def, class or simple assignment in masked source.
var pyTopDefRe = regexp.MustCompile(`(?m)^(?:async[ \t]+def|def|class)[ \t]+([A-Za-z_]\w*)|^([A-Za-z_]\w*)[ \t]*(?::[^=\n]*)?=[^=]`)

// pyBinding is one name in a from-import, with its alias if it has one.
type pyBinding struct{ name, alias string }

func (b pyBinding) local() string {
	if b.alias != "" {
		return b.alias
	}
	return b.name
}

func (b pyBinding) text() string {
	if b.alias != "" {
		return b.name + " as " + b.alias
	}
	return b.name
}

// parsePyNames reads the name list of a from-import (the text after "import").
// Parentheses, line breaks and comments are dropped.
func parsePyNames(raw string) []pyBinding {
	raw = strings.Trim(raw, "() \t\r\n")
	var out []pyBinding
	for _, part := range strings.Split(raw, ",") {
		if i := strings.Index(part, "#"); i >= 0 {
			part = part[:i]
		}
		fields := strings.Fields(part)
		switch {
		case len(fields) == 0:
			continue
		case len(fields) == 3 && fields[1] == "as":
			out = append(out, pyBinding{name: fields[0], alias: fields[2]})
		default:
			out = append(out, pyBinding{name: fields[0]})
		}
	}
	return out
}

// pyAbsMod resolves a from-import module written in file rel to its absolute
// dotted name. dots is the leading-dot run ("" for an absolute import). It
// returns "" when the dots climb above the project root.
func pyAbsMod(rel, dots, mod string) string {
	if dots == "" {
		return mod
	}
	pkg := pyPkg(rel)
	climb := len(dots) - 1
	if climb > len(pkg) {
		return ""
	}
	parts := append(append([]string{}, pkg[:len(pkg)-climb]...), splitDots(mod)...)
	return strings.Join(parts, ".")
}

// pyStmt renders a from-import with the given module spelling and names.
func pyStmt(indent, module string, names []pyBinding) string {
	var parts []string
	for _, n := range names {
		parts = append(parts, n.text())
	}
	return indent + "from " + module + " import " + strings.Join(parts, ", ")
}

// pyTopLevelDefs lists the names a Python module defines at top level.
func pyTopLevelDefs(masked string) map[string]bool {
	out := map[string]bool{}
	for _, m := range pyTopDefRe.FindAllStringSubmatch(masked, -1) {
		if m[1] != "" {
			out[m[1]] = true
		} else if m[2] != "" {
			out[m[2]] = true
		}
	}
	return out
}

// pyInsertImports puts statements after the last top-level import in src, or
// at the top when there is none.
func pyInsertImports(src string, stmts []string) string {
	if len(stmts) == 0 {
		return src
	}
	block := strings.Join(stmts, "\n") + "\n"
	masked := mask(src, languages[".py"].lex)
	at := 0
	for _, m := range pyFromStmtRe.FindAllStringSubmatchIndex(masked, -1) {
		if m[3] != m[2] { // indented: inside a block, not a module import
			continue
		}
		at = m[1]
		if nl := strings.IndexByte(src[at:], '\n'); nl >= 0 {
			at += nl + 1
		} else {
			src += "\n"
			at = len(src)
		}
	}
	if at == 0 {
		return block + src
	}
	return src[:at] + block + src[at:]
}

// rewritePyImports rewrites the from-imports in src that name the moved
// symbol. rel is the file src belongs to, fromMod the module the symbol left,
// toMod the module it went to, and drop reports that src is the destination,
// where a stale import of the symbol just disappears.
func rewritePyImports(src, rel, fromMod, toMod, sym string, drop bool, rep *moveReport) string {
	masked := mask(src, languages[".py"].lex)
	var b strings.Builder
	last := 0
	for _, m := range pyFromStmtRe.FindAllStringSubmatchIndex(masked, -1) {
		dots := src[m[4]:m[5]]
		mod := src[m[6]:m[7]]
		abs := pyAbsMod(rel, dots, mod)
		if abs != fromMod {
			continue
		}
		names := parsePyNames(src[m[8]:m[9]])
		var moved, keep []pyBinding
		for _, n := range names {
			if n.name == sym {
				moved = append(moved, n)
			} else {
				keep = append(keep, n)
			}
		}
		if len(moved) == 0 {
			continue
		}
		indent := src[m[2]:m[3]]
		var stmts []string
		if len(keep) > 0 {
			stmts = append(stmts, pyStmt(indent, dots+mod, keep))
		}
		if !drop {
			target := toMod
			if dots != "" {
				target = pyRelDotted(pyPkg(rel), toMod)
			}
			stmts = append(stmts, pyStmt(indent, target, moved))
		}
		b.WriteString(src[last:m[0]])
		b.WriteString(strings.Join(stmts, "\n"))
		last = m[1]
	}
	if last == 0 {
		return src
	}
	b.WriteString(src[last:])
	return b.String()
}

// moduleStyleUse reports an "import X" of the moved module followed by X.sym.
func moduleStyleUse(src, fromMod, sym string) bool {
	return strings.Contains(src, "import "+fromMod) && strings.Contains(src, fromMod+"."+sym)
}

// planMovePy moves a top-level Python definition from fromRel to toRel and
// rewrites the imports that name it.
func planMovePy(workdir, sym, fromRel, toRel string) ([]fileWrite, moveReport, error) {
	rep := moveReport{symbol: sym, from: fromRel, to: toRel}
	fromSrc, ok := readProjectText(workdir, fromRel)
	if !ok {
		return nil, rep, fmt.Errorf("cannot read %s as text", fromRel)
	}
	lx := languages[".py"].lex
	fromMasked := mask(fromSrc, lx)
	ed := &editor{lang: languages[".py"], rel: fromRel, base: path.Base(fromRel)}
	defer ed.close()
	syms, err := ed.symbols(fromSrc)
	if err != nil {
		return nil, rep, err
	}
	s, err := findSymbol(syms, parseSpec(sym), fromRel, sym)
	if err != nil {
		return nil, rep, err
	}
	rep.symbol = s.name
	if s.parent != "" {
		return nil, rep, fmt.Errorf("%s moves top-level definitions. %s is a member of %s; move the whole %s instead.", moveName, sym, s.parent, s.parent)
	}
	declSrc := fromSrc[s.start:s.end]
	declMasked := fromMasked[s.start:s.end]

	fromMod, toMod := pyModName(fromRel), pyModName(toRel)
	tops := pyTopLevelDefs(fromMasked)
	var dstImports []string
	// Names the moved code reads from this module: imported names come from
	// their original module, and other top-level names come back from here.
	for _, m := range pyFromStmtRe.FindAllStringSubmatchIndex(fromMasked, -1) {
		abs := pyAbsMod(fromRel, fromSrc[m[4]:m[5]], fromSrc[m[6]:m[7]])
		var used []pyBinding
		for _, n := range parsePyNames(fromSrc[m[8]:m[9]]) {
			if len(wordHits(declMasked, n.local())) > 0 {
				used = append(used, n)
			}
		}
		if len(used) == 0 {
			continue
		}
		mod := abs
		if fromSrc[m[4]:m[5]] != "" {
			mod = pyRelDotted(pyPkg(toRel), abs)
		}
		dstImports = append(dstImports, pyStmt("", mod, used))
	}
	var own []string
	for n := range tops {
		if n != s.name && len(wordHits(declMasked, n)) > 0 {
			own = append(own, n)
		}
	}
	if len(own) > 0 {
		rep.warnings = append(rep.warnings, fmt.Sprintf("the moved %s reads %s from %s, so %s imports %s back. Check for a circular import.",
			s.name, strings.Join(own, ", "), fromRel, toRel, fromMod))
		var bs []pyBinding
		for _, n := range own {
			bs = append(bs, pyBinding{name: n})
		}
		dstImports = append(dstImports, pyStmt("", fromMod, bs))
	}

	// Destination: its own text, minus any stale import of the symbol, plus
	// the moved definition.
	toText := ""
	toAbs := filepath.Join(workdir, filepath.FromSlash(toRel))
	if _, statErr := os.Stat(toAbs); statErr == nil {
		toSrc, ok := readProjectText(workdir, toRel)
		if !ok {
			return nil, rep, fmt.Errorf("cannot read %s as text", toRel)
		}
		if pyTopLevelDefs(mask(toSrc, lx))[s.name] {
			return nil, rep, fmt.Errorf("%s already defines %s. Rename one of them, or move %s to a new file.", toRel, s.name, s.name)
		}
		toText = rewritePyImports(toSrc, toRel, fromMod, toMod, s.name, true, &rep)
	}
	if strings.TrimSpace(toText) != "" {
		toText = strings.TrimRight(toText, "\n") + "\n\n\n" + declSrc + "\n"
	} else {
		toText = declSrc + "\n"
	}
	toText = pyInsertImports(toText, dstImports)
	for _, st := range dstImports {
		rep.added = append(rep.added, strings.TrimSpace(st))
	}

	// Source: the definition goes. The name comes back as an import when the
	// rest of the module still uses it.
	fromOut, _, _, err := ed.apply(fromSrc, op{Action: "delete", Symbol: sym})
	if err != nil {
		return nil, rep, err
	}
	if len(wordHits(stripPyImports(mask(fromOut, lx)), s.name)) > 0 {
		fromOut = pyInsertImports(fromOut, []string{pyStmt("", toMod, []pyBinding{{name: s.name}})})
	}

	writes := []fileWrite{{rel: toRel, content: toText}}
	for _, rel := range projectFiles(workdir) {
		if rel == fromRel || rel == toRel || extOf(rel) != ".py" {
			continue
		}
		src, ok := readProjectText(workdir, rel)
		if !ok || !strings.Contains(src, s.name) {
			continue
		}
		if moduleStyleUse(src, fromMod, s.name) {
			rep.warnings = append(rep.warnings, fmt.Sprintf("%s uses %s.%s through the module. Change those uses to %s by hand.", rel, fromMod, s.name, s.name))
		}
		out := rewritePyImports(src, rel, fromMod, toMod, s.name, false, &rep)
		if out != src {
			writes = append(writes, fileWrite{rel: rel, content: out})
			rep.importers = append(rep.importers, rel)
		}
	}
	writes = append(writes, fileWrite{rel: fromRel, content: fromOut})
	return writes, rep, nil
}

// stripPyImports blanks the from-import statements in masked source, so a
// name used only by an import does not count as a use.
func stripPyImports(masked string) string {
	return pyFromStmtRe.ReplaceAllStringFunc(masked, func(m string) string { return strings.Repeat(" ", len(m)) })
}
