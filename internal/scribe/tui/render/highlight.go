package render

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// tokKind classifies a run of source text for colouring.
type tokKind uint8

const (
	kPlain tokKind = iota
	kKeyword
	kString
	kNumber
	kComment
	kFunc
	kType
	kOperator
	kPunct
	kVariable
	kAdd  // diff: added line
	kDel  // diff: removed line
	kHunk // diff: hunk header
	kMeta // diff: file header
)

type token struct {
	kind tokKind
	text string
}

// cell is one rendered rune: its text, token class and optional background.
type cell struct {
	r    rune
	kind tokKind
	bg   lipgloss.TerminalColor
	bold bool
}

// langSpec describes a language for the generic tokenizer.
type langSpec struct {
	keywords    map[string]bool
	types       map[string]bool
	lineComment []string // any of these starts a comment to end of line
	blockOpen   string
	blockClose  string
	quotes      string // runes that open a string literal
	foldCase    bool   // keywords match case-insensitively (SQL)
	typeCap     bool   // capitalised identifiers are types
	mode        string // "", "json", "yaml", "html", "css", "markdown", "diff", "bash"
}

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

const cLikeKeywords = "auto break case char const continue default do double else enum extern float for goto if inline int long register restrict return short signed sizeof static struct switch typedef union unsigned void volatile while bool true false NULL"

