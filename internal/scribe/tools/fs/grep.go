package fs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const (
	maxGrepFileBytes = 256 * 1024
	maxGrepLineChars = 1000
	maxGrepOutput    = 32000
	// maxGrepPerFile is how many matching lines one file may contribute.
	maxGrepPerFile = 50
	grepTimeout    = 10 * time.Second
)

// grepEntry is one line of output: a match or a context line.
type grepEntry struct {
	line  int
	text  string
	match bool
}

// fileResult holds the output lines for one file.
type fileResult struct {
	rel     string
	entries []grepEntry
	matches int
}

// NewGrep returns grep. It uses ripgrep when rg is on PATH, else a pure-Go
// walker with the same rules.
func NewGrep() tools.Tool { return newGrep(false) }

func newGrep(forceGo bool) tools.Tool {
	return &tool{
		spec: newSpec("grep",
			"Search file contents with a regular expression (Go/RE2 syntax). Output is path:line:text. "+
				"Respects .gitignore, skips hidden and binary files and secret-looking files. "+
				"output_mode: content (default), files (paths only) or count.",
			tools.Schema(map[string]any{
				"pattern":          tools.Str("Regular expression to search for."),
				"path":             tools.Str("File or directory to search, relative to the project root. Default: the project root."),
				"glob":             tools.Str("Only search files whose name matches this glob, e.g. *.go."),
				"case_insensitive": tools.Bool("Match case-insensitively. Default false."),
				"context":          tools.Int("Lines of context before and after each match (0-20). Default 0."),
				"output_mode":      tools.Enum("What to print. Default content.", "content", "files", "count"),
				"max_results":      tools.Int("Maximum matching lines (content) or files (files, count). Default 200."),
			}, "pattern")),
		readOnly: true,
		run: func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
			return runGrep(ctx, env, args, forceGo)
		},
	}
}

