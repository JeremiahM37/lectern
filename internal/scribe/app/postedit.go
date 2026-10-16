package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Post-edit pass: after a tool changes files, format just those files and
// tell the model which language-server errors its edit introduced. It runs
// from the agent's AfterTool hook, so the model sees the result in the same
// tool output it would have read anyway.

const (
	formatTimeout = 5 * time.Second
	diagTimeout   = 8 * time.Second
	// maxNewDiagLines caps the "New diagnostics:" block, header included.
	maxNewDiagLines = 10
	maxDiagLineLen  = 200
)

// diagSource is the part of the language-server manager the post-edit pass
// needs. *lsp.Manager satisfies it; tests use a fake.
type diagSource interface {
	// Active reports whether a server already serves abs. It must not start one.
	Active(abs string) bool
	Diagnostics(ctx context.Context, abs string) ([]lsp.FileDiagnostic, error)
}

// postEditor holds the per-file diagnostics cache. It is safe for concurrent
// use because the agent may run parallel tool calls.
type postEditor struct {
	workdir string
	diags   diagSource // nil when no language servers are available

	mu sync.Mutex
	// seen holds the error diagnostics of each file as of the last check.
	seen map[string][]lsp.FileDiagnostic
	// deps is the code map, for finding importers. Nil until SetDependents.
	deps dependentSource
	// depSeen holds the errors of each importer as of its last cross-file
	// check (see postedit_crossfile.go).
	depSeen map[string][]lsp.FileDiagnostic
	// pending holds importer baselines taken by BeforeTool, keyed by the
	// edited file's absolute path, until AfterTool consumes them.
	pending map[string]pendingBaseline
}

func newPostEditor(workdir string, diags diagSource) *postEditor {
	return &postEditor{
		workdir: workdir,
		diags:   diags,
		seen:    map[string][]lsp.FileDiagnostic{},
		depSeen: map[string][]lsp.FileDiagnostic{},
		pending: map[string]pendingBaseline{},
	}
}

// AfterTool is the agent's AfterTool hook. It only acts on successful
// results that name files, and never on undo_edit: undo has to restore the
// exact bytes the user had before, so reformatting them would be wrong.
func (p *postEditor) AfterTool(ctx context.Context, name string, _ json.RawMessage, res *tools.Result) {
	if p == nil || res == nil {
		return
	}
	// Consume any importer baseline for this call's files, even when the
	// call failed, so it cannot leak into a later edit.
	pre := p.takePending(res.Files)
	if res.IsError || len(res.Files) == 0 || name == "undo_edit" {
		return
	}
	files := p.existingFiles(res.Files)
	if len(files) == 0 {
		return
	}
	p.autoFix(ctx, name, files, res)
	var formatted []string
	for _, abs := range files {
		if p.format(ctx, abs) {
			formatted = append(formatted, p.rel(abs))
		}
	}
	if len(formatted) > 0 {
		res.Output = appendBlock(res.Output, "Formatted: "+strings.Join(formatted, ", ")+".")
	}
	if block := p.newDiagnostics(ctx, files, changedLines(res.Diff)); block != "" {
		res.Output = appendBlock(res.Output, block)
	}
	if block := p.crossFileBlock(ctx, files, pre); block != "" {
		res.Output = appendBlock(res.Output, block)
	}
	if block := p.cloneBlock(files); block != "" {
		res.Output = appendBlock(res.Output, block)
	}
	if block := symbolDeltas(res.Diff); block != "" {
		res.Output = appendBlock(res.Output, block)
	}
}

// existingFiles resolves the paths a tool reported to absolute paths inside
// the workdir that exist as regular files. Deleted files drop out here.
func (p *postEditor) existingFiles(rels []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range rels {
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.workdir, f)
		}
		abs = filepath.Clean(abs)
		if seen[abs] || !insideDir(p.workdir, abs) {
			continue
		}
		seen[abs] = true
		if fi, err := os.Stat(abs); err == nil && fi.Mode().IsRegular() {
			out = append(out, abs)
		}
	}
	return out
}