var langSpecs = map[string]*langSpec{
	"go": {
		keywords:    wordSet("break default func interface select case defer go map struct chan else goto package switch const fallthrough if range type continue for import return var true false nil iota"),
		types:       wordSet("string int int8 int16 int32 int64 uint uint8 uint16 uint32 uint64 uintptr byte rune error any bool float32 float64 complex64 complex128"),
		lineComment: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'`", typeCap: true,
	},
	"ts": {
		keywords:    wordSet("break case catch class const continue debugger default delete do else enum export extends false finally for function if import in instanceof new null return super switch this throw true try typeof undefined var void while with yield let static async await of from as type interface implements private public protected readonly declare abstract namespace module keyof infer is satisfies"),
		types:       wordSet("string number boolean any unknown never object symbol bigint Promise Array Record Partial"),
		lineComment: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'`", typeCap: true,
	},
	"python": {
		keywords:    wordSet("and as assert async await break class continue def del elif else except False finally for from global if import in is lambda None nonlocal not or pass raise return True try while with yield match case self"),
		types:       wordSet("int str float bool list dict set tuple bytes object type"),
		lineComment: []string{"#"}, quotes: "\"'", typeCap: true,
	},
	"rust": {
		keywords:    wordSet("as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while"),
		types:       wordSet("i8 i16 i32 i64 i128 isize u8 u16 u32 u64 u128 usize f32 f64 bool char str String Vec Option Result Box"),
		lineComment: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"", typeCap: true,
	},
	"json": {mode: "json", keywords: wordSet("true false null")},
	"yaml": {mode: "yaml", keywords: wordSet("true false null yes no on off"), lineComment: []string{"#"}, quotes: "\"'"},
	"bash": {
		mode:        "bash",
		keywords:    wordSet("if then else elif fi for while until do done case esac in function return local export readonly unset shift exit break continue select declare"),
		lineComment: []string{"#"}, quotes: "\"'",
	},
	"c": {
		keywords:    wordSet(cLikeKeywords),
		lineComment: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'", typeCap: false,
	},
	"cpp": {
		keywords:    wordSet(cLikeKeywords + " class namespace template typename public private protected virtual override using new delete this try catch throw nullptr auto constexpr noexcept decltype"),
		types:       wordSet("size_t string vector map"),
		lineComment: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'", typeCap: true,
	},
	"java": {
		keywords:    wordSet("abstract assert boolean break byte case catch char class const continue default do double else enum extends final finally float for goto if implements import instanceof int interface long native new package private protected public return short static strictfp super switch synchronized this throw throws transient try void volatile while true false null var record yield sealed"),
		lineComment: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'", typeCap: true,
	},
	"ruby": {
		keywords:    wordSet("alias and begin break case class def defined do else elsif end ensure false for if in module next nil not or redo rescue retry return self super then true undef unless until when while yield require include attr_accessor"),
		lineComment: []string{"#"}, quotes: "\"'", typeCap: true,
	},
	"sql": {
		keywords:    wordSet("select from where insert into values update set delete create table drop alter index join left right inner outer on and or not null as order by group having limit offset union distinct case when then else end primary key foreign references default is in like between exists"),
		types:       wordSet("int integer text varchar bigint boolean date timestamp numeric"),
		lineComment: []string{"--"}, blockOpen: "/*", blockClose: "*/", quotes: "'\"", foldCase: true,
	},
	"html": {mode: "html", blockOpen: "<!--", blockClose: "-->", quotes: "\"'"},
	"css":  {mode: "css", blockOpen: "/*", blockClose: "*/", quotes: "\"'"},
	"swift": {
		keywords:    wordSet("associatedtype break case catch class continue default defer deinit do else enum extension fallthrough false fileprivate for func guard if import in init inout internal is let nil operator private protocol public repeat rethrows return self Self static struct subscript super switch throw throws true try typealias var where while async await actor some any open override final lazy weak unowned"),
		types:       wordSet("String Int Double Float Bool Array Dictionary Set Optional Void Error Character"),
		lineComment: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"", typeCap: true,
	},
	"csharp": {
		keywords:    wordSet("abstract as base bool break byte case catch char checked class const continue decimal default delegate do double else enum event explicit extern false finally fixed float for foreach goto if implicit in int interface internal is lock long namespace new null object operator out override params private protected public readonly ref return sbyte sealed short sizeof stackalloc static string struct switch this throw true try typeof uint ulong unchecked unsafe ushort using virtual void volatile while async await var record get set yield"),
		lineComment: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'", typeCap: true,
	},
	"php": {
		keywords:    wordSet("abstract and array as break callable case catch class clone const continue declare default do echo else elseif empty enddeclare endfor endforeach endif endswitch endwhile extends final finally fn for foreach function global goto if implements include include_once instanceof insteadof interface isset list match namespace new or print private protected public readonly require require_once return static switch throw trait try unset use var while xor yield false null true self parent"),
		lineComment: []string{"//", "#"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'", typeCap: true,
	},
	"lua": {
		keywords:    wordSet("and break do else elseif end false for function goto if in local nil not or repeat return then true until while"),
		lineComment: []string{"--"}, quotes: "\"'",
	},
	"zig": {
		keywords:    wordSet("align allowzero and anyframe anytype asm async await break catch comptime const continue defer else enum errdefer error export extern false fn for if inline noalias null or orelse packed pub resume return linksection struct suspend switch test threadlocal true try union unreachable usingnamespace var volatile while"),
		types:       wordSet("bool void u8 u16 u32 u64 usize i8 i16 i32 i64 isize f32 f64 type anyerror"),
		lineComment: []string{"//"}, quotes: "\"'", typeCap: true,
	},
	"graphql": {
		keywords:    wordSet("query mutation subscription fragment on type input enum interface union schema extend implements scalar directive repeatable true false null"),
		lineComment: []string{"#"}, quotes: "\"", typeCap: true,
	},
	"markdown": {mode: "markdown"},
	"diff":     {mode: "diff"},
}

// langAliases maps file-type names to a langSpecs key.
var langAliases = map[string]string{
	"go": "go", "golang": "go",
	"ts": "ts", "tsx": "ts", "typescript": "ts", "js": "ts", "jsx": "ts", "javascript": "ts", "mjs": "ts", "cjs": "ts",
	"py": "python", "python": "python",
	"rs": "rust", "rust": "rust",
	"json": "json", "jsonc": "json",
	"yaml": "yaml", "yml": "yaml",
	"sh": "bash", "bash": "bash", "zsh": "bash", "shell": "bash",
	"c": "c", "h": "c",
	"cpp": "cpp", "c++": "cpp", "cc": "cpp", "cxx": "cpp", "hpp": "cpp", "hh": "cpp",
	"java": "java", "kt": "java", "kotlin": "java",
	"rb": "ruby", "ruby": "ruby",
	"sql":  "sql",
	"html": "html", "htm": "html", "xml": "html", "svg": "html",
	"css": "css", "scss": "css", "less": "css",
	"md": "markdown", "markdown": "markdown",
	"diff": "diff", "patch": "diff",
	"swift": "swift",
	"cs":    "csharp", "csharp": "csharp", "c#": "csharp",
	"php":     "php",
	"lua":     "lua",
	"zig":     "zig",
	"graphql": "graphql", "gql": "graphql",
}

// LangFromPath returns the highlighter language for a file name, or "" when the
// extension is unknown (render plain).
func LangFromPath(path string) string {
	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(base))
	if ext == "" {
		return ""
	}
	if l, ok := langAliases[strings.TrimPrefix(ext, ".")]; ok {
		return l
	}
	return ""
}

