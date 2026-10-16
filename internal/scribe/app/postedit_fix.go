package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
)

// Post-edit fixes beyond formatting: file-wide source fixes after compound
// edits, quick-fix hints for new errors, and definition-count deltas.

const (
	autoFixTimeout  = 5 * time.Second
	quickFixTimeout = 2 * time.Second
	maxHintedErrors = 3 // quick-fix lookups per check, to bound latency
	maxHintTitles   = 2
	maxHintTitleLen = 100
)

// compoundTools are the tools that change code across several places; after
// them the server's organize-imports and fix-all run on each changed file.
var compoundTools = map[string]bool{
	"rename_symbol": true,
	"move_symbol":   true,
	"rename_file":   true,
}

// sourceFixer is the part of the language-server manager that runs file-wide
// fixes. *lsp.Manager satisfies it.
type sourceFixer interface {
	SourceFix(ctx context.Context, abs string) ([]string, error)
}

// quickFixer lists the quick fixes for one diagnostic.
type quickFixer interface {
	QuickFixes(ctx context.Context, abs string, d lsp.FileDiagnostic) ([]lsp.CodeAction, error)
}

// autoFix runs the server's source fixes on each file a compound tool changed,
// but only where a language server is already running, so the tool never
// pays a cold start. The fix's own diff is appended to the tool's diff, and
// any file the fix touched beyond the tool's own list is added to Files.
func (p *postEditor) autoFix(ctx context.Context, name string, files []string, res *tools.Result) {
	if !compoundTools[name] || p.diags == nil {
		return
	}
	fixer, ok := p.diags.(sourceFixer)
	if !ok {
		return
	}
	var fixedFiles []string
	var diff strings.Builder
	inCall := map[string]bool{}
	for _, abs := range files {
		inCall[abs] = true
	}
	for _, abs := range files {
		if !p.diags.Active(abs) {
			continue
		}
		before, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		fctx, cancel := context.WithTimeout(ctx, autoFixTimeout)
		touched, err := fixer.SourceFix(fctx, abs)
		cancel()
		if err != nil {
			continue
		}
		for _, t := range touched {
			if !inCall[t] && !containsStr(res.Files, p.rel(t)) {
				res.Files = append(res.Files, p.rel(t))
			}
		}
		after, err := os.ReadFile(abs)
		if err != nil || bytes.Equal(before, after) {
			continue
		}
		d, _, _ := fs.UnifiedDiff(p.rel(abs), string(before), string(after))
		diff.WriteString(d)
		fixedFiles = append(fixedFiles, p.rel(abs))
	}
	if len(fixedFiles) == 0 {
		return
	}
	res.Output = appendBlock(res.Output, "Auto-fixed: organized imports and applied fixes in "+strings.Join(fixedFiles, ", ")+".")
	res.Diff += diff.String()
}

// quickFixHints returns up to maxHintTitles quick-fix titles for each of the
// first maxHintedErrors diagnostics, aligned with ds. Only servers that are
// running are asked.
func (p *postEditor) quickFixHints(ctx context.Context, ds []lsp.FileDiagnostic) [][]string {
	hints := make([][]string, len(ds))
	qf, ok := p.diags.(quickFixer)
	if !ok {
		return hints
	}
	for i, d := range ds {
		if i >= maxHintedErrors {
			break
		}
		if !p.diags.Active(d.Path) {
			continue
		}
		qctx, cancel := context.WithTimeout(ctx, quickFixTimeout)
		acts, err := qf.QuickFixes(qctx, d.Path, d)
		cancel()
		if err != nil {
			continue
		}
		for _, a := range acts {
			if !strings.HasPrefix(a.Kind, "quickfix") && a.Kind != "" {
				continue
			}
			hints[i] = append(hints[i], clipTitle(a.Title))
			if len(hints[i]) == maxHintTitles {
				break
			}
		}
	}
	return hints
}

func clipTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxHintTitleLen {
		return s[:maxHintTitleLen-3] + "..."
	}
	return s
}

// fixedCount is how many errors in prev are gone from cur.
func fixedCount(prev, cur []lsp.FileDiagnostic) int {
	return len(newErrors(cur, prev))
}

// definitionKinds classify diff lines for the definition-count summary. A line
// may fall in more than one kind (an exported function counts as both).
var definitionKinds = []struct {
	name string
	re   *regexp.Regexp
}{
	{"functions", regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:function\b|def\s|func\s|fn\s)`)},
	{"types", regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?(?:class|struct|interface|enum|trait|type)\s+\w`)},
	{"imports", regexp.MustCompile(`^\s*(?:import\b|from\s+\S+\s+import\b|use\s)`)},
	{"exports", regexp.MustCompile(`^\s*export\s`)},
}

// symbolDeltas counts, from a unified diff, how many functions, types, imports
// and exports the edit added or removed. It reads the diff only, so it is an
// approximation: a renamed definition shows as one removal and one addition.
func symbolDeltas(diff string) string {
	delta := make([]int, len(definitionKinds))
	for _, ln := range strings.Split(diff, "\n") {
		if strings.HasPrefix(ln, "+++") || strings.HasPrefix(ln, "---") || len(ln) < 2 {
			continue
		}
		var sign int
		switch ln[0] {
		case '+':
			sign = 1
		case '-':
			sign = -1
		default:
			continue
		}
		body := ln[1:]
		for i, k := range definitionKinds {
			if k.re.MatchString(body) {
				delta[i] += sign
			}
		}
	}
	var parts []string
	for i, k := range definitionKinds {
		if delta[i] != 0 {
			parts = append(parts, fmt.Sprintf("%s %+d", k.name, delta[i]))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Definitions changed (from the diff): " + strings.Join(parts, ", ") + "."
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
