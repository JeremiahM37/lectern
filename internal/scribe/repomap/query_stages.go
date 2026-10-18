package repomap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// queryResult is what a pipeline run produced.
type queryResult struct {
	Trace []string // one line per stage, in order
	Files []string // the working set after the last stage, most important first
	Body  string   // text from a terminal outline or read stage
}

// runQuery executes a validated pipeline against m.
//
// The working set starts as every indexed file. Each narrowing stage keeps the
// files that match and then holds the set to maxQueryFiles, most important
// first, so a broad stage can never flood the next one. deps is the one stage
// that moves the set: it replaces it with the files it imports or that import
// it. The set is always kept in importance order, which is what makes limit
// mean "the most important n".
func runQuery(ctx context.Context, m *Map, stages []queryStage) (queryResult, error) {
	var res queryResult
	set := m.allFiles()
	for i, st := range stages {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		step := fmt.Sprintf("%d. %s", i+1, describeStage(st))
		note := ""
		switch st.Op {
		case opOutline:
			res.Body = renderOutline(m, set)
		case opRead:
			res.Body = renderRead(ctx, m, set, st.Ranges)
		case opSearch:
			var err error
			set, note, err = m.searchStage(ctx, set, st.Pattern)
			if err != nil {
				return res, err
			}
		case opFind:
			set = m.findStage(set, st.Query)
		case opFilter:
			set = m.filterStage(set, st)
		case opDeps:
			set = m.depsStage(set, st.Direction)
		case opLimit:
			if st.N < len(set) {
				set = set[:st.N]
			}
		}
		if isTerminal(st.Op) {
			res.Trace = append(res.Trace, step)
			continue
		}
		if len(set) > maxQueryFiles {
			set = set[:maxQueryFiles]
			note = joinNotes(note, fmt.Sprintf("kept the %d most important", maxQueryFiles))
		}
		line := fmt.Sprintf("%s -> %d files", step, len(set))
		if note != "" {
			line += " (" + note + ")"
		}
		res.Trace = append(res.Trace, line)
	}
	res.Files = set
	return res, nil
}

// joinNotes joins two parenthesised remarks with a comma, skipping empties.
func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + ", " + b
}

// allFiles returns every indexed file in importance order.
func (m *Map) allFiles() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return rankInPlace(m, append([]string(nil), m.paths...))
}

// rankInPlace sorts paths by PageRank, most important first, with the path as
// the tie-breaker so the order is stable. Callers hold m.mu.
func rankInPlace(m *Map, paths []string) []string {
	sort.SliceStable(paths, func(i, j int) bool {
		ri, rj := m.fileRank(paths[i]), m.fileRank(paths[j])
		if ri != rj {
			return ri > rj
		}
		return paths[i] < paths[j]
	})
	return paths
}

// searchStage keeps the files of set that contain pattern as literal text.
// Matching ignores case. The trigram index rules out files that cannot
// contain the text, so most files are never opened. At most
// maxQuerySearchReads files are read; the set is in importance order, so the
// files that get read are the most important ones.
func (m *Map) searchStage(ctx context.Context, set []string, pattern string) ([]string, string, error) {
	lit := strings.ToLower(pattern)
	skip, _ := m.LiteralFilter(lit)
	var out []string
	reads := 0
	note := ""
	for _, p := range set {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		full := filepath.Join(m.root, filepath.FromSlash(p))
		st, err := os.Stat(full)
		if err != nil || !st.Mode().IsRegular() || st.Size() > maxQueryFileBytes {
			continue
		}
		if skip != nil && skip(p, st.ModTime().UnixNano(), st.Size()) {
			continue
		}
		if reads == maxQuerySearchReads {
			note = fmt.Sprintf("searched only the first %d candidates; add a filter to narrow it", maxQuerySearchReads)
			break
		}
		reads++
		src, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		if bytes.Contains(bytes.ToLower(src), []byte(lit)) {
			out = append(out, p)
		}
	}
	return m.orderedCopy(out), note, nil
}

// orderedCopy returns paths in importance order with duplicates removed.
func (m *Map) orderedCopy(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	uniq := paths[:0:0]
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			uniq = append(uniq, p)
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return rankInPlace(m, uniq)
}

// findStage keeps the files of set that hold a symbol, or are named, like
// query. It reuses the same fuzzy matching as find_symbol and find_file.
func (m *Map) findStage(set []string, query string) []string {
	in := make(map[string]bool, len(set))
	for _, p := range set {
		in[p] = true
	}
	var hits []string
	for _, h := range m.SearchSymbols(query, 1000) {
		if in[h.File] {
			hits = append(hits, h.File)
		}
	}
	for _, h := range m.FindFile(query, 1000) {
		if in[h.Path] {
			hits = append(hits, h.Path)
		}
	}
	return m.orderedCopy(hits)
}

// filterStage keeps the files of set that match every criterion given. Ext
// and path matching ignore case. Recent uses the same 48-hour window as the
// [NEW] tag in the Soul Map.
func (m *Map) filterStage(set []string, st queryStage) []string {
	ext := normalizeExt(st.Ext)
	sub := strings.ToLower(st.PathContains)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for _, p := range set {
		lp := strings.ToLower(p)
		if ext != "" && !strings.HasSuffix(lp, ext) {
			continue
		}
		if sub != "" && !strings.Contains(lp, sub) {
			continue
		}
		if st.Recent && !m.modifiedRecently(p) {
			continue
		}
		out = append(out, p)
	}
	return rankInPlace(m, out)
}