func (p *postEditor) rel(abs string) string {
	if r, err := filepath.Rel(p.workdir, abs); err == nil {
		return filepath.ToSlash(r)
	}
	return abs
}

// format runs the formatter for abs's language, if one is available, and
// reports whether it changed the file. A missing formatter is silent.
func (p *postEditor) format(ctx context.Context, abs string) bool {
	bin, argv, ok := formatterFor(p.workdir, abs)
	if !ok {
		return false
	}
	before, err := os.ReadFile(abs)
	if err != nil {
		return false
	}
	fctx, cancel := context.WithTimeout(ctx, formatTimeout)
	defer cancel()
	cmd := exec.CommandContext(fctx, bin, argv...)
	cmd.Dir = p.workdir
	cmd.WaitDelay = time.Second
	// The formatter writes the file itself. Its stdout is noise we discard;
	// a failure (for example a syntax error) leaves the file alone.
	_ = cmd.Run()
	after, err := os.ReadFile(abs)
	return err == nil && !bytes.Equal(before, after)
}

// formatterFor picks the formatter for abs's extension. Each candidate is
// used only when its binary is on PATH, or for prettier and rustfmt when the
// project has the matching local install or manifest.
func formatterFor(workdir, abs string) (string, []string, bool) {
	switch strings.ToLower(filepath.Ext(abs)) {
	case ".go":
		if bin, ok := lookPath("gofmt"); ok {
			return bin, []string{"-w", abs}, true
		}
	case ".py":
		if bin, ok := lookPath("ruff"); ok {
			return bin, []string{"format", "-q", abs}, true
		}
		if bin, ok := lookPath("black"); ok {
			return bin, []string{"-q", abs}, true
		}
	case ".rs":
		if fileExists(filepath.Join(workdir, "Cargo.toml")) {
			if bin, ok := lookPath("rustfmt"); ok {
				return bin, []string{abs}, true
			}
		}
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".json", ".css", ".scss", ".md", ".yaml", ".yml", ".html":
		// Only the project's own prettier: a global one may disagree with the
		// project's config, and a silent reformat there is worse than none.
		local := filepath.Join(workdir, "node_modules", ".bin", "prettier")
		if fileExists(local) {
			return local, []string{"--write", abs}, true
		}
	}
	return "", nil, false
}

