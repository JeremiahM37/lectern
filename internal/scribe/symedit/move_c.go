package symedit

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// C and C++ move_symbol. A top-level function definition, or a struct, union,
// enum or class definition, moves to another file of the C family. Files that
// #include the source file and use the name are the importers. When the
// destination is a header, an importer includes it as well, and keeps its
// include of the source file only while the source still declares something
// the importer uses. Prototypes are not located by the symbol reader, so they
// cannot be moved; a function definition may enter a header only when inline.

// cIncludeRe matches an #include line in raw source. Group 1 is the opening
// delimiter, group 2 the path.
var cIncludeRe = regexp.MustCompile(`(?m)^[ \t]*#[ \t]*include[ \t]*([<"])([^>"\n]+)[>"][^\n]*`)

// cDirectiveRe matches the directives new includes go after.
var cDirectiveRe = regexp.MustCompile(`(?m)^[ \t]*#[ \t]*(?:include|pragma)\b[^\n]*`)

// cKeywords are words that name no file-level declaration, so they never make
// an include necessary.
var cKeywords = map[string]bool{
	"auto": true, "bool": true, "break": true, "case": true, "char": true, "class": true,
	"const": true, "continue": true, "default": true, "delete": true, "do": true, "double": true,
	"else": true, "enum": true, "extern": true, "false": true, "float": true, "for": true,
	"goto": true, "if": true, "inline": true, "int": true, "long": true, "namespace": true,
	"new": true, "NULL": true, "private": true, "protected": true, "public": true, "register": true,
	"restrict": true, "return": true, "short": true, "signed": true, "sizeof": true, "static": true,
	"struct": true, "switch": true, "template": true, "this": true, "true": true, "typedef": true,
	"typename": true, "union": true, "unsigned": true, "using": true, "virtual": true, "void": true,
	"volatile": true, "while": true,
}

var cSourceExts = map[string]bool{".c": true, ".cc": true, ".cpp": true, ".cxx": true}
var cHeaderExts = map[string]bool{".h": true, ".hh": true, ".hpp": true, ".hxx": true}

// cFamilyExt reports whether a file extension belongs to the C family.
func cFamilyExt(ext string) bool { return cSourceExts[ext] || cHeaderExts[ext] }

// cFamilyLang reports whether a language is C or C++.
func cFamilyLang(l *language) bool { return l != nil && (l.name == "c" || l.name == "cpp") }

// cInclude is one #include line of a file.
type cInclude struct {
	spec       string // the path between the delimiters
	angled     bool   // <...> rather than "..."
	target     string // the project file it names, "" when it is not in the project
	start, end int    // byte range of the line, without its newline
}

