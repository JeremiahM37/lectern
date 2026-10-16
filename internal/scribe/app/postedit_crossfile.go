package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
)

// Cross-file post-edit check: an edit to one file can break the files that
// import it. Before a single-file write runs, BeforeTool records the errors of
// those importers (the baseline). After the edit, AfterTool re-checks them and
// reports the errors that are new, attributed to the edit.
//
// The importers come from the code map. Only language servers that are
// already running are asked, so the check never pays a cold start. It is
// bounded by maxDependentsChecked files and by a time budget per phase.

const (
	// maxDependentsChecked caps the importers checked per edit.
	maxDependentsChecked = 10
	// crossFileBudget bounds the time spent on importers in each phase.
	crossFileBudget = 4 * time.Second
	// maxCrossLines caps the "Cross-file:" block, header included.
	maxCrossLines = 6
	// pendingTTL drops a pre-edit baseline whose edit never reached AfterTool.
	pendingTTL = 60 * time.Second
)

// dependentSource is the part of the code map the cross-file check needs.
// *repomap.Map satisfies it.
type dependentSource interface {
	Impact(file string) (repomap.Impact, error)
}

// pendingBaseline is the importers' errors taken before an edit ran.
type pendingBaseline struct {
	at   time.Time
	errs map[string][]lsp.FileDiagnostic
}

// importerSet is the importers chosen for one edit. total counts every
// importer found before the cap, so the caller can say how many were skipped.
type importerSet struct {
	paths []string
	total int
}

// writeTools are the tools whose "path" argument names the one file they
// change. Compound tools name their files differently and get no baseline.
var writeTools = map[string]bool{
	"write_file": true,
	"edit_file":  true,
	"multi_edit": true,
	"ast_edit":   true,
}

// SetDependents gives the post-edit pass the code map. Until it is set, no
// importers are checked. Safe to call from any goroutine.
func (p *postEditor) SetDependents(src dependentSource) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.deps = src
	p.mu.Unlock()
}

// BeforeTool is the pre-edit hook. For a single-file write it records the
// errors of the file's importers, so AfterTool can tell which ones the edit
// introduced. It does nothing for other tools, without a language server, or
// without a code map.
func (p *postEditor) BeforeTool(ctx context.Context, name string, args json.RawMessage) {
	if p == nil || p.diags == nil || !writeTools[name] {
		return
	}
	rel := pathArg(args)
	if rel == "" {
		return
	}
	abs := filepath.Clean(filepath.Join(p.workdir, filepath.FromSlash(rel)))
	if !insideDir(p.workdir, abs) {
		return
	}
	errs := p.snapshotImporters(ctx, []string{abs})
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, pb := range p.pending {
		if now.Sub(pb.at) > pendingTTL {
			delete(p.pending, k)
		}
	}
	// Always replace the entry for this file, even with nothing to record, so
	// a baseline left by an earlier call that never reached AfterTool (its
	// result named no files) cannot be used for this edit.
	if len(errs) == 0 {
		delete(p.pending, abs)
		return
	}
	p.pending[abs] = pendingBaseline{at: now, errs: errs}
}

// snapshotImporters returns the current errors of each running importer of
// the given files. A file with no entry was not checked (no server, error, or
// the time budget ran out).
func (p *postEditor) snapshotImporters(ctx context.Context, edited []string) map[string][]lsp.FileDiagnostic {
	imp := p.importers(edited)
	out := map[string][]lsp.FileDiagnostic{}
	bctx, cancel := context.WithTimeout(ctx, crossFileBudget)
	defer cancel()
	for _, d := range imp.paths {
		if bctx.Err() != nil {
			break
		}
		if !p.diags.Active(d) {
			continue
		}
		cur, err := p.diags.Diagnostics(bctx, d)
		if err != nil {
			continue
		}
		out[d] = errorsOnly(cur)
	}
	return out
}

// importers lists the direct importers of the edited files (the code map's
// level-1 dependents), without the edited files themselves, capped at
// maxDependentsChecked. The order is the code map's rank order.
func (p *postEditor) importers(edited []string) importerSet {
	p.mu.Lock()
	deps := p.deps
	p.mu.Unlock()
	if deps == nil {
		return importerSet{}
	}
	self := map[string]bool{}
	for _, e := range edited {
		self[e] = true
	}
	seen := map[string]bool{}
	var all []string
	for _, abs := range edited {
		im, err := deps.Impact(p.rel(abs))
		if err != nil {
			continue
		}
		for _, d := range im.Dependents {
			if d.Level != 1 {
				continue
			}
			path := filepath.Clean(filepath.Join(p.workdir, filepath.FromSlash(d.Path)))
			if self[path] || seen[path] || !insideDir(p.workdir, path) {
				continue
			}
			seen[path] = true
			all = append(all, path)
		}
	}
	out := importerSet{total: len(all), paths: all}
	if len(all) > maxDependentsChecked {
		out.paths = all[:maxDependentsChecked]
	}
	return out
}

