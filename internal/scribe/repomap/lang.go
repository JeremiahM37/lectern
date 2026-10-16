package repomap

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// lexConfig describes how a language writes comments and strings. mask uses
// it to blank everything that is not code, so the definition regexes and the
// brace/indent scanners never see a "}" inside a string or a "func" inside a
// comment.
type lexConfig struct {
	lineComments []string
	blockOpen    string
	blockClose   string
	nested       bool
	quotes       string
	charLits     bool // ' may be a char literal (Rust, Go, C) rather than a lifetime
	triple       bool // """ strings
	rawRust      bool // r#"..."# strings
	longBracket  bool // [[...]] strings (Lua)
	hashSpace    bool // '#' starts a comment only after whitespace (shell)
}

// mask returns a copy of src with comments blanked and string contents blanked
// (the quote characters themselves are kept). Newlines are never touched, so
// line numbers and byte offsets in the masked text match src. With
// keepStrings, string contents are kept; import extraction needs that.
func mask(src []byte, lx *lexConfig, keepStrings bool) []byte {
	out := make([]byte, len(src))
	copy(out, src)
	n := len(src)
	blank := func(a, b int) {
		for k := a; k < b && k < n; k++ {
			if out[k] != '\n' && out[k] != '\r' {
				out[k] = ' '
			}
		}
	}
	i := 0
	for i < n {
		c := src[i]
		if lx.blockOpen != "" && hasAt(src, i, lx.blockOpen) {
			start := i
			i += len(lx.blockOpen)
			depth := 1
			for i < n {
				if lx.nested && hasAt(src, i, lx.blockOpen) {
					depth++
					i += len(lx.blockOpen)
					continue
				}
				if hasAt(src, i, lx.blockClose) {
					depth--
					i += len(lx.blockClose)
					if depth == 0 {
						break
					}
					continue
				}
				i++
			}
			blank(start, i)
			continue
		}
		if isLineComment(src, i, lx) {
			start := i
			for i < n && src[i] != '\n' {
				i++
			}
			blank(start, i)
			continue
		}
		if lx.rawRust && c == 'r' && (i == 0 || !isIdentPart(src[i-1])) {
			j := i + 1
			for j < n && src[j] == '#' {
				j++
			}
			if j < n && src[j] == '"' {
				closer := "\"" + strings.Repeat("#", j-(i+1))
				k := j + 1
				cend, end := n, n
				if idx := bytes.Index(src[k:], []byte(closer)); idx >= 0 {
					cend = k + idx
					end = cend + len(closer)
				}
				if !keepStrings {
					blank(k, cend)
				}
				i = end
				continue
			}
		}
		if lx.longBracket && c == '[' && i+1 < n && src[i+1] == '[' {
			k := i + 2
			cend, end := n, n
			if idx := bytes.Index(src[k:], []byte("]]")); idx >= 0 {
				cend = k + idx
				end = cend + 2
			}
			if !keepStrings {
				blank(k, cend)
			}
			i = end
			continue
		}
		if strings.IndexByte(lx.quotes, c) >= 0 {
			if c == '\'' && lx.charLits && !isCharLit(src, i) {
				i++ // a lifetime, not a string
				continue
			}
			if lx.triple && hasAt(src, i, strings.Repeat(string(c), 3)) {
				q := strings.Repeat(string(c), 3)
				k := i + 3
				cend, end := n, n
				if idx := bytes.Index(src[k:], []byte(q)); idx >= 0 {
					cend = k + idx
					end = cend + 3
				}
				if !keepStrings {
					blank(k, cend)
				}
				i = end
				continue
			}
			j := i + 1
			for j < n {
				if src[j] == '\\' && c != '`' {
					j += 2
					continue
				}
				if src[j] == c {
					break
				}
				if src[j] == '\n' && c != '`' {
					break // unterminated single-line string
				}
				j++
			}
			if j > n {
				j = n
			}
			if !keepStrings {
				blank(i+1, j)
			}
			i = j + 1
			continue
		}
		i++
	}
	return out
}

func hasAt(src []byte, i int, s string) bool {
	return s != "" && i >= 0 && i+len(s) <= len(src) && string(src[i:i+len(s)]) == s
}

func isLineComment(src []byte, i int, lx *lexConfig) bool {
	for _, lc := range lx.lineComments {
		if !hasAt(src, i, lc) {
			continue
		}
		if lc == "#" && lx.hashSpace && i > 0 && !isSpaceByte(src[i-1]) {
			continue
		}
		return true
	}
	return false
}