// lookPath finds a formatter on PATH.
func lookPath(name string) (string, bool) {
	bin, err := exec.LookPath(name)
	return bin, err == nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// newDiagnostics fetches fresh diagnostics for each file a language server
// serves, compares its errors with the cached set from the last check, and
// returns a block listing only the errors that are new. Warnings are not
// reported. The cache is updated even when nothing is new.
//
// The first time a file is checked there is no earlier set to compare with,
// so only errors on or near the lines the edit changed are reported (taken
// from the tool's diff): errors elsewhere were there before the edit. With no
// diff for the file, every error counts.
func (p *postEditor) newDiagnostics(ctx context.Context, files []string, changed map[string][][2]int) string {
	if p.diags == nil {
		return ""
	}
	var added []lsp.FileDiagnostic
	fixed := 0
	for _, abs := range files {
		if !p.diags.Active(abs) {
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, diagTimeout)
		cur, err := p.diags.Diagnostics(dctx, abs)
		cancel()
		if err != nil {
			continue // best effort: keep the old cache, report nothing
		}
		errs := errorsOnly(cur)
		p.mu.Lock()
		prev, known := p.seen[abs]
		p.seen[abs] = errs
		p.mu.Unlock()
		fresh := newErrors(prev, errs)
		if !known {
			if spans, ok := changedFor(changed, p.rel(abs)); ok {
				fresh = nearSpans(fresh, spans)
			}
		} else {
			fixed += fixedCount(prev, errs)
		}
		added = append(added, fresh...)
	}
	if len(added) == 0 && fixed == 0 {
		return ""
	}
	var hints [][]string
	if len(added) > 0 {
		hints = p.quickFixHints(ctx, added)
	}
	return p.formatNewDiagnostics(added, hints, fixed)
}

// formatNewDiagnostics renders the block, capped at maxNewDiagLines lines. Each
// new error may be followed by up to maxHintTitles quick-fix hints. fixed is
// the number of errors that went away since the last check.
func (p *postEditor) formatNewDiagnostics(ds []lsp.FileDiagnostic, hints [][]string, fixed int) string {
	header := ""
	if len(ds) > 0 {
		header = fmt.Sprintf("New diagnostics: %d new error(s).", len(ds))
	}
	if fixed > 0 {
		if header != "" {
			header += " "
		}
		header += fmt.Sprintf("%d fixed since the last check.", fixed)
	}
	lines := []string{header}
	for i, d := range ds {
		if len(lines) >= maxNewDiagLines-1 {
			lines = append(lines, fmt.Sprintf("  ... and %d more", len(ds)-i))
			break
		}
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
		lines = append(lines, line)
		if i < len(hints) {
			for _, h := range hints[i] {
				if len(lines) >= maxNewDiagLines {
					break
				}
				lines = append(lines, "    fix: "+h)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func errorsOnly(ds []lsp.FileDiagnostic) []lsp.FileDiagnostic {
	var out []lsp.FileDiagnostic
	for _, d := range ds {
		if d.Severity == "error" {
			out = append(out, d)
		}
	}
	return out
}

// newErrors returns the errors in cur that prev does not already contain.
// Matching ignores line and column, because an edit above a diagnostic moves
// it without changing it. Each prev entry matches at most one cur entry, so
// a second copy of an existing error still counts as new.
func newErrors(prev, cur []lsp.FileDiagnostic) []lsp.FileDiagnostic {
	counts := map[string]int{}
	for _, d := range prev {
		counts[diagKey(d)]++
	}
	var out []lsp.FileDiagnostic
	for _, d := range cur {
		k := diagKey(d)
		if counts[k] > 0 {
			counts[k]--
			continue
		}
		out = append(out, d)
	}
	return out
}

func diagKey(d lsp.FileDiagnostic) string {
	return d.Severity + "\x00" + d.Source + "\x00" + d.Code + "\x00" + d.Message
}

// appendBlock adds a block to tool output, separated by a blank line.
func appendBlock(out, block string) string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return block
	}
	return out + "\n\n" + block
}

// insideDir reports whether p is root or below it.
func insideDir(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// changedLines reads the new-file line ranges of each file in a unified diff,
// keyed by the path after "+++ b/".
func changedLines(diff string) map[string][][2]int {
	out := map[string][][2]int{}
	cur := ""
	for _, ln := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(ln, "+++ "):
			cur = strings.TrimPrefix(strings.TrimPrefix(ln, "+++ "), "b/")
			if _, ok := out[cur]; !ok {
				out[cur] = nil
			}
		case strings.HasPrefix(ln, "@@ ") && cur != "":
			var os, oc, ns, nc int
			if _, err := fmt.Sscanf(ln, "@@ -%d,%d +%d,%d @@", &os, &oc, &ns, &nc); err == nil {
				out[cur] = append(out[cur], [2]int{ns, ns + max(nc, 1) - 1})
			}
		}
	}
	return out
}

// changedFor finds rel's ranges; diffs may name files relative to the workdir
// or by a longer path ending in rel.
func changedFor(changed map[string][][2]int, rel string) ([][2]int, bool) {
	if spans, ok := changed[rel]; ok {
		return spans, true
	}
	for k, spans := range changed {
		if strings.HasSuffix(k, "/"+rel) {
			return spans, true
		}
	}
	return nil, false
}

// nearSpans keeps the diagnostics within diagSlack lines of a changed range.
func nearSpans(ds []lsp.FileDiagnostic, spans [][2]int) []lsp.FileDiagnostic {
	const diagSlack = 3
	var out []lsp.FileDiagnostic
	for _, d := range ds {
		for _, s := range spans {
			if d.Line >= s[0]-diagSlack && d.Line <= s[1]+diagSlack {
				out = append(out, d)
				break
			}
		}
	}
	return out
}