// resolveLang maps a fence info word to a langSpecs key, "" when unknown.
func resolveLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(l, " \t{"); i >= 0 {
		l = l[:i]
	}
	return langAliases[l]
}

// Highlight colours source code. Foreground colours only; the caller supplies
// any background. Unknown languages come back as plain text.
func Highlight(code, lang string, th theme.Theme) string {
	lines := strings.Split(strings.ReplaceAll(code, "\t", "    "), "\n")
	toks := tokenizeLines(lines, resolveLang(lang))
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		cells := cellsFor(line, toks[i], nil)
		b.WriteString(paint(cells, th))
	}
	return b.String()
}

// tokenizeLines splits each line into classified tokens. Block comments carry
// across lines. lang "" yields one plain token per line.
func tokenizeLines(lines []string, lang string) [][]token {
	out := make([][]token, len(lines))
	spec := langSpecs[lang]
	inBlock := false
	for i, line := range lines {
		if spec == nil {
			out[i] = []token{{kPlain, line}}
			continue
		}
		var toks []token
		toks, inBlock = tokenizeLine(line, spec, inBlock)
		out[i] = coalesce(toks)
	}
	return out
}

func coalesce(toks []token) []token {
	var out []token
	for _, t := range toks {
		if t.text == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].kind == t.kind {
			out[n-1].text += t.text
			continue
		}
		out = append(out, t)
	}
	return out
}

func hasPrefixAt(r []rune, i int, p string) bool {
	pr := []rune(p)
	if i+len(pr) > len(r) {
		return false
	}
	for k, c := range pr {
		if r[i+k] != c {
			return false
		}
	}
	return true
}

func isIdentStart(c rune) bool { return c == '_' || c == '$' || unicode.IsLetter(c) }
func isIdentPart(c rune) bool {
	return c == '_' || c == '$' || unicode.IsLetter(c) || unicode.IsDigit(c)
}

