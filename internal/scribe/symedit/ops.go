package symedit

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// editor applies operations to the text of one file. It never touches disk.
type editor struct {
	lang *language
	rel  string
	base string
	ext  *extractor
}

func (e *editor) close() {
	if e.ext != nil {
		e.ext.close()
	}
}

// symbols locates every definition in text.
// maxParseBytes is the largest file the symbol tools will parse. Bigger files
// are generated or vendored, and parsing them costs more than the edit is worth.
const maxParseBytes = 1 << 20

func (e *editor) symbols(text string) ([]symbol, error) {
	if len(text) > maxParseBytes {
		return nil, fmt.Errorf("%s is over 1 MB, which is too large for symbol editing. Use edit_file on the lines you need.", e.rel)
	}
	if e.lang.fam == famGo {
		return goSymbols(text)
	}
	if e.ext == nil {
		e.ext = &extractor{}
	}
	return genericSymbols(e.ext, e.base, e.lang.fam, text)
}

// apply runs one operation against text and returns the new text, a short
// description, and the name of the symbol it addressed.
func (e *editor) apply(text string, o op) (string, string, string, error) {
	if o.Action == "add_import" {
		out, msg, err := e.addImport(text, o.NewCode)
		return out, msg, "imports", err
	}
	if o.Action == "insert_text" {
		out, msg, err := insertText(text, o)
		return out, msg, "file", err
	}
	if isModuleOp(o.Action) {
		out, msg, err := e.moduleOp(text, o)
		return out, msg, "imports", err
	}
	syms, err := e.symbols(text)
	if err != nil {
		return "", "", "", err
	}
	s, err := findSymbol(syms, parseSpec(o.Symbol), e.rel, o.Symbol)
	if err != nil {
		return "", "", "", err
	}
	if err := checkTarget(o.Action, s); err != nil {
		return "", "", "", err
	}
	idx := newLineIndex(text)
	masked := mask(text, e.lang.lex)
	var out, msg string
	switch o.Action {
	case "replace":
		out, msg, err = e.replace(idx, s, o.NewCode)
	case "replace_body":
		out, msg, err = e.replaceBody(idx, masked, s, o.NewCode)
	case "insert_before":
		out = replaceLines(idx, s.startLine, s.startLine, reindent(o.NewCode, lineIndent(idx.text(s.startLine)))+"\n\n")
		msg = fmt.Sprintf("inserted before %s (line %d)", s.qual(), s.startLine)
	case "insert_after":
		out, msg = e.insertAfter(idx, s, o.NewCode)
	case "delete":
		out, msg, err = e.remove(idx, s)
	case "rename_local":
		out, msg, err = renameLocal(text, masked, s, o.OldName, o.NewName)
	case "replace_in_body":
		out, msg, err = replaceInBody(idx, s, o.OldString, o.EndString, o.NewString)
	case "set_return_type":
		out, msg, err = e.setReturnType(text, idx, masked, s, o.NewCode)
	case "set_async":
		out, msg, err = e.setAsync(text, idx, masked, s)
	case "add_parameter":
		out, msg, err = e.addParameter(text, idx, masked, s, o.NewCode)
	case "remove_parameter":
		out, msg, err = e.removeParameter(text, idx, masked, s, o.NewCode)
	case "set_type":
		out, msg, err = e.setType(text, idx, masked, s, o.NewCode)
	case "set_initializer":
		out, msg, err = e.setInitializer(text, idx, masked, s, o.NewCode, false)
	case "remove_initializer":
		out, msg, err = e.setInitializer(text, idx, masked, s, "", true)
	case "rename":
		out, msg, err = e.renameDecl(text, idx, masked, s, o.NewName)
	case "add_method", "add_property", "add_getter", "add_setter", "add_constructor":
		out, msg, err = e.addMember(idx, masked, s, o.NewCode)
	case "set_static":
		out, msg, err = e.setStatic(text, idx, masked, s, staticRemoves(o.NewCode))
	case "add_decorator":
		out, msg, err = e.addDecorator(idx, s, o.NewCode)
	case "remove_decorator":
		out, msg, err = e.removeDecorator(idx, s, o.NewCode)
	case "remove_member":
		out, msg, err = e.removeMember(idx, s)
	case "set_extends":
		out, msg, err = e.setExtends(text, idx, masked, s, o.NewCode)
	default:
		err = fmt.Errorf("unknown action %q", o.Action)
	}
	if err != nil {
		return "", "", "", err
	}
	if out == text {
		msg = "already present: " + s.qual() + " already has this content"
	}
	if e.lang.fam == famPy && (o.Action == "replace_body" || o.Action == "replace_in_body" || o.Action == "rename_local") {
		after, err := e.symbols(out)
		if err == nil && len(after) != len(syms) {
			return "", "", "", fmt.Errorf("%s: the edit changed the file's structure (%d definitions became %d). The new body probably has the wrong indentation",
				s.qual(), len(syms), len(after))
		}
	}
	return out, msg, s.qual(), nil
}

