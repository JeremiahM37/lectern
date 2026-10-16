package repomap

import (
	"regexp"
	"strings"
)

// ignoreRule is one .gitignore line. base is the directory of the file it came
// from, relative to the repo root.
type ignoreRule struct {
	re      *regexp.Regexp
	base    string
	negate  bool
	dirOnly bool
}

// parseIgnore reads gitignore syntax: comments, blank lines, '!' negation,
// trailing '/' for directories, a leading or interior '/' to anchor, '*', '?',
// '[...]' classes and '**'.
func parseIgnore(base string, data []byte) []ignoreRule {
	var out []ignoreRule
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimRight(ln, " \r\t")
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		var r ignoreRule
		r.base = base
		if strings.HasPrefix(ln, "!") {
			r.negate = true
			ln = ln[1:]
		}
		if strings.HasSuffix(ln, "/") {
			r.dirOnly = true
			ln = strings.TrimSuffix(ln, "/")
		}
		anchored := strings.Contains(ln, "/")
		ln = strings.TrimPrefix(ln, "/")
		if ln == "" {
			continue
		}
		pat := globToRegex(ln)
		if anchored {
			pat = "^" + pat + "$"
		} else {
			pat = "(?:^|/)" + pat + "$"
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			continue
		}
		r.re = re
		out = append(out, r)
	}
	return out
}

func globToRegex(g string) string {
	var b strings.Builder
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				if i+2 < len(g) && g[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(g[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := g[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + class + "]")
			i += end + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// ignored reports whether rel is excluded. Later rules override earlier ones,
// as in git.
func ignored(rules []ignoreRule, rel string, isDir bool) bool {
	result := false
	for _, r := range rules {
		target := rel
		if r.base != "" {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			target = rel[len(r.base)+1:]
		}
		if r.dirOnly && !isDir {
			continue
		}
		if r.re.MatchString(target) {
			result = !r.negate
		}
	}
	return result
}
