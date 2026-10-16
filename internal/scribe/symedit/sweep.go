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
	sw := runSweep(env, workdir, oldName, newName, nil)
	writes, before, sites, files := sw.writes, sw.before, sw.sites, sw.files
	skipped, clashes, hidden := sw.skipped, sw.clashes, sw.hidden
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
		fmt.Fprintf(&b, "  %s\n", s)
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

// sweepState is what one text sweep found and would write.
type sweepState struct {
	writes  []fileWrite
	before  []string
	sites   []string // "rel:line: text", clipped
	files   []string
	skipped []string
	clashes []string
	hidden  int
}

// runSweep finds the whole-word code references to oldName outside skip (a set
// of project-relative paths) and builds the writes that rename them. It writes
// nothing; the caller commits.
func runSweep(env *tools.Env, workdir, oldName, newName string, skip map[string]bool) sweepState {
	var sw sweepState
	var sites []sweepSite
	for _, rel := range candidateFiles(env, workdir, oldName) {
		if skip[rel] {
			continue
		}
		src, ok := readProjectText(workdir, rel)
		if !ok || !strings.Contains(src, oldName) {
			continue
		}
		lang, ok := languages[extOf(rel)]
		if !ok {
			if len(wordHits(src, oldName)) > 0 {
				sw.skipped = append(sw.skipped, rel+" (no comment and string rules for this file type, not edited)")
			}
			continue
		}
		if isSecretName(path.Base(rel)) {
			if len(wordHits(src, oldName)) > 0 {
				sw.skipped = append(sw.skipped, rel+" (sensitive file name, not edited)")
			}
			continue
		}
		masked := mask(src, lang.lex)
		hits := wordHits(masked, oldName)
		sw.hidden += len(wordHits(src, oldName)) - len(hits)
		if len(hits) == 0 {
			continue
		}
		if len(wordHits(masked, newName)) > 0 {
			sw.clashes = append(sw.clashes, rel)
		}
		idx := newLineIndex(src)
		var b strings.Builder
		prev := 0
		for _, off := range hits {
			b.WriteString(src[prev:off])
			b.WriteString(newName)
			prev = off + len(oldName)
			ln := idx.lineOf(off)
			sites = append(sites, sweepSite{rel, ln, clip(strings.TrimSpace(idx.text(ln)), 160)})
		}
		b.WriteString(src[prev:])
		sw.writes = append(sw.writes, fileWrite{rel: rel, content: b.String()})
		sw.before = append(sw.before, src)
		sw.files = append(sw.files, rel)
	}
	for _, st := range sites {
		sw.sites = append(sw.sites, fmt.Sprintf("%s:%d: %s", st.rel, st.line, st.text))
	}
	return sw
}

type sweepSite struct {
	rel  string
	line int
	text string
}

// SweepRemaining runs the text sweep after a language server's rename, over
// the files the server did not change (skip holds their project-relative
// paths). It writes what it finds and returns the sites it changed, so the
// result can say what the server left behind. An empty result means no code
// references remained.
func SweepRemaining(env *tools.Env, workdir, oldName, newName string, skip map[string]bool) ([]string, []string, error) {
	if env == nil {
		return nil, nil, fmt.Errorf("rename: no environment")
	}
	sw := runSweep(env, workdir, oldName, newName, skip)
	if len(sw.writes) == 0 {
		return nil, nil, nil
	}
	if err := commitWrites(env, workdir, sw.writes); err != nil {
		return nil, nil, err
	}
	return sw.files, sw.sites, nil
}

// relOf makes a project path relative to dir, slash-separated.
func relOf(dir, target string) string {
	r, err := filepath.Rel(filepath.FromSlash(dir), filepath.FromSlash(target))
	if err != nil {
		return target
	}
	return filepath.ToSlash(r)
}
