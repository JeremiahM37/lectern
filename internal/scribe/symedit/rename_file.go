package symedit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
)

const renameFileName = "rename_file"

// renameReport is what a planned file move did, for the result text.
type renameReport struct {
	from, to  string
	importers []string
	warnings  []string
	language  string
}

type renameFileTool struct{}

func (t *renameFileTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: renameFileName,
		Description: "Move or rename a source file and rewrite the import paths that name it. TypeScript and JavaScript " +
			"rewrite relative specifiers in both directions. Python rewrites module names, including relative imports. " +
			"Go moves a file only within its own package (directory); a move to another package is refused. " +
			"Nothing is type-checked: run project typecheck afterwards.",
		Schema: tools.Schema(map[string]any{
			"from": tools.Str("Current path of the file, relative to the project root. Must be read first."),
			"to":   tools.Str("New path, relative to the project root. Must not exist yet."),
		}, "from", "to"),
	}
}

func (t *renameFileTool) ReadOnly() bool { return false }

func (t *renameFileTool) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) (res tools.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = tools.Errorf("%s failed unexpectedly: %v", renameFileName, r)
		}
	}()
	if env == nil {
		return tools.Errorf("%s: no environment", renameFileName)
	}
	var a struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := tools.Decode(raw, &a); err != nil {
		return fail(err)
	}
	if a.From == "" || a.To == "" {
		return tools.Errorf("from and to are both required")
	}
	fromAbs, fromRel, err := gate(ctx, env, a.From)
	if err != nil {
		return fail(err)
	}
	toAbs, toRel, err := gate(ctx, env, a.To)
	if err != nil {
		return fail(err)
	}
	if fromRel == toRel {
		return tools.Errorf("Source and destination are the same: %s.", fromRel)
	}
	if err := checkReadable(env, fromAbs, fromRel); err != nil {
		return fail(err)
	}
	if _, err := os.Stat(toAbs); err == nil {
		return tools.Errorf("Destination already exists: %s. Choose another name, or move the existing file first.", toRel)
	}
	fromLang, toLang := languages[extOf(fromRel)], languages[extOf(toRel)]
	if fromLang == nil || toLang == nil {
		return tools.Errorf("%s moves source files it can parse. Check the file types of %s and %s.", renameFileName, fromRel, toRel)
	}

	var (
		writes []fileWrite
		rep    renameReport
	)
	switch {
	case isTSFamily(fromLang) && isTSFamily(toLang):
		writes, rep, err = planRenameTS(env.Workdir, fromRel, toRel)
	case fromLang.name == "python" && toLang.name == "python":
		writes, rep, err = planRenamePy(env.Workdir, fromRel, toRel)
	case fromLang.name == "go" && toLang.name == "go":
		writes, rep, err = planRenameGo(env.Workdir, fromRel, toRel)
	default:
		return tools.Errorf("%s supports TypeScript/JavaScript, Python and Go, within one language. %s and %s do not match.", renameFileName, fromRel, toRel)
	}
	if err != nil {
		return tools.Errorf("%s", err.Error())
	}

	var diff strings.Builder
	var files []string
	for _, w := range writes {
		orig, _ := readProjectText(env.Workdir, w.rel)
		d, _, _ := fs.UnifiedDiff(w.rel, orig, w.content)
		diff.WriteString(d)
		files = append(files, w.rel)
	}
	if err := commitWrites(env, env.Workdir, writes); err != nil {
		return tools.Errorf("%s failed during write, and all changes were rolled back: %v", renameFileName, err)
	}
	return tools.Result{
		Output:  rep.text(),
		Summary: fmt.Sprintf("moved to %s; %d file(s) changed", toRel, len(files)),
		Diff:    diff.String(),
		Files:   files,
	}
}

func (r renameReport) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Moved %s to %s.\n", r.from, r.to)
	switch {
	case len(r.importers) > 0:
		fmt.Fprintf(&b, "Import paths updated in %d file(s): %s\n", len(r.importers), strings.Join(r.importers, ", "))
	case r.language == "go":
		b.WriteString("Same package, so no import changes were needed.\n")
	default:
		b.WriteString("No import updates needed.\n")
	}
	for _, w := range r.warnings {
		fmt.Fprintf(&b, "Check: %s\n", w)
	}
	b.WriteString("Not type-checked: run project typecheck or project build.")
	return b.String()
}

// --- TypeScript and JavaScript ------------------------------------------------

