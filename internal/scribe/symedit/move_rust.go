package symedit

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Rust move_symbol. A top-level fn, struct, enum, trait, const or type moves to
// another module of the same crate. The use statements that name it through
// the module it left are rewritten in the crate's other files. Only the forms
// this file reads are rewritten: a path with a name or a flat item list, each
// item a name with an optional alias. A glob, a nested list, or a path that is
// not crate, self or super relative is reported and left alone.

// rustUseRe matches a use statement in masked source. Group 1 is the indent and
// visibility, group 2 the text between "use" and the semicolon.
var rustUseRe = regexp.MustCompile(`(?m)^([ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?)use[ \t]+([^;]+);`)

// rustTopUseRe matches a top-level use statement. New ones go after the last.
var rustTopUseRe = regexp.MustCompile(`(?m)^(?:pub(?:\([^)\n]*\))?[ \t]+)?use[ \t]+[^;]+;`)

// rustScopeRe matches a run of self:: or super:: path prefixes.
var rustScopeRe = regexp.MustCompile(`(?:\b(?:self|super)::)+`)

// rustLeadRe matches what sits above an item's keyword: space and attributes.
var rustLeadRe = regexp.MustCompile(`^(?:\s|#\[[^\]\n]*\])*`)

// rustPubRe matches an item that starts with a visibility keyword.
var rustPubRe = regexp.MustCompile(`^pub\b`)

// symIdentRe matches identifiers in masked source.
var symIdentRe = regexp.MustCompile(`[A-Za-z_]\w*`)

// rustModule is where a Rust file sits in its crate: the directory holding the
// crate's src/, and the module path below the crate root.
type rustModule struct {
	crate string
	path  []string
}

// rustModuleOf reads a file's module path from its place under src/. Files
// outside a src/ directory and bin/ targets (each a crate of its own) are not
// handled.
func rustModuleOf(rel string) (rustModule, bool) {
	parts := strings.Split(rel, "/")
	idx := -1
	for i := len(parts) - 2; i >= 0; i-- {
		if parts[i] == "src" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return rustModule{}, false
	}
	crate := strings.Join(parts[:idx+1], "/")
	segs := parts[idx+1:]
	last := strings.TrimSuffix(segs[len(segs)-1], ".rs")
	if len(segs) == 1 && (last == "lib" || last == "main") {
		return rustModule{crate: crate}, true
	}
	mod := append([]string{}, segs[:len(segs)-1]...)
	if last != "mod" {
		mod = append(mod, last)
	}
	if len(mod) > 0 && mod[0] == "bin" {
		return rustModule{}, false
	}
	return rustModule{crate: crate, path: mod}, true
}

func rustPathText(segs []string) string { return strings.Join(segs, "::") }

func rustSamePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// rustResolve turns the segments of a path written in module m into an
// absolute module path. ok is false for a path that is not crate, self or
// super relative, which may name an external crate or a sibling module.
func rustResolve(m []string, segs []string) ([]string, bool) {
	if len(segs) == 0 {
		return nil, false
	}
	switch segs[0] {
	case "crate":
		return append([]string{}, segs[1:]...), true
	case "self":
		return append(append([]string{}, m...), segs[1:]...), true
	case "super":
		base := append([]string{}, m...)
		i := 0
		for ; i < len(segs) && segs[i] == "super"; i++ {
			if len(base) == 0 {
				return nil, false
			}
			base = base[:len(base)-1]
		}
		return append(base, segs[i:]...), true
	}
	return nil, false
}

// rustSpell writes the absolute module path to in the style the importer used:
// crate:: when it used crate::, otherwise the super:: and self:: form that
// reaches to from module from.
func rustSpell(from, to []string, crateStyle bool) string {
	if crateStyle {
		return rustPathText(append([]string{"crate"}, to...))
	}
	c := 0
	for c < len(from) && c < len(to) && from[c] == to[c] {
		c++
	}
	var segs []string
	if len(from) == c {
		segs = append(segs, "self")
	}
	for i := c; i < len(from); i++ {
		segs = append(segs, "super")
	}
	return rustPathText(append(segs, to[c:]...))
}

// rustItem is one name in a use list, with its alias if it has one.
type rustItem struct{ name, alias string }

func (it rustItem) local() string {
	if it.alias != "" {
		return it.alias
	}
	return it.name
}

