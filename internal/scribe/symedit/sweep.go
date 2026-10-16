package symedit

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
)

// maxSweepLines caps how many changed sites the result lists.
const maxSweepLines = 200

// TextRename renames the identifier oldName to newName without a language
// server. It visits the files that can hold a reference (the code map's
// references when a map is open, else every project file) and changes only
// whole-word occurrences in code. Comments and string literals are left
// alone, using the lexer ast_edit uses. Nothing here is resolved by a
// compiler, so the result labels every change as text-only and lists the
// sites.
func TextRename(env *tools.Env, workdir, oldName, newName string) tools.Result {
	if !identRe.MatchString(newName) {
		return tools.Errorf("new_name must be a single identifier")
	}
	if oldName == "" || oldName == newName {
		return tools.Errorf("rename needs the current name, and it must differ from new_name")
	}
	type site struct {
		rel  string
		line int
		text string
	}
	var (
		writes  []fileWrite
		before  []string
		sites   []site
		skipped []string
		clashes []string
		hidden  int
		files   []string
	)
	for _, rel := range candidateFiles(env, workdir, oldName) {
		src, ok := readProjectText(workdir, rel)
		if !ok || !strings.Contains(src, oldName) {
			continue
		}
		lang, ok := languages[extOf(rel)]
		if !ok {
			if len(wordHits(src, oldName)) > 0 {
				skipped = append(skipped, rel+" (no comment and string rules for this file type, not edited)")
			}
			continue
		}
		if isSecretName(path.Base(rel)) {
			if len(wordHits(src, oldName)) > 0 {
				skipped = append(skipped, rel+" (sensitive file name, not edited)")
			}
			continue
		}
		masked := mask(src, lang.lex)
		hits := wordHits(masked, oldName)
		hidden += len(wordHits(src, oldName)) - len(hits)
		if len(hits) == 0 {
			continue
		}
		if len(wordHits(masked, newName)) > 0 {
			clashes = append(clashes, rel)
		}
		idx := newLineIndex(src)
		var b strings.Builder
		prev := 0
		for _, off := range hits {
			b.WriteString(src[prev:off])
			b.WriteString(newName)
			prev = off + len(oldName)
			ln := idx.lineOf(off)
			sites = append(sites, site{rel, ln, clip(strings.TrimSpace(idx.text(ln)), 160)})
		}
		b.WriteString(src[prev:])
		writes = append(writes, fileWrite{rel: rel, content: b.String()})
		before = append(before, src)
		files = append(files, rel)
	}
	if len(writes) == 0 {
		msg := fmt.Sprintf("Could not rename '%s': no code references were found in any source file. Comments and strings are never renamed.", oldName)
		if hidden > 0 {
			msg += fmt.Sprintf(" (%d occurrence(s) sit in comments or strings.)", hidden)
		}
		return tools.Errorf("%s", msg)
	}
	if env == nil {
		return tools.Errorf("rename: no environment")
	}
	if err := commitWrites(env, workdir, writes); err != nil {
		return tools.Errorf("the rename was not applied, and all files were restored: %v", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Renamed '%s' to '%s' in %d file(s) (text-only: no language server):\n", oldName, newName, len(files))
	for _, f := range files {
		fmt.Fprintf(&b, "  %s\n", f)
	}
	b.WriteString("\nText-only sites (matched by name in code, not resolved by a language server):\n")
	for i, s := range sites {
		if i == maxSweepLines {
			fmt.Fprintf(&b, "  ... %d more not shown\n", len(sites)-maxSweepLines)
			break
		}
		fmt.Fprintf(&b, "  %s:%d: %s\n", s.rel, s.line, s.text)
	}
	b.WriteString("\nA different symbol that shares the name would have changed too. Run project test to check.\n")
	if hidden > 0 {
		fmt.Fprintf(&b, "Left unchanged: %d occurrence(s) in comments and strings.\n", hidden)
	}
	for _, s := range skipped {
		fmt.Fprintf(&b, "Not swept: %s\n", s)
	}
	for _, c := range clashes {
		fmt.Fprintf(&b, "Check: %s already contains '%s'; the sweep cannot see a clash.\n", c, newName)
	}
	var diff strings.Builder
	for i, w := range writes {
		d, _, _ := fs.UnifiedDiff(w.rel, before[i], w.content)
		diff.WriteString(d)
	}
	return tools.Result{
		Output:  strings.TrimRight(b.String(), "\n"),
		Summary: fmt.Sprintf("text-renamed in %d file(s)", len(files)),
		Diff:    diff.String(),
		Files:   files,
	}
}

// relOf makes a project path relative to dir, slash-separated.
func relOf(dir, target string) string {
	r, err := filepath.Rel(filepath.FromSlash(dir), filepath.FromSlash(target))
	if err != nil {
		return target
	}
	return filepath.ToSlash(r)
}
