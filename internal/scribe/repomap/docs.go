package repomap

import (
	"bytes"
	"strings"
)

// attachDocSummaries sets Summary on each exported symbol that has a doc
// comment, for languages other than Go (Go uses the package doc). The comment
// is the block directly above the definition, skipping decorators and
// attributes. Python uses the docstring on the first lines of the body.
func attachDocSummaries(lang string, src []byte, fi *fileInfo) {
	if len(fi.Symbols) == 0 {
		return
	}
	lines := newLines(src)
	for i := range fi.Symbols {
		s := &fi.Symbols[i]
		if !s.Exported || s.Line < 1 || s.Line > lines.n() {
			continue
		}
		var doc string
		if lang == "python" {
			doc = pythonDocstring(lines, s.Line)
		}
		if doc == "" {
			doc = commentAbove(lines, s.Line)
		}
		if doc != "" {
			s.Summary = firstSentenceCut(doc, 100)
		}
	}
}

// lineView reads the lines of a source by offset, so a file is not split into
// a slice of lines. Only the few lines around each symbol are materialised.
type lineView struct {
	src    []byte
	starts []int
}

func newLines(src []byte) lineView {
	starts := make([]int, 1, bytes.Count(src, []byte{'\n'})+1)
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return lineView{src: src, starts: starts}
}

// n is the number of lines.
func (v lineView) n() int { return len(v.starts) }

// at returns line i (0-based) without its newline.
func (v lineView) at(i int) string {
	lo := v.starts[i]
	hi := len(v.src)
	if i+1 < len(v.starts) {
		hi = v.starts[i+1] - 1
	}
	return strings.TrimSuffix(string(v.src[lo:hi]), "\r")
}

// commentAbove returns the text of the comment block that ends on the line
// before line (1-based), or "" when there is none. Blank lines end the block.
// Decorators such as @Component or #[derive] between the comment and the
// definition are skipped.
func commentAbove(lines lineView, line int) string {
	var got []string
	for i := line - 2; i >= 0; i-- {
		t := strings.TrimSpace(lines.at(i))
		if t == "" {
			break
		}
		if isDecorator(t) {
			if len(got) > 0 {
				break
			}
			continue
		}
		text, ok := commentText(t)
		if !ok {
			break
		}
		if text != "" {
			got = append(got, text)
		}
	}
	// Collected bottom-up; a block reads top-down.
	for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 {
		got[i], got[j] = got[j], got[i]
	}
	return strings.TrimSpace(strings.Join(got, " "))
}

// commentText reports whether one trimmed line is a comment line and returns
// its text without the marker. Directive lines (such as "@ts-ignore" or
// "eslint-disable") are comments but carry no summary.
func commentText(t string) (string, bool) {
	switch {
	case strings.HasPrefix(t, "///"):
		t = strings.TrimPrefix(t, "///")
	case strings.HasPrefix(t, "//"):
		t = strings.TrimPrefix(t, "//")
	case strings.HasPrefix(t, "/**"):
		t = strings.TrimSuffix(strings.TrimPrefix(t, "/**"), "*/")
	case strings.HasPrefix(t, "/*"):
		t = strings.TrimSuffix(strings.TrimPrefix(t, "/*"), "*/")
	case strings.HasPrefix(t, "*/"):
		return "", true
	case strings.HasPrefix(t, "*"):
		t = strings.TrimPrefix(t, "*")
	case strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "#!") && !strings.HasPrefix(t, "#["):
		t = strings.TrimPrefix(t, "#")
	default:
		return "", false
	}
	t = strings.TrimSpace(strings.TrimSuffix(t, "*/"))
	if strings.HasPrefix(t, "@") || strings.HasPrefix(t, "eslint") || strings.HasPrefix(t, "nolint") || strings.HasPrefix(t, "TODO") {
		return "", true
	}
	return t, true
}

func isDecorator(t string) bool {
	return strings.HasPrefix(t, "@") || strings.HasPrefix(t, "#[")
}

// pythonDocstring returns the docstring of the def or class whose header is on
// line (1-based), or "". The docstring must start within the first few lines
// after the header, so a string literal further down the body is ignored.
func pythonDocstring(lines lineView, line int) string {
	header := line - 1
	// A multi-line signature ends at the first line that closes with a colon.
	end := header
	for end < lines.n() && end < header+8 && !strings.HasSuffix(strings.TrimSpace(lines.at(end)), ":") {
		end++
	}
	for i := end + 1; i < lines.n() && i <= end+2; i++ {
		t := strings.TrimSpace(lines.at(i))
		if t == "" {
			continue
		}
		for _, q := range []string{`"""`, `'''`} {
			if !strings.HasPrefix(t, q) {
				continue
			}
			body := strings.TrimPrefix(t, q)
			var parts []string
			if j := strings.Index(body, q); j >= 0 {
				return strings.TrimSpace(body[:j])
			}
			parts = append(parts, body)
			for k := i + 1; k < lines.n() && k < i+30; k++ {
				u := strings.TrimSpace(lines.at(k))
				if j := strings.Index(u, q); j >= 0 {
					parts = append(parts, u[:j])
					break
				}
				parts = append(parts, u)
			}
			return strings.TrimSpace(strings.Join(strings.Fields(strings.Join(parts, " ")), " "))
		}
		return ""
	}
	return ""
}

// firstSentenceCut is firstSentence with a caller-chosen length.
func firstSentenceCut(doc string, max int) string {
	s := strings.Join(strings.Fields(doc), " ")
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if len(s) > max {
		s = s[:max-3] + "..."
	}
	return s
}