func (it rustItem) text() string {
	if it.alias != "" {
		return it.name + " as " + it.alias
	}
	return it.name
}

// rustUse is a use statement this file can read in full.
type rustUse struct {
	prefix     []string
	prefixText string
	items      []rustItem
	glob       bool
}

// parseRustItem reads "name" or "name as alias".
func parseRustItem(s string) (rustItem, bool) {
	if strings.ContainsAny(s, ":*{}") {
		return rustItem{}, false
	}
	f := strings.Fields(s)
	switch {
	case len(f) == 1:
		return rustItem{name: f[0]}, true
	case len(f) == 3 && f[1] == "as":
		return rustItem{name: f[0], alias: f[2]}, true
	}
	return rustItem{}, false
}

// parseRustUse reads the text of a use statement, without "use" and ";".
func parseRustUse(body string) (rustUse, bool) {
	var u rustUse
	body = strings.Join(strings.Fields(body), " ")
	switch {
	case strings.HasSuffix(body, "::*"):
		u.glob = true
		body = strings.TrimSuffix(body, "::*")
	case strings.Contains(body, "{"):
		i := strings.Index(body, "{")
		if !strings.HasSuffix(body, "}") {
			return u, false
		}
		for _, part := range strings.Split(body[i+1:len(body)-1], ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			it, ok := parseRustItem(part)
			if !ok {
				return u, false
			}
			u.items = append(u.items, it)
		}
		if len(u.items) == 0 {
			return u, false
		}
		body = strings.TrimSuffix(strings.TrimSpace(body[:i]), "::")
	default:
		i := strings.LastIndex(body, "::")
		if i < 0 {
			return u, false
		}
		it, ok := parseRustItem(body[i+2:])
		if !ok {
			return u, false
		}
		u.items = []rustItem{it}
		body = body[:i]
	}
	u.prefixText = strings.TrimSpace(body)
	if u.prefixText == "" {
		return u, false
	}
	for _, seg := range strings.Split(u.prefixText, "::") {
		seg = strings.TrimSpace(seg)
		if seg == "" || strings.ContainsAny(seg, " {}*") {
			return u, false
		}
		u.prefix = append(u.prefix, seg)
	}
	return u, true
}

// rustStmt renders a use statement with the given visibility, path and items.
func rustStmt(vis, prefix string, items []rustItem) string {
	if len(items) == 1 {
		return vis + "use " + prefix + "::" + items[0].text() + ";"
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, it.text())
	}
	return vis + "use " + prefix + "::{" + strings.Join(parts, ", ") + "};"
}

// rustUseSet collects the items the moved code needs, grouped by the path they
// come from, in the order they were first seen.
type rustUseSet struct {
	order []string
	items map[string][]rustItem
}

func (u *rustUseSet) add(prefix string, it rustItem) {
	if u.items == nil {
		u.items = map[string][]rustItem{}
	}
	if _, ok := u.items[prefix]; !ok {
		u.order = append(u.order, prefix)
	}
	for _, have := range u.items[prefix] {
		if have.local() == it.local() {
			return
		}
	}
	u.items[prefix] = append(u.items[prefix], it)
}

func (u *rustUseSet) lines() []string {
	var out []string
	for _, p := range u.order {
		out = append(out, rustStmt("", p, u.items[p]))
	}
	return out
}

// rustIsPub reports whether a masked item declaration is public.
func rustIsPub(masked string) bool {
	at := rustLeadRe.FindStringIndex(masked)[1]
	return rustPubRe.MatchString(masked[at:])
}

// rustMakePub adds pub to a declaration that has no visibility. masked is the
// same text with comments and strings blanked, so offsets line up.
func rustMakePub(decl, masked string) string {
	at := rustLeadRe.FindStringIndex(masked)[1]
	if rustPubRe.MatchString(masked[at:]) {
		return decl
	}
	return decl[:at] + "pub " + decl[at:]
}