func (e *editor) replace(idx lineIndex, s symbol, code string) (string, string, error) {
	if isBlank(code) {
		return "", "", fmt.Errorf("new_code is empty. Use action delete to remove %s", s.qual())
	}
	if len(s.shared) > 0 {
		return "", "", fmt.Errorf("%s is declared together with %s, so it cannot be replaced alone. Replace the whole declaration by naming its first name", s.qual(), strings.Join(s.shared, ", "))
	}
	ind := lineIndent(idx.text(s.startLine))
	msg := fmt.Sprintf("replaced %s (lines %d-%d)", s.qual(), s.startLine, s.endLine)
	if s.hasDoc && !hasLeadingComment(code) {
		msg += "; its doc comment was replaced too, since the new code has none"
	}
	return replaceLines(idx, s.startLine, s.endLine+1, reindent(code, ind)+"\n"), msg, nil
}

// replaceBody replaces what is inside a definition's body and keeps its header.
func (e *editor) replaceBody(idx lineIndex, masked string, s symbol, code string) (string, string, error) {
	msg := fmt.Sprintf("replaced the body of %s (lines %d-%d)", s.qual(), s.startLine, s.endLine)
	symIndent := lineIndent(idx.text(s.startLine))
	switch e.lang.fam {
	case famGo, famBrace:
		open, cl := s.bodyOpen, s.bodyClose
		if open < 0 {
			var ok bool
			if open, cl, ok = braceBody(masked, s.start, s.end); !ok {
				return "", "", fmt.Errorf("%s has no { } body. Use action replace for the whole definition", s.qual())
			}
		}
		bodyIndent := symIndent + unitFor(e.lang.fam)
		closeLine := idx.lineOf(cl)
		for ln := idx.lineOf(open) + 1; ln < closeLine; ln++ {
			if t := idx.text(ln); !isBlank(t) {
				if ind := lineIndent(t); len(ind) > len(symIndent) && strings.HasPrefix(ind, symIndent) {
					bodyIndent = ind
				}
				break
			}
		}
		inner := "\n" + symIndent
		if !isBlank(code) {
			inner = "\n" + reindent(code, bodyIndent) + "\n" + symIndent
		}
		return idx.src[:open+1] + inner + idx.src[cl:], msg, nil

	case famPy:
		hdr, ok := pyHeaderEnd(masked, s.start, s.end)
		if !ok {
			return "", "", fmt.Errorf("could not find the header of %s", s.qual())
		}
		hl := idx.lineOf(hdr)
		if hl >= s.endLine {
			return "", "", fmt.Errorf("%s has its body on the header line. Use action replace for it", s.qual())
		}
		defIndent := lineIndent(idx.text(hl))
		bodyIndent := ""
		for ln := hl + 1; ln <= s.endLine; ln++ {
			t := idx.text(ln)
			if isBlank(t) || strings.HasPrefix(strings.TrimLeft(t, " \t"), "#") {
				continue
			}
			ind := lineIndent(t)
			if len(ind) <= len(defIndent) || !strings.HasPrefix(ind, defIndent) {
				return "", "", fmt.Errorf("the body of %s is not indented under its header", s.qual())
			}
			bodyIndent = ind
			break
		}
		if bodyIndent == "" {
			bodyIndent = defIndent + "    "
		}
		repl := bodyIndent + "pass\n"
		if !isBlank(code) {
			repl = reindent(code, bodyIndent) + "\n"
		}
		return replaceLines(idx, hl+1, s.endLine+1, repl), msg, nil

	default: // famEnd
		if s.endLine-s.startLine < 2 || strings.TrimSpace(idx.text(s.endLine)) != "end" {
			return "", "", fmt.Errorf("could not find the closing end of %s", s.qual())
		}
		bodyIndent := symIndent + "  "
		for ln := s.startLine + 1; ln < s.endLine; ln++ {
			if t := idx.text(ln); !isBlank(t) {
				bodyIndent = lineIndent(t)
				break
			}
		}
		repl := ""
		if !isBlank(code) {
			repl = reindent(code, bodyIndent) + "\n"
		}
		return replaceLines(idx, s.startLine+1, s.endLine, repl), msg, nil
	}
}