// isCharLit reports whether the ' at src[i] opens a character literal.
func isCharLit(src []byte, i int) bool {
	if i+1 >= len(src) {
		return false
	}
	if src[i+1] == '\\' {
		return true
	}
	_, sz := utf8.DecodeRune(src[i+1:])
	j := i + 1 + sz
	return j < len(src) && src[j] == '\''
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// language is one supported source language.
type language struct {
	name        string
	lex         lexConfig
	end         endMode
	kw          *kwConfig
	newlineEnds bool // a newline at paren depth 0 ends a bodyless declaration
	rules       []defRule
	exported    func(sig, name, kind string) bool
	imports     []importRule
}

var extToLang = map[string]string{}
var languages = map[string]*language{}

func init() {
	add := func(name string, exts string, lg *language) {
		lg.name = name
		languages[name] = lg
		for _, e := range strings.Fields(exts) {
			extToLang[e] = name
		}
	}
	jsLex := lexConfig{lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'`"}
	add("typescript", ".ts .tsx .mts .cts", &language{lex: jsLex, end: endBrace, newlineEnds: true, rules: tsRules, exported: tsExported, imports: jsImports})
	add("javascript", ".js .jsx .mjs .cjs", &language{lex: jsLex, end: endBrace, newlineEnds: true, rules: tsRules, exported: tsExported, imports: jsImports})
	add("python", ".py .pyi", &language{
		lex:      lexConfig{lineComments: []string{"#"}, quotes: "\"'", triple: true},
		end:      endIndent,
		rules:    pyRules,
		exported: func(sig, name, kind string) bool { return !strings.HasPrefix(name, "_") },
		imports:  pyImports,
	})
	add("rust", ".rs", &language{
		lex:      lexConfig{lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/", nested: true, quotes: "\"'", charLits: true, rawRust: true},
		end:      endBrace,
		rules:    rustRules,
		exported: func(sig, name, kind string) bool { return strings.HasPrefix(sig, "pub") },
		imports:  rustImports,
	})
	add("java", ".java", &language{
		lex:      lexConfig{lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'", triple: true},
		end:      endBrace,
		rules:    javaRules,
		exported: func(sig, name, kind string) bool { return strings.Contains(sig, "public") },
		imports:  jvmImports,
	})
	add("kotlin", ".kt .kts", &language{
		lex:         lexConfig{lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/", nested: true, quotes: "\"'", triple: true},
		end:         endBrace,
		newlineEnds: true,
		rules:       kotlinRules,
		exported:    func(sig, name, kind string) bool { return !strings.Contains(sig, "private") },
		imports:     jvmImports,
	})
	add("csharp", ".cs", &language{
		lex:      lexConfig{lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'"},
		end:      endBrace,
		rules:    csharpRules,
		exported: func(sig, name, kind string) bool { return strings.Contains(sig, "public") },
	})
	add("swift", ".swift", &language{
		lex:         lexConfig{lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/", nested: true, quotes: "\"", triple: true},
		end:         endBrace,
		newlineEnds: true,
		rules:       swiftRules,
		exported: func(sig, name, kind string) bool {
			return strings.Contains(sig, "public") || strings.Contains(sig, "open ")
		},
	})
	cLex := lexConfig{lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'", charLits: true}
	add("c", ".c .h", &language{lex: cLex, end: endBrace, rules: cRules, exported: cExported, imports: cImports})
	add("cpp", ".cc .cpp .cxx .hpp .hh .hxx", &language{lex: cLex, end: endBrace, rules: cRules, exported: cExported, imports: cImports})
	add("ruby", ".rb", &language{
		lex:      lexConfig{lineComments: []string{"#"}, quotes: "\"'"},
		end:      endKeyword,
		kw:       &kwConfig{always: words("def class module do begin case"), lineStart: words("if unless while until for"), closers: words("end"), endlessDef: true},
		rules:    rubyRules,
		exported: func(sig, name, kind string) bool { return !strings.HasPrefix(name, "_") },
		imports:  rubyImports,
	})
	add("php", ".php", &language{
		lex:   lexConfig{lineComments: []string{"//", "#"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'"},
		end:   endBrace,
		rules: phpRules,
		exported: func(sig, name, kind string) bool {
			return !strings.Contains(sig, "private") && !strings.Contains(sig, "protected")
		},
		imports: phpImports,
	})
	add("lua", ".lua", &language{
		lex:      lexConfig{lineComments: []string{"--"}, blockOpen: "--[[", blockClose: "]]", quotes: "\"'", longBracket: true},
		end:      endKeyword,
		kw:       &kwConfig{always: words("function if do"), closers: words("end")},
		rules:    luaRules,
		exported: func(sig, name, kind string) bool { return !strings.HasPrefix(sig, "local") },
		imports:  luaImports,
	})
	add("shell", ".sh .bash .zsh", &language{
		lex:      lexConfig{lineComments: []string{"#"}, quotes: "\"'", hashSpace: true},
		end:      endBrace,
		rules:    shellRules,
		exported: func(sig, name, kind string) bool { return true },
		imports:  shellImports,
	})
	add("zig", ".zig", &language{
		lex:      lexConfig{lineComments: []string{"//"}, quotes: "\"'", charLits: true},
		end:      endBrace,
		rules:    zigRules,
		exported: func(sig, name, kind string) bool { return strings.HasPrefix(sig, "pub") },
		imports:  zigImports,
	})
	add("elixir", ".ex .exs", &language{
		lex:      lexConfig{lineComments: []string{"#"}, quotes: "\"'"},
		end:      endKeyword,
		kw:       &kwConfig{always: words("do fn"), closers: words("end")},
		rules:    elixirRules,
		exported: func(sig, name, kind string) bool { return !strings.HasPrefix(sig, "defp") },
	})
	add("go", ".go", &language{lex: lexConfig{lineComments: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'`", charLits: true}, end: endBrace})
}

// langFor returns the language for a repo-relative path, or nil.
func langFor(rel string) *language {
	name, ok := extToLang[strings.ToLower(filepath.Ext(rel))]
	if !ok {
		return nil
	}
	return languages[name]
}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

func rx(s string) *regexp.Regexp { return regexp.MustCompile(s) }