// planRenameTS rewrites every relative specifier that names fromRel, and the
// relative specifiers inside the moved file, so both still resolve.
func planRenameTS(workdir, fromRel, toRel string) ([]fileWrite, renameReport, error) {
	rep := renameReport{from: fromRel, to: toRel, language: "typescript"}
	fromSrc, ok := readProjectText(workdir, fromRel)
	if !ok {
		return nil, rep, fmt.Errorf("cannot read %s as text", fromRel)
	}
	fromDir, toDir := path.Dir(fromRel), path.Dir(toRel)
	key := tsModKey(fromRel)

	moved := rebaseSpecs(fromSrc, languages[extOf(fromRel)].lex, func(spec string) string {
		if !isRelSpec(spec) {
			return spec
		}
		return tsRespec(toDir, spec, path.Join(fromDir, spec))
	})

	var writes []fileWrite
	writes = append(writes, fileWrite{rel: toRel, content: moved})
	for _, rel := range projectFiles(workdir) {
		if rel == fromRel || !isTSFamily(languages[extOf(rel)]) {
			continue
		}
		src, ok := readProjectText(workdir, rel)
		if !ok {
			continue
		}
		var edits []textEdit
		for _, r := range codeSpecs(src, mask(src, languages[extOf(rel)].lex)) {
			if !isRelSpec(r.spec) {
				if k := tsModKey(r.spec); k == key || strings.HasSuffix(k, "/"+key) {
					rep.warnings = append(rep.warnings, fmt.Sprintf("%s imports %q by a non-relative path, which was not rewritten. Update it by hand if it means %s.", rel, r.spec, fromRel))
				}
				continue
			}
			if tsResolve(rel, r.spec) != key {
				continue
			}
			edits = append(edits, textEdit{r.start, r.end, tsRespec(path.Dir(rel), r.spec, toRel)})
		}
		if len(edits) == 0 {
			continue
		}
		writes = append(writes, fileWrite{rel: rel, content: applyEdits(src, edits)})
		rep.importers = append(rep.importers, rel)
	}
	writes = append(writes, fileWrite{rel: fromRel, remove: true})
	return writes, rep, nil
}

// --- Python -------------------------------------------------------------------

var (
	pyFromRe   = regexp.MustCompile(`(?m)^([ \t]*)from[ \t]+(\.*)([A-Za-z_][\w.]*)?([ \t]+import\b)`)
	pyImportRe = regexp.MustCompile(`(?m)^([ \t]*)import[ \t]+([^\n]+)`)
	pyNameRe   = regexp.MustCompile(`^[A-Za-z_][\w.]*`)
)

// pyModName is the dotted module name of a project path.
func pyModName(rel string) string {
	return strings.ReplaceAll(strings.TrimSuffix(rel, ".py"), "/", ".")
}

// pyPkg is the package (directory) a project path sits in, as name parts.
func pyPkg(rel string) []string {
	dir := path.Dir(rel)
	if dir == "." {
		return nil
	}
	return strings.Split(dir, "/")
}

// pyRelDotted writes the relative form of target for code in package pkg:
// one dot per package to climb, then the rest of the dotted name.
func pyRelDotted(pkg []string, target string) string {
	t := strings.Split(target, ".")
	c := 0
	for c < len(pkg) && c < len(t)-1 && pkg[c] == t[c] {
		c++
	}
	return strings.Repeat(".", len(pkg)-c+1) + strings.Join(t[c:], ".")
}

// planRenamePy rewrites the module names that import fromRel.
func planRenamePy(workdir, fromRel, toRel string) ([]fileWrite, renameReport, error) {
	rep := renameReport{from: fromRel, to: toRel, language: "python"}
	if path.Base(fromRel) == "__init__.py" || path.Base(toRel) == "__init__.py" {
		return nil, rep, fmt.Errorf("%s does not move package __init__ files: the package they define would change name. Move the package's other modules instead.", renameFileName)
	}
	fromMod, toMod := pyModName(fromRel), pyModName(toRel)
	base := strings.TrimSuffix(path.Base(fromRel), ".py")
	var writes []fileWrite
	for _, rel := range projectFiles(workdir) {
		if extOf(rel) != ".py" {
			continue
		}
		src, ok := readProjectText(workdir, rel)
		if !ok {
			continue
		}
		oldPkg, newPkg := pyPkg(rel), pyPkg(rel)
		if rel == fromRel {
			newPkg = pyPkg(toRel)
		}
		out := rewritePy(src, rel, fromRel, oldPkg, newPkg, fromMod, toMod, base, &rep)
		if rel == fromRel {
			writes = append([]fileWrite{{rel: toRel, content: out}}, writes...)
			continue
		}
		if out != src {
			writes = append(writes, fileWrite{rel: rel, content: out})
			rep.importers = append(rep.importers, rel)
		}
	}
	writes = append(writes, fileWrite{rel: fromRel, remove: true})
	return writes, rep, nil
}