func unitFor(f family) string {
	if f == famGo {
		return "\t"
	}
	return "    "
}

func (e *editor) insertAfter(idx lineIndex, s symbol, code string) (string, string) {
	pos := idx.start(s.endLine + 1)
	body := reindent(code, lineIndent(idx.text(s.startLine)))
	var ins string
	switch {
	case pos == len(idx.src) && strings.HasSuffix(idx.src, "\n"):
		ins = "\n" + body + "\n"
	case pos == len(idx.src):
		ins = "\n\n" + body
	case isBlank(idx.text(s.endLine + 1)):
		ins = "\n" + body + "\n"
	default:
		ins = "\n" + body + "\n\n"
	}
	return idx.src[:pos] + ins + idx.src[pos:], fmt.Sprintf("inserted after %s (line %d)", s.qual(), s.endLine)
}

func (e *editor) remove(idx lineIndex, s symbol) (string, string, error) {
	if len(s.shared) > 0 {
		return "", "", fmt.Errorf("%s is declared together with %s. Deleting it would delete them too; replace the whole declaration instead", s.qual(), strings.Join(s.shared, ", "))
	}
	from, to := s.startLine, s.endLine+1
	prevBlank := from > 1 && isBlank(idx.text(from-1))
	nextBlank := to <= idx.count() && isBlank(idx.text(to))
	if (prevBlank && nextBlank) || (from == 1 && nextBlank) {
		to++
	}
	return replaceLines(idx, from, to, ""), fmt.Sprintf("deleted %s (lines %d-%d)", s.qual(), s.startLine, s.endLine), nil
}

var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// renameLocal renames every code occurrence of an identifier inside a symbol.
// Strings and comments are skipped, and whole identifiers only.
func renameLocal(text, masked string, s symbol, oldName, newName string) (string, string, error) {
	if !identRe.MatchString(newName) {
		return "", "", fmt.Errorf("new_name %q is not an identifier", newName)
	}
	region := masked[s.start:s.end]
	var offs []int
	for i := 0; ; {
		j := strings.Index(region[i:], oldName)
		if j < 0 {
			break
		}
		p := i + j
		endp := p + len(oldName)
		if (p == 0 || !isIdentByte(region[p-1])) && (endp == len(region) || !isIdentByte(region[endp])) {
			offs = append(offs, s.start+p)
		}
		i = endp
	}
	if len(offs) == 0 {
		return "", "", fmt.Errorf("%s has no code identifier %q", s.qual(), oldName)
	}
	out := text
	for k := len(offs) - 1; k >= 0; k-- {
		out = out[:offs[k]] + newName + out[offs[k]+len(oldName):]
	}
	return out, fmt.Sprintf("renamed %q to %q in %s (%d places)", oldName, newName, s.qual(), len(offs)), nil
}

// replaceInBody replaces old inside a symbol (see replaceInBodyAnchored).
func replaceInBody(idx lineIndex, s symbol, old, end, nw string) (string, string, error) {
	return replaceInBodyAnchored(idx, s, old, end, nw)
}

// closestLines shows the lines of a symbol most similar to the first line of old.
func closestLines(idx lineIndex, s symbol, old string) string {
	first := old
	for _, l := range strings.Split(old, "\n") {
		if !isBlank(l) {
			first = l
			break
		}
	}
	want := bigrams(strings.TrimSpace(first))
	type cand struct {
		line  int
		score float64
	}
	var cs []cand
	for ln := s.startLine; ln <= s.endLine; ln++ {
		if sc := dice(bigrams(strings.TrimSpace(idx.text(ln))), want); sc > 0 {
			cs = append(cs, cand{ln, sc})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].score > cs[j].score })
	var sb strings.Builder
	for i := 0; i < len(cs) && i < 3; i++ {
		fmt.Fprintf(&sb, "\n%6d  %s", cs[i].line, clip(idx.text(cs[i].line), 160))
	}
	if sb.Len() == 0 {
		return " none similar"
	}
	return sb.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