// rustRelocate rewrites self:: and super:: prefixes in a moved declaration as
// crate:: paths, because the code now sits in another module. masked is the
// declaration with comments and strings blanked.
func rustRelocate(decl, masked string, fromPath []string) string {
	var b strings.Builder
	last := 0
	for _, loc := range rustScopeRe.FindAllStringIndex(masked, -1) {
		if loc[0] >= 2 && masked[loc[0]-2:loc[0]] == "::" {
			continue
		}
		segs := strings.Split(strings.TrimSuffix(decl[loc[0]:loc[1]], "::"), "::")
		base, ok := rustResolve(fromPath, segs)
		if !ok {
			continue
		}
		b.WriteString(decl[last:loc[0]])
		b.WriteString(rustPathText(append([]string{"crate"}, base...)) + "::")
		last = loc[1]
	}
	b.WriteString(decl[last:])
	return b.String()
}

// rustInsertUses puts use statements after the last top-level use in text, or
// at the top when there is none.
func rustInsertUses(text string, stmts []string) string {
	var fresh []string
	for _, s := range stmts {
		if !strings.Contains(text, s) {
			fresh = append(fresh, s)
		}
	}
	if len(fresh) == 0 {
		return text
	}
	block := strings.Join(fresh, "\n") + "\n"
	masked := mask(text, languages[".rs"].lex)
	at := -1
	for _, loc := range rustTopUseRe.FindAllStringIndex(masked, -1) {
		at = loc[1]
	}
	if at >= 0 {
		if nl := strings.IndexByte(text[at:], '\n'); nl >= 0 {
			at += nl + 1
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

// rustStripUses blanks the use statements in masked source, so a name that is
// only imported does not count as a use.
func rustStripUses(masked string) string {
	return rustUseRe.ReplaceAllStringFunc(masked, func(m string) string { return strings.Repeat(" ", len(m)) })
}

// rustBareUse reports a name used in code outside any use statement, while no
// use statement names it. Such a use reaches the name through a glob import or
// a full path, which this tool does not rewrite.
func rustBareUse(src, sym string) bool {
	masked := mask(src, languages[".rs"].lex)
	if bareHits(rustStripUses(masked), sym) == 0 {
		return false
	}
	for _, loc := range rustUseRe.FindAllStringSubmatchIndex(masked, -1) {
		if len(wordHits(masked[loc[4]:loc[5]], sym)) > 0 {
			return false
		}
	}
	return true
}

// bareHits counts whole-word hits of name in masked code that are not a field,
// method or associated item (a hit after '.', ':' or '>').
func bareHits(masked, name string) int {
	n := 0
	for _, off := range wordHits(masked, name) {
		if off > 0 && strings.IndexByte(".:>", masked[off-1]) >= 0 {
			continue
		}
		n++
	}
	return n
}

// rustRewriteUses rewrites the use statements in src (the text of the file at
// module m) that import sym from the module fromPath. The name leaves the old
// statement. Unless the file is itself the destination module toPath, it
// imports the name from toPath instead.
func rustRewriteUses(src, rel string, m rustModule, fromPath, toPath []string, sym string, rep *moveReport) string {
	masked := mask(src, languages[".rs"].lex)
	drop := rustSamePath(m.path, toPath)
	var b strings.Builder
	last := 0
	changed := false
	for _, loc := range rustUseRe.FindAllStringSubmatchIndex(masked, -1) {
		g := src[loc[2]:loc[3]]
		vis := strings.TrimLeft(g, " \t")
		lead := g[:len(g)-len(vis)]
		body := src[loc[4]:loc[5]]
		u, ok := parseRustUse(body)
		if !ok {
			if len(wordHits(body, sym)) > 0 {
				rep.warnings = append(rep.warnings, fmt.Sprintf("%s has a use statement this tool cannot read, and it names %s. Check it by hand: %s", rel, sym, strings.TrimSpace(body)))
			}
			continue
		}
		abs, resolved := rustResolve(m.path, u.prefix)
		if !resolved {
			// A bare path may name a module of this crate, which this tool does
			// not resolve, so a use of the name through one is reported.
			if len(wordHits(body, sym)) > 0 {
				rep.warnings = append(rep.warnings, fmt.Sprintf("%s imports %s by a path that is not crate::, self:: or super::, so it was not checked. Update it by hand if it names %s.", rel, sym, sym))
			}
			continue
		}
		if !rustSamePath(abs, fromPath) {
			continue
		}
		if u.glob {
			rep.warnings = append(rep.warnings, fmt.Sprintf("%s imports everything from %s with a glob. %s is no longer in that module, so a use of %s that relied on the glob will not resolve. Import %s by name if it needs it.", rel, rustPathText(append([]string{"crate"}, fromPath...)), sym, sym, sym))
			continue
		}
		var moved, keep []rustItem
		for _, it := range u.items {
			if it.name == sym {
				moved = append(moved, it)
			} else {
				keep = append(keep, it)
			}
		}
		if len(moved) == 0 {
			continue
		}
		var stmts []string
		if len(keep) > 0 {
			stmts = append(stmts, rustStmt(vis, u.prefixText, keep))
		}
		if !drop {
			stmts = append(stmts, rustStmt(vis, rustSpell(m.path, toPath, u.prefix[0] == "crate"), moved))
		}
		end := loc[1]
		var repl string
		if len(stmts) == 0 {
			if end < len(src) && src[end] == '\n' {
				end++
			}
		} else {
			repl = lead + strings.Join(stmts, "\n"+lead)
		}
		b.WriteString(src[last:loc[0]])
		b.WriteString(repl)
		last = end
		changed = true
	}
	if !changed {
		return src
	}
	b.WriteString(src[last:])
	return b.String()
}

// rustDeclared reports whether the parent module of m declares it with mod.
func rustDeclared(workdir string, m rustModule) bool {
	name := m.path[len(m.path)-1]
	var cands []string
	if len(m.path) == 1 {
		cands = []string{m.crate + "/lib.rs", m.crate + "/main.rs"}
	} else {
		dir := m.crate + "/" + strings.Join(m.path[:len(m.path)-1], "/")
		cands = []string{dir + ".rs", dir + "/mod.rs"}
	}
	decl := regexp.MustCompile(`(?m)^[ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?mod[ \t]+` + regexp.QuoteMeta(name) + `[ \t]*;`)
	for _, c := range cands {
		if src, ok := readProjectText(workdir, c); ok && decl.MatchString(src) {
			return true
		}
	}
	return false
}

// planMoveRust moves a top-level Rust item from fromRel to toRel within one
// crate and rewrites the use statements that named it.
func planMoveRust(workdir, sym, fromRel, toRel string) ([]fileWrite, moveReport, error) {
	rep := moveReport{from: fromRel, to: toRel}
	fromMod, okFrom := rustModuleOf(fromRel)
	toMod, okTo := rustModuleOf(toRel)
	if !okFrom || !okTo || fromMod.crate != toMod.crate {
		return nil, rep, fmt.Errorf("%s moves Rust items inside one crate's src/ directory. %s and %s are not both there (bin/ targets and files outside src/ are not handled).", moveName, fromRel, toRel)
	}
	lx := languages[".rs"].lex
	fromSrc, ok := readProjectText(workdir, fromRel)
	if !ok {
		return nil, rep, fmt.Errorf("cannot read %s as text", fromRel)
	}
	fromMasked := mask(fromSrc, lx)
	ed := &editor{lang: languages[".rs"], rel: fromRel, base: path.Base(fromRel)}
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
		return nil, rep, fmt.Errorf("%s moves top-level items. %s is a member of %s; move the whole %s instead.", moveName, sym, s.parent, s.parent)
	}

	// Attribute lines directly above the item (#[derive(...)]) move with it.
	idx := newLineIndex(fromSrc)
	startLine := s.startLine
	for startLine > 1 && strings.HasPrefix(strings.TrimSpace(idx.text(startLine-1)), "#[") {
		startLine--
	}
	start := idx.start(startLine)
	declSrc := fromSrc[start:s.end]
	declMasked := fromMasked[start:s.end]

	toAbs := filepath.Join(workdir, filepath.FromSlash(toRel))
	toSrc := ""
	toExists := false
	if _, err := os.Stat(toAbs); err == nil {
		toExists = true
		if toSrc, ok = readProjectText(workdir, toRel); !ok {
			return nil, rep, fmt.Errorf("cannot read %s as text", toRel)
		}
		toEd := &editor{lang: languages[".rs"], rel: toRel, base: path.Base(toRel)}
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

	// The moved item needs the imports its names came from, and the top-level
	// items of this module that it uses. A private item of this module cannot be
	// reached from the destination, so that case is refused, not guessed at.
	// A name counts as used only where the declaration writes it bare. A path
	// such as self::name is rewritten to crate:: and needs no import.
	usedBare := func(name string) bool { return bareHits(declMasked, name) > 0 }
	handled := map[string]bool{}
	var dst rustUseSet
	for _, loc := range rustUseRe.FindAllStringSubmatchIndex(fromMasked, -1) {
		u, ok := parseRustUse(fromSrc[loc[4]:loc[5]])
		if !ok || u.glob {
			continue
		}
		prefix := u.prefixText
		if abs, resolved := rustResolve(fromMod.path, u.prefix); resolved {
			prefix = rustPathText(append([]string{"crate"}, abs...))
		}
		for _, it := range u.items {
			if it.name == "self" || !usedBare(it.local()) {
				continue
			}
			dst.add(prefix, it)
			handled[it.local()] = true
		}
	}
	fromCrateRoot := rustPathText(append([]string{"crate"}, fromMod.path...))
	for _, t := range syms {
		if t.parent != "" || t.name == s.name || !usedBare(t.name) || handled[t.name] {
			continue
		}
		if !rustIsPub(fromMasked[t.start:t.end]) {
			return nil, rep, fmt.Errorf("the moved %s uses %s, which is private to %s. Make %s pub first, then move %s.", s.name, t.name, fromRel, t.name, s.name)
		}
		dst.add(fromCrateRoot, rustItem{name: t.name})
		handled[t.name] = true
	}

	// self:: and super:: in the moved text are rewritten as crate:: paths, and
	// the item is made pub, because the source file imports it back and other
	// modules name it.
	decl := rustRelocate(declSrc, declMasked, fromMod.path)
	decl = rustMakePub(decl, mask(decl, lx))

	toText := ""
	if toExists {
		toText = rustRewriteUses(toSrc, toRel, toMod, fromMod.path, toMod.path, s.name, &rep)
	}
	toText = rustInsertUses(toText, dst.lines())
	rep.added = append(rep.added, dst.lines()...)
	if strings.TrimSpace(toText) != "" {
		toText = strings.TrimRight(toText, "\n") + "\n\n" + decl + "\n"
	} else {
		toText = decl + "\n"
	}

	// Source: the item goes, and its name comes back as an import if the rest of
	// the file still uses it.
	fromOut := removeRange(fromSrc, start, s.end)
	if len(wordHits(rustStripUses(mask(fromOut, lx)), s.name)) > 0 {
		back := rustPathText(append([]string{"crate"}, toMod.path...)) + "::" + s.name
		fromOut = rustInsertUses(fromOut, []string{"use " + back + ";"})
	}

	writes := []fileWrite{{rel: toRel, content: toText}}
	for _, rel := range projectFiles(workdir) {
		if rel == fromRel || rel == toRel || extOf(rel) != ".rs" {
			continue
		}
		m, ok := rustModuleOf(rel)
		if !ok || m.crate != fromMod.crate {
			continue
		}
		src, ok := readProjectText(workdir, rel)
		if !ok || !strings.Contains(src, s.name) {
			continue
		}
		out := rustRewriteUses(src, rel, m, fromMod.path, toMod.path, s.name, &rep)
		if out != src {
			writes = append(writes, fileWrite{rel: rel, content: out})
			rep.importers = append(rep.importers, rel)
		}
		if rustBareUse(out, s.name) {
			rep.warnings = append(rep.warnings, fmt.Sprintf("%s uses %s without a use statement for it (a glob import or a full path). Check it by hand.", rel, s.name))
		}
	}
	if len(toMod.path) > 0 && !rustDeclared(workdir, toMod) {
		rep.warnings = append(rep.warnings, fmt.Sprintf("%s is not declared as a module yet. Add `mod %s;` to its parent module, or the crate will not compile it.", toRel, toMod.path[len(toMod.path)-1]))
	}
	writes = append(writes, fileWrite{rel: fromRel, content: fromOut})
	return writes, rep, nil
}

// symIdentSet is the set of identifiers in masked source.
func symIdentSet(masked string) map[string]bool {
	out := map[string]bool{}
	for _, id := range symIdentRe.FindAllString(masked, -1) {
		out[id] = true
	}
	return out
}

// removeRange deletes src[start:end] and its line break. When the item sat
// between two blank lines, one of them goes too, so no double gap is left.
func removeRange(src string, start, end int) string {
	if end < len(src) && src[end] == '\n' {
		end++
		if strings.HasSuffix(src[:start], "\n\n") && strings.HasPrefix(src[end:], "\n") {
			end++
		}
	}
	return src[:start] + src[end:]
}