// rewritePy returns src with the import statements that name the moved
// module changed. oldPkg is the package src sat in and newPkg the package it
// will sit in; for the moved file itself the two differ.
func rewritePy(src, rel, fromRel string, oldPkg, newPkg []string, fromMod, toMod, base string, rep *renameReport) string {
	masked := mask(src, languages[".py"].lex)
	var edits []textEdit
	moving := rel == fromRel
	for _, m := range pyFromRe.FindAllStringSubmatchIndex(src, -1) {
		kw := m[0] + strings.Index(src[m[0]:m[1]], "from")
		if masked[kw] != src[kw] {
			continue
		}
		dots := src[m[4]:m[5]]
		mod := ""
		if m[6] >= 0 {
			mod = src[m[6]:m[7]]
		}
		if len(dots) == 0 {
			if mod != fromMod {
				continue
			}
			edits = append(edits, textEdit{m[6], m[7], toMod})
			continue
		}
		up := len(dots) - 1
		if up > len(oldPkg) {
			continue
		}
		abs := strings.Join(append(append([]string{}, oldPkg[:len(oldPkg)-up]...), splitDots(mod)...), ".")
		target := abs
		if abs == fromMod {
			target = toMod
		}
		if mod == "" {
			names := src[m[1]:]
			if i := strings.IndexByte(names, '\n'); i >= 0 {
				names = names[:i]
			}
			if strings.Contains(" "+names+" ", " "+base+" ") {
				rep.warnings = append(rep.warnings, fmt.Sprintf("%s imports %s with 'from %s import %s'. Check that it names the moved module.", rel, fromRel, dots, base))
			}
			continue
		}
		if !moving && target == abs {
			continue
		}
		edits = append(edits, textEdit{m[4], m[7], pyRelDotted(newPkg, target)})
	}
	for _, m := range pyImportRe.FindAllStringSubmatchIndex(src, -1) {
		kw := m[0] + strings.Index(src[m[0]:m[1]], "import")
		if masked[kw] != src[kw] {
			continue
		}
		items := src[m[4]:m[5]]
		body, tail := items, ""
		if i := strings.IndexByte(items, '#'); i >= 0 {
			body, tail = items[:i], items[i:]
		}
		parts := strings.Split(body, ",")
		changed := false
		for i, p := range parts {
			lead := len(p) - len(strings.TrimLeft(p, " \t"))
			t := p[lead:]
			name := pyNameRe.FindString(t)
			if name != fromMod {
				continue
			}
			parts[i] = p[:lead] + toMod + t[len(name):]
			changed = true
			if !strings.Contains(t, " as ") {
				rep.warnings = append(rep.warnings, fmt.Sprintf("%s imports %s without 'as'. Code that uses %s.… must change to %s by hand.", rel, fromMod, fromMod, toMod))
			}
		}
		if changed {
			edits = append(edits, textEdit{m[4], m[5], strings.Join(parts, ",") + tail})
		}
	}
	return applyEdits(src, edits)
}

// splitDots splits a dotted module name, ignoring an empty one.
func splitDots(mod string) []string {
	if mod == "" {
		return nil
	}
	return strings.Split(mod, ".")
}

// --- Go -----------------------------------------------------------------------

// planRenameGo moves a Go file within its package. Every file in a directory
// is one package, so nothing else changes.
func planRenameGo(workdir, fromRel, toRel string) ([]fileWrite, renameReport, error) {
	rep := renameReport{from: fromRel, to: toRel, language: "go"}
	if path.Dir(fromRel) != path.Dir(toRel) {
		return nil, rep, fmt.Errorf("%s cannot move a Go file to another package (%s is in %s, %s is in %s). Moving it changes which package it belongs to and breaks its callers. Move it within its directory, or move the declarations with move_symbol.",
			renameFileName, fromRel, path.Dir(fromRel), toRel, path.Dir(toRel))
	}
	src, ok := readProjectText(workdir, fromRel)
	if !ok {
		return nil, rep, fmt.Errorf("cannot read %s as text", fromRel)
	}
	return []fileWrite{{rel: toRel, content: src}, {rel: fromRel, remove: true}}, rep, nil
}