func runGrep(ctx context.Context, env *tools.Env, args json.RawMessage, forceGo bool) tools.Result {
	var a struct {
		Pattern         string   `json:"pattern"`
		Path            string   `json:"path"`
		Glob            string   `json:"glob"`
		CaseInsensitive flexBool `json:"case_insensitive"`
		Context         flexInt  `json:"context"`
		OutputMode      string   `json:"output_mode"`
		MaxResults      flexInt  `json:"max_results"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return failTagged(err)
	}
	if a.Pattern == "" {
		return tools.Errorf("pattern is required.")
	}
	pat := a.Pattern
	if a.CaseInsensitive {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return tools.Errorf("invalid pattern %q: %v", a.Pattern, err)
	}
	mode := a.OutputMode
	if mode == "" {
		mode = "content"
	}
	if mode != "content" && mode != "files" && mode != "count" {
		return tools.Errorf("output_mode must be content, files or count, not %q.", mode)
	}
	max := int(a.MaxResults)
	if max <= 0 {
		max = 200
	}
	ctxN := int(a.Context)
	if ctxN < 0 {
		ctxN = 0
	}
	if ctxN > 20 {
		ctxN = 20
	}
	p := a.Path
	if p == "" {
		p = "."
	}
	t, err := resolve(ctx, env, "grep", p, accessSearch)
	if err != nil {
		return failTagged(err)
	}
	if _, err := statPath(t); err != nil {
		return failTagged(err)
	}
	gf, err := newGlobFilter(a.Glob)
	if err != nil {
		return failTagged(err)
	}

	runCtx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	var files []fileResult
	total, stopped := 0, false
	sink := func(fr fileResult) bool {
		files = append(files, fr)
		total += fr.matches
		if mode == "content" {
			stopped = total >= max
		} else {
			stopped = len(files) >= max
		}
		return stopped
	}

	// The code map's trigram index can rule out files that cannot contain a
	// literal run of the pattern. Ruling out is only ever a skip of a file
	// that would not match, so the answer is the same as without it.
	skip := trigramSkip(env, a.Pattern)
	var searchErr error
	usedRg := false
	if !forceGo {
		if rg, err := exec.LookPath("rg"); err == nil {
			usedRg = true
			targets := []string{t.abs}
			if skip != nil {
				if rels, ok := narrowedFiles(runCtx, env.Workdir, t.abs, gf, skip); ok {
					targets = rels
				}
			}
			if len(targets) > 0 {
				searchErr = rgSearch(runCtx, rg, env.Workdir, targets, pat, ctxN, a.Glob, sink)
			}
		}
	}
	if !usedRg {
		goSearch(runCtx, env.Workdir, t.abs, re, gf, ctxN, skip, sink)
	}
	if searchErr != nil {
		return tools.Errorf("grep failed: %v", searchErr)
	}
	return formatGrep(files, mode, ctxN, max, stopped, errors.Is(runCtx.Err(), context.DeadlineExceeded), total)
}

// formatGrep renders collected results in the requested mode.
func formatGrep(files []fileResult, mode string, ctxN, max int, stopped, timedOut bool, total int) tools.Result {
	var lines []string
	shown := 0
	truncated := false
	var lastRel string
	lastLine := 0
	started := false
	fileCount := 0
	for _, fr := range files {
		if fr.matches == 0 {
			continue
		}
		fileCount++
		switch mode {
		case "files":
			if shown >= max {
				truncated = true
				break
			}
			lines = append(lines, fr.rel)
			shown++
		case "count":
			if shown >= max {
				truncated = true
				break
			}
			lines = append(lines, fmt.Sprintf("%s:%d", fr.rel, fr.matches))
			shown++
		default:
			for _, e := range fr.entries {
				if e.match {
					if shown >= max {
						truncated = true
						break
					}
					shown++
				}
				if ctxN > 0 && started && (fr.rel != lastRel || e.line > lastLine+1) {
					lines = append(lines, "--")
				}
				sep := ":"
				if !e.match {
					sep = "-"
				}
				lines = append(lines, fmt.Sprintf("%s%s%d%s%s", fr.rel, sep, e.line, sep, e.text))
				lastRel, lastLine, started = fr.rel, e.line, true
			}
		}
		if truncated {
			break
		}
	}
	if len(lines) == 0 {
		msg := "No matches."
		if timedOut {
			msg = "No matches before the 10s search limit. Narrow the path or pattern."
		}
		return tools.Result{Output: msg, Summary: "0 matches"}
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxGrepOutput {
		cut := strings.LastIndex(out[:maxGrepOutput], "\n")
		if cut < 0 {
			cut = maxGrepOutput
		}
		out = out[:cut]
		truncated = true
	}
	switch {
	case timedOut:
		out += "\n(Search stopped at the 10s limit; results are partial. Narrow the path or pattern.)"
	case truncated || stopped:
		out += fmt.Sprintf("\n(Stopped at max_results=%d. Narrow the pattern or raise max_results.)", max)
	}
	summary := fmt.Sprintf("%d matches in %d files", total, fileCount)
	if mode == "files" {
		summary = fmt.Sprintf("%d files", fileCount)
	}
	return tools.Result{Output: out, Summary: summary}
}

// goSearch walks target and calls sink per file with matches. It skips what
// rg skips by default: hidden entries, binary files and files over 256KB.
func goSearch(ctx context.Context, wd, target string, re *regexp.Regexp, gf globFilter, ctxN int, skip trigramFilter, sink func(fileResult) bool) {
	fi, err := os.Stat(target)
	if err != nil {
		return
	}
	consider := func(abs, rel string, fi fs.FileInfo, base string) bool {
		if !grepWanted(fi, rel, base, gf) {
			return true
		}
		if skip != nil && skip(rel, fi.ModTime().UnixNano(), fi.Size()) {
			return true
		}
		fr, ok := scanFile(abs, rel, re, ctxN)
		if !ok {
			return true
		}
		return !sink(fr)
	}
	if !fi.IsDir() {
		rel := relSlash(wd, target)
		consider(target, rel, fi, "")
		return
	}
	baseRel := relSlash(wd, target)
	base := ""
	if baseRel != "." {
		base = baseRel + "/"
	}
	walkDir(ctx, wd, target, chainIgnore(wd, target), 1, -1, func(abs, rel string, fi fs.FileInfo) bool {
		if fi.IsDir() {
			return true
		}
		return consider(abs, rel, fi, base)
	})
}

// grepWanted reports whether a file takes part in a grep at all: a regular
// file under the size cap, not hidden, not secret-looking, and matching the
// glob. base is the search directory relative to the project root, so hidden
// means a dot-prefixed part below it.
func grepWanted(fi fs.FileInfo, rel, base string, gf globFilter) bool {
	if !fi.Mode().IsRegular() || fi.Size() > maxGrepFileBytes {
		return false
	}
	inBase := strings.TrimPrefix(rel, base)
	for _, part := range strings.Split(inBase, "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != "" {
			return false
		}
	}
	return !isSecretName(fi.Name()) && gf.match(inBase)
}

// scanFile searches one file. ok is false when it has no matches or is binary.
func scanFile(abs, rel string, re *regexp.Regexp, ctxN int) (fileResult, bool) {
	data, err := os.ReadFile(abs)
	if err != nil || isBinaryData(data) {
		return fileResult{}, false
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	isMatch := make([]bool, len(lines))
	n := 0
	for i, l := range lines {
		if re.MatchString(strings.TrimSuffix(l, "\r")) {
			isMatch[i] = true
			n++
			if n >= maxGrepPerFile {
				break // one file cannot fill the whole result
			}
		}
	}
	if n == 0 {
		return fileResult{}, false
	}
	want := make([]bool, len(lines))
	for i := range lines {
		if !isMatch[i] {
			continue
		}
		for j := max(0, i-ctxN); j <= min(len(lines)-1, i+ctxN); j++ {
			want[j] = true
		}
	}
	fr := fileResult{rel: rel, matches: n}
	for i, l := range lines {
		if want[i] {
			fr.entries = append(fr.entries, grepEntry{line: i + 1, text: clipLine(strings.TrimSuffix(l, "\r"), maxGrepLineChars), match: isMatch[i]})
		}
	}
	return fr, true
}

// secretGlobArgs turns secretGlobs into rg exclusion flags.
func secretGlobArgs() []string {
	var out []string
	for _, g := range secretGlobs {
		out = append(out, "--glob", "!"+g)
	}
	return out
}

// rgSearch runs ripgrep with JSON output and feeds sink one file at a time.
// It stops the process as soon as sink asks it to.
func rgSearch(ctx context.Context, rg, wd string, targets []string, pattern string, ctxN int, glob string, sink func(fileResult) bool) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"--json", "--no-config", "--no-require-git",
		"--max-filesize", "256K", "--max-columns", "1000", "--max-count", strconv.Itoa(maxGrepPerFile),
		"--glob", "!.git", "--glob", "!node_modules"}
	args = append(args, secretGlobArgs()...)
	if ctxN > 0 {
		args = append(args, "-C", strconv.Itoa(ctxN))
	}
	if glob != "" {
		args = append(args, "--glob", glob)
	}
	args = append(args, "-e", pattern, "--")
	args = append(args, targets...)

	cmd := exec.CommandContext(cctx, rg, args...)
	cmd.Dir = wd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var cur *fileResult
	stopped := false
scan:
	for sc.Scan() {
		var ev struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "begin":
			var d struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
			}
			if json.Unmarshal(ev.Data, &d) == nil {
				cur = &fileResult{rel: rgRel(wd, d.Path.Text)}
			}
		case "match", "context":
			if cur == nil {
				continue
			}
			var d struct {
				Lines struct {
					Text  string `json:"text"`
					Bytes string `json:"bytes"`
				} `json:"lines"`
				LineNumber int `json:"line_number"`
			}
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			text := d.Lines.Text
			if text == "" && d.Lines.Bytes != "" {
				if b, err := base64.StdEncoding.DecodeString(d.Lines.Bytes); err == nil {
					text = string(b)
				}
			}
			text = strings.TrimRight(text, "\r\n")
			isMatch := ev.Type == "match"
			if isMatch {
				cur.matches++
			}
			cur.entries = append(cur.entries, grepEntry{line: d.LineNumber, text: clipLine(text, maxGrepLineChars), match: isMatch})
		case "end":
			if cur != nil && cur.matches > 0 && sink(*cur) {
				stopped = true
				break scan
			}
			cur = nil
		}
	}
	if stopped {
		cancel()
	}
	werr := cmd.Wait()
	if stopped || ctx.Err() != nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(werr, &ee) && ee.ExitCode() == 2 {
		return errors.New(strings.TrimSpace(stderr.String()))
	}
	return nil
}

func rgRel(wd, p string) string {
	if filepath.IsAbs(p) {
		return relSlash(wd, p)
	}
	return path.Clean(filepath.ToSlash(p))
}