// takePending returns and removes the baselines recorded for the files a tool
// reported. It runs on every AfterTool call, including failed ones, so a
// failed edit does not leave a baseline behind.
func (p *postEditor) takePending(files []string) map[string][]lsp.FileDiagnostic {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out map[string][]lsp.FileDiagnostic
	for _, f := range files {
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.workdir, f)
		}
		abs = filepath.Clean(abs)
		pb, ok := p.pending[abs]
		if !ok {
			continue
		}
		delete(p.pending, abs)
		if time.Since(pb.at) > pendingTTL {
			continue
		}
		if out == nil {
			out = map[string][]lsp.FileDiagnostic{}
		}
		for k, v := range pb.errs {
			out[k] = v
		}
	}
	return out
}

// crossFileBlock checks the importers of the edited files now and reports the
// errors that are new in them, plus how many went away. pre is the baseline
// taken before the edit. Where there is none, the importer's errors as of its
// last check are used. With neither, the importer's errors are only recorded.
//
// The block is empty when nothing is new or fixed. When there is something to
// report and some importers were not checked, it says how many.
func (p *postEditor) crossFileBlock(ctx context.Context, edited []string, pre map[string][]lsp.FileDiagnostic) string {
	if p.diags == nil {
		return ""
	}
	imp := p.importers(edited)
	if len(imp.paths) == 0 {
		return ""
	}
	var added []lsp.FileDiagnostic
	fixed, checked := 0, 0
	bctx, cancel := context.WithTimeout(ctx, crossFileBudget)
	defer cancel()
	for _, d := range imp.paths {
		if bctx.Err() != nil {
			break
		}
		if !p.diags.Active(d) {
			continue
		}
		cur, err := p.diags.Diagnostics(bctx, d)
		if err != nil {
			continue
		}
		errs := errorsOnly(cur)
		checked++
		p.mu.Lock()
		prev, known := p.depSeen[d]
		p.depSeen[d] = errs
		p.mu.Unlock()
		base, ok := pre[d]
		if !ok {
			base, ok = prev, known
		}
		if !ok {
			continue
		}
		added = append(added, newErrors(base, errs)...)
		fixed += fixedCount(base, errs)
	}
	if len(added) == 0 && fixed == 0 {
		return ""
	}
	return p.formatCrossFile(added, fixed, checked, imp.total)
}

// formatCrossFile renders the cross-file block, capped at maxCrossLines.
func (p *postEditor) formatCrossFile(added []lsp.FileDiagnostic, fixed, checked, total int) string {
	header := fmt.Sprintf("Cross-file: %d new error(s) in files that import the edit (%d checked).", len(added), checked)
	if fixed > 0 {
		header += fmt.Sprintf(" %d fixed.", fixed)
	}
	lines := []string{header}
	for i, d := range added {
		if len(lines) >= maxCrossLines-1 {
			lines = append(lines, fmt.Sprintf("  ... and %d more", len(added)-i))
			break
		}
		lines = append(lines, p.diagLine(d))
	}
	if skipped := total - checked; skipped > 0 {
		lines = append(lines, fmt.Sprintf("  Not checked: %d importer(s) beyond the cap of %d, or not served by a running language server, or past the time budget.", skipped, maxDependentsChecked))
	}
	return strings.Join(lines, "\n")
}

// diagLine renders one error as "  path:line:col error: message (source code)".
func (p *postEditor) diagLine(d lsp.FileDiagnostic) string {
	msg := strings.Join(strings.Fields(d.Message), " ")
	src := d.Source
	if d.Code != "" && d.Code != "<nil>" {
		src = strings.TrimSpace(src + " " + d.Code)
	}
	line := fmt.Sprintf("  %s:%d:%d error: %s", p.rel(d.Path), d.Line, d.Column, msg)
	if src != "" {
		line += " (" + src + ")"
	}
	if len(line) > maxDiagLineLen {
		line = line[:maxDiagLineLen-3] + "..."
	}
	return line
}

// pathArg reads the "path" field of a tool's arguments, or "" when absent.
func pathArg(args json.RawMessage) string {
	var a struct {
		Path string `json:"path"`
	}
	if len(args) == 0 || json.Unmarshal(args, &a) != nil {
		return ""
	}
	return a.Path
}