// cResolve finds the project file an include names. A quoted include is looked
// up next to the including file first, then from the project root.
func cResolve(workdir, importer, spec string, angled bool) string {
	var cands []string
	if !angled {
		cands = append(cands, path.Join(path.Dir(importer), spec))
	}
	cands = append(cands, path.Clean(spec))
	for _, c := range cands {
		if fi, err := os.Stat(filepath.Join(workdir, filepath.FromSlash(c))); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

// cScanIncludes lists the #include lines of src, the text of file rel.
func cScanIncludes(workdir, rel, src string) []cInclude {
	var out []cInclude
	for _, m := range cIncludeRe.FindAllStringSubmatchIndex(src, -1) {
		inc := cInclude{spec: src[m[4]:m[5]], angled: src[m[2]:m[3]] == "<", start: m[0], end: m[1]}
		inc.target = cResolve(workdir, rel, inc.spec, inc.angled)
		out = append(out, inc)
	}
	return out
}

// cHasInclude reports whether one of the includes names target.
func cHasInclude(incs []cInclude, target string) bool {
	for _, inc := range incs {
		if inc.target == target {
			return true
		}
	}
	return false
}

// cIncludeLine writes an include of target as the file importer would spell it:
// relative to the importer's own directory.
func cIncludeLine(importer, target string) string {
	rel, err := filepath.Rel(filepath.FromSlash(path.Dir(importer)), filepath.FromSlash(target))
	if err != nil {
		rel = target
	}
	return `#include "` + filepath.ToSlash(rel) + `"`
}

// cInsertIncludes puts include lines after the last #include or #pragma in
// text, or at the top when there is none.
func cInsertIncludes(text string, lines []string) string {
	if len(lines) == 0 {
		return text
	}
	block := strings.Join(lines, "\n") + "\n"
	at := -1
	for _, loc := range cDirectiveRe.FindAllStringIndex(text, -1) {
		at = loc[1]
	}
	if at >= 0 {
		if at < len(text) {
			at++
		} else {
			text += "\n"
			at = len(text)
		}
		return text[:at] + block + text[at:]
	}
	if strings.TrimSpace(text) == "" {
		return block
	}
	return block + "\n" + text
}

// cIdentSet is the identifiers in masked C or C++ code, without keywords.
func cIdentSet(masked string) map[string]bool {
	out := map[string]bool{}
	for id := range symIdentSet(masked) {
		if !cKeywords[id] {
			out[id] = true
		}
	}
	return out
}

// cAnyIdent reports whether masked code uses any identifier in set.
func cAnyIdent(masked string, set map[string]bool) bool {
	for _, id := range symIdentRe.FindAllString(masked, -1) {
		if set[id] {
			return true
		}
	}
	return false
}

// cHeadHas reports whether a word appears before the first { of a masked
// declaration, which is where its specifiers are.
func cHeadHas(masked, word string) bool {
	head := masked
	if i := strings.Index(masked, "{"); i >= 0 {
		head = masked[:i]
	}
	return len(wordHits(head, word)) > 0
}

// planMoveC moves a top-level C or C++ definition from fromRel to toRel and
// adds the includes the importers and the moved code need.
func planMoveC(workdir, sym, fromRel, toRel string) ([]fileWrite, moveReport, error) {
	rep := moveReport{from: fromRel, to: toRel}
	fromSrc, ok := readProjectText(workdir, fromRel)
	if !ok {
		return nil, rep, fmt.Errorf("cannot read %s as text", fromRel)
	}
	fromMasked := mask(fromSrc, cLexer)
	ed := &editor{lang: languages[extOf(fromRel)], rel: fromRel, base: path.Base(fromRel)}
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
	toHeader := cHeaderExts[extOf(toRel)]
	if toHeader && s.kind == "function" && !cHeadHas(declMasked, "inline") {
		return nil, rep, fmt.Errorf("%s is a function definition, and a header can only hold an inline one. Mark %s inline, or move it to a source file.", s.name, s.name)
	}
	if cHeadHas(declMasked, "static") {
		if remains := mask(removeRange(fromSrc, s.start, s.end), cLexer); len(wordHits(remains, s.name)) > 0 {
			return nil, rep, fmt.Errorf("%s is static, so %s cannot call it once it lives in %s. Make it non-static first, or move its callers too.", s.name, fromRel, toRel)
		}
	}

	toSrc := ""
	toExists := false
	if _, err := os.Stat(filepath.Join(workdir, filepath.FromSlash(toRel))); err == nil {
		toExists = true
		if toSrc, ok = readProjectText(workdir, toRel); !ok {
			return nil, rep, fmt.Errorf("cannot read %s as text", toRel)
		}
		toEd := &editor{lang: languages[extOf(toRel)], rel: toRel, base: path.Base(toRel)}
		tsyms, err := toEd.symbols(toSrc)
		toEd.close()
		if err != nil {
			return nil, rep, err
		}
		for _, t := range tsyms {
			if t.parent == "" && t.name == s.name {
				return nil, rep, fmt.Errorf("%s already defines %s. Rename one of them, or move %s to a new file.", toRel, s.name, s.name)
			}
		}
	}

	// What the moved code needs from the source file's side: the source itself
	// when it is a header, or the local headers of a source file that declare the
	// names it uses. A name the source defines and no header declares cannot be
	// reached from another file, so that case is refused.
	uses := cIdentSet(declMasked)
	delete(uses, s.name)
	fromIncs := cScanIncludes(workdir, fromRel, fromSrc)
	var needs []string
	if cHeaderExts[extOf(fromRel)] {
		if cAnyIdent(fromMasked[:s.start]+fromMasked[s.end:], uses) {
			needs = append(needs, fromRel)
		}
	} else {
		covered := map[string]bool{}
		for _, inc := range fromIncs {
			if inc.target == "" || inc.target == toRel {
				continue
			}
			hdr, ok := readProjectText(workdir, inc.target)
			if !ok {
				continue
			}
			hm := mask(hdr, cLexer)
			for name := range uses {
				if len(wordHits(hm, name)) > 0 {
					covered[name] = true
				}
			}
			if cAnyIdent(hm, uses) {
				needs = append(needs, inc.target)
			}
		}
		for _, t := range syms {
			if t.parent == "" && t.name != s.name && uses[t.name] && !covered[t.name] {
				return nil, rep, fmt.Errorf("the moved %s uses %s, which %s defines and no header declares. Another file cannot see it, so move %s too, or declare it in a header that %s includes.", s.name, t.name, fromRel, t.name, fromRel)
			}
		}
		for _, inc := range fromIncs {
			if inc.angled {
				rep.warnings = append(rep.warnings, fmt.Sprintf("%s includes <...> headers. If the moved %s needs one of them, add it to %s by hand.", fromRel, s.name, toRel))
				break
			}
		}
	}

	// Destination: its own text, the includes the moved code needs, then the
	// definition. A new header starts with #pragma once.
	base := toSrc
	if !toExists && toHeader {
		base = "#pragma once\n"
	}
	var toIncs []cInclude
	if toExists {
		toIncs = cScanIncludes(workdir, toRel, toSrc)
	}
	var lines []string
	for _, t := range needs {
		if cHasInclude(toIncs, t) {
			continue
		}
		line := cIncludeLine(toRel, t)
		lines = append(lines, line)
		rep.added = append(rep.added, line)
	}
	toText := cInsertIncludes(base, lines)
	if strings.TrimSpace(toText) != "" {
		toText = strings.TrimRight(toText, "\n") + "\n\n" + declSrc + "\n"
	} else {
		toText = declSrc + "\n"
	}

	// Source: the definition goes. If the rest of the file still uses the name,
	// a header destination is included; a source destination is left to the
	// declarations the file already has, and the report says so.
	fromOut := removeRange(fromSrc, s.start, s.end)
	if len(wordHits(mask(fromOut, cLexer), s.name)) > 0 {
		if toHeader {
			if !cHasInclude(fromIncs, toRel) {
				fromOut = cInsertIncludes(fromOut, []string{cIncludeLine(fromRel, toRel)})
			}
		} else {
			rep.warnings = append(rep.warnings, fmt.Sprintf("%s still uses %s, which now lives in %s. Check that a header %s includes declares %s.", fromRel, s.name, toRel, fromRel, s.name))
		}
	}

	writes := []fileWrite{{rel: toRel, content: toText}}
	remaining := cIdentSet(mask(fromOut, cLexer))
	delete(remaining, s.name)
	for _, rel := range projectFiles(workdir) {
		if rel == fromRel || rel == toRel || !cFamilyExt(extOf(rel)) {
			continue
		}
		src, ok := readProjectText(workdir, rel)
		if !ok {
			continue
		}
		masked := mask(src, cLexer)
		if len(wordHits(masked, s.name)) == 0 {
			continue
		}
		incs := cScanIncludes(workdir, rel, src)
		idx := -1
		for i, inc := range incs {
			if inc.target == fromRel {
				idx = i
				break
			}
		}
		if idx < 0 {
			if bareHits(masked, s.name) > 0 {
				rep.warnings = append(rep.warnings, fmt.Sprintf("%s uses %s but does not include %s. Check how it gets %s.", rel, s.name, fromRel, s.name))
			}
			continue
		}
		if !toHeader {
			continue
		}
		inc := incs[idx]
		var repl []string
		if cAnyIdent(masked, remaining) {
			repl = append(repl, src[inc.start:inc.end])
		}
		if !cHasInclude(incs, toRel) {
			repl = append(repl, cIncludeLine(rel, toRel))
		}
		end := inc.end
		if len(repl) == 0 && end < len(src) && src[end] == '\n' {
			end++
		}
		out := src[:inc.start] + strings.Join(repl, "\n") + src[end:]
		if out != src {
			writes = append(writes, fileWrite{rel: rel, content: out})
			rep.importers = append(rep.importers, rel)
		}
	}
	writes = append(writes, fileWrite{rel: fromRel, content: fromOut})
	return writes, rep, nil
}