func tokenizeLine(line string, spec *langSpec, inBlock bool) ([]token, bool) {
	switch spec.mode {
	case "diff":
		return diffLine(line), false
	case "markdown":
		return markdownLine(line), false
	case "json":
		return jsonLine(line), false
	case "yaml":
		return yamlLine(line, spec), false
	}
	r := []rune(line)
	n := len(r)
	var toks []token
	emit := func(k tokKind, from, to int) {
		if to > from {
			toks = append(toks, token{k, string(r[from:to])})
		}
	}
	i := 0
	for i < n {
		if inBlock {
			end := -1
			for j := i; j < n; j++ {
				if hasPrefixAt(r, j, spec.blockClose) {
					end = j + len([]rune(spec.blockClose))
					break
				}
			}
			if end < 0 {
				emit(kComment, i, n)
				return toks, true
			}
			emit(kComment, i, end)
			i = end
			inBlock = false
			continue
		}
		c := r[i]
		if unicode.IsSpace(c) {
			emit(kPlain, i, i+1)
			i++
			continue
		}
		if spec.mode == "html" && hasPrefixAt(r, i, "<!--") {
			inBlock = true
			toks = append(toks, token{kComment, "<!--"})
			i += 4
			continue
		}
		if commentAt(r, i, spec) {
			emit(kComment, i, n)
			return toks, false
		}
		if spec.blockOpen != "" && hasPrefixAt(r, i, spec.blockOpen) && spec.mode != "html" {
			inBlock = true
			toks = append(toks, token{kComment, spec.blockOpen})
			i += len([]rune(spec.blockOpen))
			continue
		}
		if strings.ContainsRune(spec.quotes, c) {
			j := i + 1
			for j < n {
				if r[j] == '\\' {
					j += 2
					continue
				}
				if r[j] == c {
					j++
					break
				}
				j++
			}
			if j > n {
				j = n
			}
			emit(kString, i, j)
			i = j
			continue
		}
		if unicode.IsDigit(c) || (c == '.' && i+1 < n && unicode.IsDigit(r[i+1])) {
			j := i + 1
			for j < n && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_' || r[j] == '.') {
				j++
			}
			emit(kNumber, i, j)
			i = j
			continue
		}
		if isIdentStart(c) {
			j := i + 1
			for j < n && isIdentPart(r[j]) {
				j++
			}
			word := string(r[i:j])
			key := word
			if spec.foldCase {
				key = strings.ToLower(word)
			}
			k := kPlain
			switch {
			case spec.keywords[key]:
				k = kKeyword
			case spec.types[key]:
				k = kType
			case spec.mode == "html" || spec.mode == "css":
				k = kPlain
			case nextNonSpace(r, j) == '(':
				k = kFunc
			case spec.typeCap && unicode.IsUpper([]rune(word)[0]):
				k = kType
			}
			if k == kPlain && spec.mode == "bash" && i > 0 && r[i-1] == '$' {
				k = kVariable
			}
			if k == kPlain && spec.mode == "css" && nextNonSpace(r, j) == ':' {
				k = kVariable
			}
			emit(k, i, j)
			i = j
			continue
		}
		if c == '$' && spec.mode == "bash" && i+1 < n && (isIdentStart(r[i+1]) || r[i+1] == '{') {
			j := i + 1
			if r[j] == '{' {
				for j < n && r[j] != '}' {
					j++
				}
				if j < n {
					j++
				}
			} else {
				for j < n && isIdentPart(r[j]) {
					j++
				}
			}
			emit(kVariable, i, j)
			i = j
			continue
		}
		if spec.mode == "css" && c == '@' && i+1 < n && isIdentStart(r[i+1]) {
			j := i + 1
			for j < n && isIdentPart(r[j]) {
				j++
			}
			emit(kKeyword, i, j)
			i = j
			continue
		}
		if spec.mode == "css" && c == '#' && i+1 < n && isHex(r[i+1]) {
			j := i + 1
			for j < n && isHex(r[j]) {
				j++
			}
			emit(kNumber, i, j)
			i = j
			continue
		}
		if strings.ContainsRune("+-*/%=<>!&|^~?:", c) {
			emit(kOperator, i, i+1)
		} else if strings.ContainsRune("()[]{}.,;", c) {
			emit(kPunct, i, i+1)
		} else {
			emit(kPlain, i, i+1)
		}
		i++
	}
	return toks, inBlock
}

