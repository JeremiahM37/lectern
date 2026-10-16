package fs

import (
	"context"
	"io/fs"
	"os"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// trigramFilter reports that a file is proven not to contain the searched
// literal. It is supplied by the code map (see repomap.Map.LiteralFilter).
type trigramFilter = func(rel string, mtime, size int64) bool

// literalIndex is what grep needs from the code map. repomap.Map satisfies it,
// and fs does not import repomap.
type literalIndex interface {
	LiteralFilter(lit string) (func(rel string, mtime, size int64) bool, bool)
}

// maxNarrowedFiles bounds the explicit file list passed to ripgrep. Beyond it,
// ripgrep walks the tree itself, which gives the same result.
const maxNarrowedFiles = 4000

// trigramSkip returns the code map's filter for pattern, or nil when there is
// no code map, no literal to narrow on, or the index cannot answer.
func trigramSkip(env *tools.Env, pattern string) trigramFilter {
	lit := requiredLiteral(pattern)
	if lit == "" {
		return nil
	}
	idx, ok := tools.Service[literalIndex](env, "repomap")
	if !ok {
		return nil
	}
	f, ok := idx.LiteralFilter(lit)
	if !ok {
		return nil
	}
	return f
}

// narrowedFiles lists the files under target that grep would search and that
// the trigram filter does not rule out, as paths relative to wd. ok is false
// when the list is too long to be worth passing on, in which case the caller
// searches the whole tree.
func narrowedFiles(ctx context.Context, wd, target string, gf globFilter, skip trigramFilter) ([]string, bool) {
	var rels []string
	consider := func(rel string, fi fs.FileInfo, base string) {
		if grepWanted(fi, rel, base, gf) && !skip(rel, fi.ModTime().UnixNano(), fi.Size()) {
			rels = append(rels, rel)
		}
	}
	fi, err := os.Stat(target)
	if err != nil {
		return nil, false
	}
	if !fi.IsDir() {
		consider(relSlash(wd, target), fi, "")
	} else {
		baseRel := relSlash(wd, target)
		base := ""
		if baseRel != "." {
			base = baseRel + "/"
		}
		walkDir(ctx, wd, target, chainIgnore(wd, target), 1, -1, func(abs, rel string, fi fs.FileInfo) bool {
			if !fi.IsDir() {
				consider(rel, fi, base)
			}
			return ctx.Err() == nil
		})
	}
	if ctx.Err() != nil || len(rels) > maxNarrowedFiles {
		return nil, false
	}
	return rels, true
}

// requiredLiteral returns the longest run of plain ASCII characters that every
// match of the RE2 pattern must contain, or "" when none is provable. It
// gives up on alternation, character classes it cannot skip, and escapes that
// take operands, and it never counts a run that sits inside a group or is made
// optional by a following quantifier.
func requiredLiteral(p string) string {
	if strings.Contains(p, "|") {
		return ""
	}
	var best, cur []byte
	depth := 0
	flush := func() {
		if depth == 0 && len(cur) > len(best) {
			best = append([]byte(nil), cur...)
		}
		cur = cur[:0]
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\':
			if i+1 >= len(p) {
				return ""
			}
			i++
			d := p[i]
			switch {
			case d >= 0x80:
				flush()
			case isAlnumByte(d):
				// Class escapes and anchors take no operand; others (\pN, \x41,
				// \Q, octal and back-references) make the scan unsafe.
				if !strings.ContainsRune("dDsSwWbBAzZ", rune(d)) {
					return ""
				}
				flush()
			default:
				cur = append(cur, d)
			}
		case c == '[':
			flush()
			end := classEnd(p, i)
			if end < 0 {
				return ""
			}
			i = end
		case c == '(':
			flush()
			depth++
		case c == ')':
			flush()
			if depth > 0 {
				depth--
			}
		case c == '{':
			end := strings.IndexByte(p[i:], '}')
			if end < 0 {
				return ""
			}
			if len(cur) > 0 {
				cur = cur[:len(cur)-1]
			}
			flush()
			i += end
		case c == '?' || c == '*' || c == '+':
			if len(cur) > 0 {
				cur = cur[:len(cur)-1]
			}
			flush()
		case c >= 0x80 || !plainByte(c):
			flush()
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return string(best)
}

// classEnd returns the index of the ']' that closes the class opened at p[i],
// or -1 when it is unterminated.
func classEnd(p string, i int) int {
	j := i + 1
	if j < len(p) && p[j] == '^' {
		j++
	}
	if j < len(p) && p[j] == ']' {
		j++
	}
	for j < len(p) {
		switch {
		case p[j] == '\\':
			j += 2
		case p[j] == ']':
			return j
		case strings.HasPrefix(p[j:], "[:"):
			k := strings.Index(p[j+2:], ":]")
			if k < 0 {
				return -1
			}
			j += 2 + k + 2
		default:
			j++
		}
	}
	return -1
}

func isAlnumByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// plainByte reports an ASCII byte that matches only itself outside a class.
func plainByte(c byte) bool {
	return !strings.ContainsRune(`\.+*?()|[]{}^$`, rune(c))
}