// modifiedRecently reports whether p changed inside newWindow. Callers hold
// m.mu.
func (m *Map) modifiedRecently(p string) bool {
	fi := m.files[p]
	return fi != nil && fi.Mtime > 0 && time.Since(time.Unix(0, fi.Mtime)) < newWindow
}

// depsStage replaces the set with the files that the files in it import
// (direction imports) or that import them (direction imported_by), using the
// same resolved import edges as code_impact.
func (m *Map) depsStage(set []string, direction string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for _, p := range set {
		if direction == "imports" {
			out = append(out, m.imports[p]...)
		} else {
			out = append(out, m.in[p]...)
		}
	}
	return rankInPlace(m, dedupe(out))
}

// dedupe drops repeated paths and keeps the first occurrence of each.
func dedupe(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := paths[:0]
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// renderOutline prints the symbols of each file in the set, in line order,
// nested under their parent type. It reads no file contents, so it is cheap.
func renderOutline(m *Map, files []string) string {
	var sb strings.Builder
	lines := 0
	for _, p := range files {
		if lines >= maxQueryLines {
			sb.WriteString("... more files not shown. Narrow the query with filter or limit.\n")
			break
		}
		syms, _ := m.FileSymbols(p)
		sb.WriteString(p + "\n")
		lines++
		if len(syms) == 0 {
			sb.WriteString("  (no symbols the map can extract)\n")
			lines++
			continue
		}
		for _, s := range syms {
			if lines >= maxQueryLines {
				sb.WriteString("... more symbols not shown.\n")
				break
			}
			indent := "  "
			if s.Parent != "" {
				indent += "    "
			}
			sig := s.Signature
			if sig == "" {
				sig = s.Kind + " " + s.Name
			}
			fmt.Fprintf(&sb, "%sL%d-%d  %s\n", indent, s.Line, s.EndLine, sig)
			lines++
		}
	}
	return sb.String()
}

// renderRead prints numbered lines from each file in the set. With no ranges
// it shows the first maxQueryReadLines lines. Binary files and files over
// maxQueryFileBytes are skipped with a note. Output stops once it reaches
// maxQueryOutput, so the total is bounded even for a wide set.
func renderRead(ctx context.Context, m *Map, files []string, ranges []lineRange) string {
	var sb strings.Builder
	for _, p := range files {
		if err := ctx.Err(); err != nil {
			break
		}
		if sb.Len() >= maxQueryOutput {
			sb.WriteString("... more files not shown. Narrow the query, or read a range of one file.\n")
			break
		}
		full := filepath.Join(m.root, filepath.FromSlash(p))
		st, err := os.Stat(full)
		if err != nil || !st.Mode().IsRegular() {
			fmt.Fprintf(&sb, "== %s: cannot be read\n", p)
			continue
		}
		if st.Size() > maxQueryFileBytes {
			fmt.Fprintf(&sb, "== %s: larger than %d KB; use the read tool for it\n", p, maxQueryFileBytes>>10)
			continue
		}
		src, err := os.ReadFile(full)
		if err != nil {
			fmt.Fprintf(&sb, "== %s: cannot be read\n", p)
			continue
		}
		if isBinary(src) {
			fmt.Fprintf(&sb, "== %s: binary, not shown\n", p)
			continue
		}
		writeFileLines(&sb, p, strings.Split(string(src), "\n"), ranges)
	}
	return sb.String()
}

// writeFileLines writes the requested spans of one file's lines, or its head
// when no span is given.
func writeFileLines(sb *strings.Builder, p string, lines []string, ranges []lineRange) {
	// A file that ends in a newline splits into a final empty element that is
	// not a line of its own.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	fmt.Fprintf(sb, "== %s (%d lines)\n", p, len(lines))
	if len(ranges) == 0 {
		ranges = []lineRange{{Start: 1, End: min(len(lines), maxQueryReadLines)}}
		if len(lines) > maxQueryReadLines {
			fmt.Fprintf(sb, "  showing lines 1-%d; pass ranges to read more\n", maxQueryReadLines)
		}
	}
	for _, r := range ranges {
		end := min(r.End, len(lines))
		if r.Start > end {
			fmt.Fprintf(sb, "  lines %d-%d are outside the file\n", r.Start, r.End)
			continue
		}
		for i := r.Start; i <= end; i++ {
			fmt.Fprintf(sb, "%6d  %s\n", i, strings.TrimRight(lines[i-1], "\r"))
		}
	}
}

// formatQuery renders a result as the text the model reads: the stage trace,
// then either the terminal output or the file list. The whole text is cut at
// maxQueryOutput with a note saying so.
func formatQuery(res queryResult) string {
	var sb strings.Builder
	sb.WriteString("Stages:\n")
	for _, line := range res.Trace {
		sb.WriteString("  " + line + "\n")
	}
	if res.Body != "" {
		sb.WriteString("\n" + res.Body)
	} else {
		sb.WriteString("Files:\n")
		for i, p := range res.Files {
			if i == maxQueryFiles {
				break
			}
			sb.WriteString("  " + p + "\n")
		}
		if len(res.Files) == 0 {
			sb.WriteString("  (none)\n")
		}
	}
	out := sb.String()
	if len(out) > maxQueryOutput {
		cut := strings.LastIndexByte(out[:maxQueryOutput], '\n')
		if cut < 0 {
			cut = maxQueryOutput
		}
		out = out[:cut+1] + "... output cut at 16 KB. Narrow the query with filter or limit.\n"
	}
	return out
}
