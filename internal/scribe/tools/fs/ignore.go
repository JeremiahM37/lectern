package fs

import (
	"regexp"
	"strings"
)

// Ignore is a set of .gitignore rules. A rule from a nested .gitignore only
// applies below the directory that holds it. A nil *Ignore ignores nothing.
type Ignore struct {
	rules []ignoreRule
}

type ignoreRule struct {
	base     string // directory of the .gitignore, relative to the workdir; "" for the root
	re       *regexp.Regexp
	negate   bool
	dirOnly  bool
	anchored bool // pattern contains a slash, so it matches from base only
}

// Extend returns ig with the rules of data (a .gitignore that lives in base)
// appended. Later rules win, as in git. ig itself is not modified.
func (ig *Ignore) Extend(base string, data []byte) *Ignore {
	rules := parseIgnore(base, string(data))
	if len(rules) == 0 {
		return ig
	}
	out := &Ignore{}
	if ig != nil {
		out.rules = append(out.rules, ig.rules...)
	}
	out.rules = append(out.rules, rules...)
	return out
}

// Ignored reports whether the path rel (slash form, relative to the workdir)
// is ignored. A path under an ignored directory is ignored too.
func (ig *Ignore) Ignored(rel string, isDir bool) bool {
	if ig == nil || len(ig.rules) == 0 || rel == "" || rel == "." {
		return false
	}
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if ig.decide(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return ig.decide(rel, isDir)
}

func (ig *Ignore) decide(rel string, isDir bool) bool {
	ignored := false
	for _, r := range ig.rules {
		if r.matches(rel, isDir) {
			ignored = !r.negate
		}
	}
	return ignored
}

func (r ignoreRule) matches(rel string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	target := rel
	if r.base != "" {
		if !strings.HasPrefix(rel, r.base+"/") {
			return false
		}
		target = rel[len(r.base)+1:]
	}
	if !r.anchored {
		if i := strings.LastIndex(target, "/"); i >= 0 {
			target = target[i+1:]
		}
	}
	return r.re.MatchString(target)
}

// parseIgnore reads gitignore syntax: comments, blank lines, leading "!"
// negation, trailing "/" for directories only, and a slash anywhere (other
// than a trailing one) to anchor the pattern to base.
func parseIgnore(base, text string) []ignoreRule {
	var rules []ignoreRule
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(strings.TrimSuffix(line, "\r"), " ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{base: base}
		switch {
		case strings.HasPrefix(line, "!"):
			r.negate = true
			line = line[1:]
		case strings.HasPrefix(line, `\!`), strings.HasPrefix(line, `\#`):
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		r.anchored = strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		re, err := globRegexp(line)
		if err != nil {
			continue
		}
		r.re = re
		rules = append(rules, r)
	}
	return rules
}

// globRegexp compiles a glob into an anchored regexp. "*" and "?" do not
// cross "/"; "**/" matches zero or more directories; "**" matches anything.
// A bracket expression is passed through, with "!" meaning negation.
func globRegexp(pat string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	rs := []rune(pat)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch c {
		case '*':
			if i+1 < len(rs) && rs[i+1] == '*' {
				if i+2 < len(rs) && rs[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := -1
			for j := i + 2; j < len(rs); j++ {
				if rs[j] == ']' {
					end = j
					break
				}
			}
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := rs[i+1 : end]
			if len(class) > 0 && class[0] == '!' {
				b.WriteString("[^")
				class = class[1:]
			} else {
				b.WriteString("[")
			}
			for _, cc := range class {
				if cc == '\\' || cc == '^' || cc == '[' {
					b.WriteRune('\\')
				}
				b.WriteRune(cc)
			}
			b.WriteString("]")
			i = end
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