func isHex(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func commentAt(r []rune, i int, spec *langSpec) bool {
	for _, lc := range spec.lineComment {
		if hasPrefixAt(r, i, lc) {
			return true
		}
	}
	return false
}

func nextNonSpace(r []rune, from int) rune {
	for j := from; j < len(r); j++ {
		if !unicode.IsSpace(r[j]) {
			return r[j]
		}
	}
	return 0
}

// jsonLine colours one line of JSON. A string followed by a colon is a key.
func jsonLine(line string) []token {
	r := []rune(line)
	n := len(r)
	var toks []token
	for i := 0; i < n; {
		c := r[i]
		switch {
		case unicode.IsSpace(c):
			j := i
			for j < n && unicode.IsSpace(r[j]) {
				j++
			}
			toks = append(toks, token{kPlain, string(r[i:j])})
			i = j
		case c == '"':
			j := i + 1
			for j < n {
				if r[j] == '\\' {
					j += 2
					continue
				}
				if r[j] == '"' {
					j++
					break
				}
				j++
			}
			if j > n {
				j = n
			}
			k := kString
			if nextNonSpace(r, j) == ':' {
				k = kVariable
			}
			toks = append(toks, token{k, string(r[i:j])})
			i = j
		case unicode.IsDigit(c) || c == '-':
			j := i + 1
			for j < n && (unicode.IsDigit(r[j]) || r[j] == '.' || r[j] == 'e' || r[j] == 'E' || r[j] == '+' || r[j] == '-') {
				j++
			}
			toks = append(toks, token{kNumber, string(r[i:j])})
			i = j
		case unicode.IsLetter(c):
			j := i
			for j < n && unicode.IsLetter(r[j]) {
				j++
			}
			w := string(r[i:j])
			k := kPlain
			if w == "true" || w == "false" || w == "null" {
				k = kKeyword
			}
			toks = append(toks, token{k, w})
			i = j
		default:
			k := kPunct
			toks = append(toks, token{k, string(c)})
			i++
		}
	}
	return toks
}

// yamlLine colours one line of YAML: comments, keys before a colon, scalars.
func yamlLine(line string, spec *langSpec) []token {
	r := []rune(line)
	n := len(r)
	var toks []token
	// leading indent and list dashes
	i := 0
	for i < n && (r[i] == ' ' || r[i] == '\t') {
		i++
	}
	if i > 0 {
		toks = append(toks, token{kPlain, string(r[:i])})
	}
	if i < n && r[i] == '-' && (i+1 == n || r[i+1] == ' ') {
		toks = append(toks, token{kPunct, "-"})
		i++
	}
	// key: value
	if i < n && r[i] != '#' {
		if idx := strings.Index(string(r[i:]), ":"); idx >= 0 {
			keyPart := string(r[i:])[:idx]
			rest := string(r[i:])[idx:]
			if keyPart != "" && !strings.ContainsAny(keyPart, "\"'") && (len(rest) == 1 || rest[1] == ' ') {
				toks = append(toks, token{kVariable, keyPart}, token{kPunct, ":"})
				i += len([]rune(keyPart)) + 1
			}
		}
	}
	for i < n {
		c := r[i]
		switch {
		case c == '#' && (i == 0 || r[i-1] == ' ' || r[i-1] == '\t'):
			toks = append(toks, token{kComment, string(r[i:])})
			return toks
		case c == '"' || c == '\'':
			j := i + 1
			for j < n && r[j] != c {
				j++
			}
			if j < n {
				j++
			}
			toks = append(toks, token{kString, string(r[i:j])})
			i = j
		case unicode.IsDigit(c):
			j := i
			for j < n && (unicode.IsDigit(r[j]) || r[j] == '.') {
				j++
			}
			toks = append(toks, token{kNumber, string(r[i:j])})
			i = j
		case unicode.IsLetter(c):
			j := i
			for j < n && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_' || r[j] == '-') {
				j++
			}
			w := string(r[i:j])
			k := kPlain
			if spec.keywords[w] {
				k = kKeyword
			}
			toks = append(toks, token{k, w})
			i = j
		default:
			toks = append(toks, token{kPlain, string(c)})
			i++
		}
	}
	return toks
}

