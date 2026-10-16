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
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/exec"
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

// formatterTimeout bounds the project formatter run after a refactor (spec 06
// section 9.3: 10 s soft).
const formatterTimeout = 10 * time.Second

// autoFix runs the file-wide fixes on each file a compound tool changed. Where
// a language server is running, its organize-imports and fix-all run first.
// Then the project's formatter runs on the file, so a refactor leaves the file
// formatted even with no server up (the refactor's formatter fallback). The
// fixes' own diff is appended to the tool's diff, and any file the server
// touched beyond the tool's own list is added to Files.
func (p *postEditor) autoFix(ctx context.Context, name string, files []string, res *tools.Result) {
	if !compoundTools[name] {
		return
	}
	fixer, _ := p.diags.(sourceFixer) // nil when no language server is available
	var fixedFiles []string
	var diff strings.Builder
	inCall := map[string]bool{}
	for _, abs := range files {
		inCall[abs] = true
	}
	for _, abs := range files {
		before, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if fixer != nil && p.diags.Active(abs) {
			fctx, cancel := context.WithTimeout(ctx, autoFixTimeout)
			touched, err := fixer.SourceFix(fctx, abs)
			cancel()
			if err == nil {
				for _, t := range touched {
					if !inCall[t] && !containsStr(res.Files, p.rel(t)) {
						res.Files = append(res.Files, p.rel(t))
					}
				}
			}
		}
		fmtCtx, cancelFmt := context.WithTimeout(ctx, formatterTimeout)
		_ = exec.FormatFile(fmtCtx, p.workdir, abs, formatterTimeout) // no formatter: the file is left as it is
		cancelFmt()
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
	res.Output = appendBlock(res.Output, "Auto-fixed: applied fixes and formatting in "+strings.Join(fixedFiles, ", ")+".")
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