// markdownLine colours one line of Markdown: headings, quotes, code spans, list markers.
func markdownLine(line string) []token {
	trim := strings.TrimLeft(line, " ")
	switch {
	case strings.HasPrefix(trim, "#"):
		return []token{{kKeyword, line}}
	case strings.HasPrefix(trim, ">"):
		return []token{{kComment, line}}
	case strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~"):
		return []token{{kPunct, line}}
	}
	var toks []token
	rest := line
	if strings.HasPrefix(trim, "- ") || strings.HasPrefix(trim, "* ") {
		ind := len(line) - len(trim)
		toks = append(toks, token{kPlain, line[:ind]}, token{kPunct, trim[:1]})
		rest = trim[1:]
	}
	for rest != "" {
		i := strings.IndexByte(rest, '`')
		if i < 0 {
			toks = append(toks, token{kPlain, rest})
			break
		}
		toks = append(toks, token{kPlain, rest[:i]})
		j := strings.IndexByte(rest[i+1:], '`')
		if j < 0 {
			toks = append(toks, token{kPlain, rest[i:]})
			break
		}
		toks = append(toks, token{kString, rest[i : i+j+2]})
		rest = rest[i+j+2:]
	}
	return toks
}

// diffLine colours one line of a unified diff.
func diffLine(line string) []token {
	switch {
	case strings.HasPrefix(line, "@@"):
		return []token{{kHunk, line}}
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"),
		strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
		return []token{{kMeta, line}}
	case strings.HasPrefix(line, "+"):
		return []token{{kAdd, line}}
	case strings.HasPrefix(line, "-"):
		return []token{{kDel, line}}
	}
	return []token{{kPlain, line}}
}

// cellsFor expands tokens into per-rune cells with an optional background.
func cellsFor(line string, toks []token, bg lipgloss.TerminalColor) []cell {
	var cells []cell
	if len(toks) == 0 {
		for _, r := range line {
			cells = append(cells, cell{r: r, bg: bg})
		}
		return cells
	}
	for _, t := range toks {
		for _, r := range t.text {
			cells = append(cells, cell{r: r, kind: t.kind, bg: bg})
		}
	}
	return cells
}

// kindColor returns the foreground for a token kind.
func kindColor(k tokKind, th theme.Theme) lipgloss.TerminalColor {
	switch k {
	case kKeyword:
		return th.SynKeyword
	case kString:
		return th.SynString
	case kNumber:
		return th.SynNumber
	case kComment:
		return th.SynComment
	case kFunc:
		return th.SynFunction
	case kType:
		return th.SynType
	case kOperator:
		return th.SynOperator
	case kPunct:
		return th.SynPunctuation
	case kVariable:
		return th.SynVariable
	case kAdd:
		return th.DiffAddedSign
	case kDel:
		return th.DiffRemovedSign
	case kHunk:
		return th.Info
	case kMeta:
		return th.TextDim
	}
	return th.TextPrimary
}

// paint renders cells, grouping runs that share a style into one Render call.
func paint(cells []cell, th theme.Theme) string {
	var b strings.Builder
	for i := 0; i < len(cells); {
		j := i + 1
		for j < len(cells) && cells[j].kind == cells[i].kind && cells[j].bg == cells[i].bg && cells[j].bold == cells[i].bold {
			j++
		}
		var sb strings.Builder
		for _, c := range cells[i:j] {
			sb.WriteRune(c.r)
		}
		st := lipgloss.NewStyle().Foreground(kindColor(cells[i].kind, th))
		if cells[i].kind == kComment {
			st = st.Italic(true)
		}
		if cells[i].bg != nil {
			st = st.Background(cells[i].bg)
		}
		if cells[i].bold {
			st = st.Bold(true)
		}
		b.WriteString(st.Render(sb.String()))
		i = j
	}
	return b.String()
}
