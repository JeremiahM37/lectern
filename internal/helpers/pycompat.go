package helpers

// The agent-hook helpers replace Python scripts whose output other programs
// read back — a settings file Claude Code parses, a ~/.claude.json the CLI
// rewrites, a status line on screen. Byte-identical output means doing a few
// things exactly the way CPython does them: its json module (insertion order,
// ensure_ascii, float repr, NaN), str() of a decoded value, str.strip(),
// os.path.expanduser and shlex.quote. They live here so each port stays a
// line-for-line reading of its script.

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// pyDict is a JSON object as Python holds it: keys keep insertion order, and
// re-assigning a key keeps its place.
type pyDict struct {
	keys []string
	vals map[string]any
}

func newPyDict() *pyDict { return &pyDict{vals: map[string]any{}} }

func (d *pyDict) get(k string) (any, bool) {
	v, ok := d.vals[k]
	return v, ok
}

func (d *pyDict) set(k string, v any) {
	if _, ok := d.vals[k]; !ok {
		d.keys = append(d.keys, k)
	}
	d.vals[k] = v
}

func (d *pyDict) pop(k string) {
	if _, ok := d.vals[k]; !ok {
		return
	}
	delete(d.vals, k)
	for i, key := range d.keys {
		if key == k {
			d.keys = append(d.keys[:i:i], d.keys[i+1:]...)
			break
		}
	}
}

// pyJSONLoads decodes s as CPython's json.loads does. Values are nil, bool,
// *big.Int, float64, string, []any and *pyDict. Lone UTF-16 surrogates from
// \u escapes are kept, encoded as WTF-8, so they survive a round trip.
func pyJSONLoads(s string) (any, error) {
	p := &pyJSONParser{s: s}
	p.ws()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, fmt.Errorf("json: extra data at %d", p.i)
	}
	return v, nil
}

// pyJSONLoadBytes is json.load on a text-mode file: the bytes must be UTF-8.
func pyJSONLoadBytes(b []byte) (any, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("json: invalid utf-8")
	}
	return pyJSONLoads(string(b))
}

type pyJSONParser struct {
	s string
	i int
}

func (p *pyJSONParser) ws() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *pyJSONParser) fail() error { return fmt.Errorf("json: invalid document at %d", p.i) }

func (p *pyJSONParser) lit(word string) bool {
	if strings.HasPrefix(p.s[p.i:], word) {
		p.i += len(word)
		return true
	}
	return false
}

func (p *pyJSONParser) value(depth int) (any, error) {
	// CPython stops at its recursion limit; nobody writes JSON this deep.
	if depth > 900 {
		return nil, p.fail()
	}
	if p.i >= len(p.s) {
		return nil, p.fail()
	}
	switch c := p.s[p.i]; {
	case c == '"':
		p.i++
		return p.str()
	case c == '{':
		p.i++
		return p.object(depth)
	case c == '[':
		p.i++
		return p.array(depth)
	case p.lit("null"):
		return nil, nil
	case p.lit("true"):
		return true, nil
	case p.lit("false"):
		return false, nil
	case p.lit("NaN"):
		return math.NaN(), nil
	case p.lit("Infinity"):
		return math.Inf(1), nil
	case p.lit("-Infinity"):
		return math.Inf(-1), nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	return nil, p.fail()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// number follows json's NUMBER_RE: (-?(?:0|[1-9]\d*))(\.\d+)?([eE][-+]?\d+)?
func (p *pyJSONParser) number() (any, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	if p.i >= len(p.s) || !isDigit(p.s[p.i]) {
		p.i = start
		return nil, p.fail()
	}
	if p.s[p.i] == '0' {
		p.i++
	} else {
		for p.i < len(p.s) && isDigit(p.s[p.i]) {
			p.i++
		}
	}
	isFloat := false
	if p.i+1 < len(p.s) && p.s[p.i] == '.' && isDigit(p.s[p.i+1]) {
		isFloat = true
		p.i++
		for p.i < len(p.s) && isDigit(p.s[p.i]) {
			p.i++
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		j := p.i + 1
		if j < len(p.s) && (p.s[j] == '+' || p.s[j] == '-') {
			j++
		}
		if j < len(p.s) && isDigit(p.s[j]) {
			isFloat = true
			for j < len(p.s) && isDigit(p.s[j]) {
				j++
			}
			p.i = j
		}
	}
	text := p.s[start:p.i]
	if isFloat {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, p.fail()
		}
		return f, nil
	}
	n, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return nil, p.fail()
	}
	return n, nil
}

func hexVal(s string) (int, bool) {
	if len(s) != 4 {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil || strings.ContainsAny(s, "+-_xX") {
		return 0, false
	}
	return int(n), true
}

// appendWTF8 writes a code point, including a lone surrogate, as bytes.
func appendWTF8(b []byte, r int) []byte {
	if r >= 0xD800 && r <= 0xDFFF {
		return append(b, byte(0xE0|r>>12), byte(0x80|(r>>6)&0x3F), byte(0x80|r&0x3F))
	}
	return utf8.AppendRune(b, rune(r))
}

func (p *pyJSONParser) str() (string, error) {
	var b []byte
	for {
		if p.i >= len(p.s) {
			return "", p.fail()
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return string(b), nil
		case c < 0x20:
			return "", p.fail()
		case c != '\\':
			b = append(b, c)
			p.i++
			continue
		}
		p.i++
		if p.i >= len(p.s) {
			return "", p.fail()
		}
		esc := p.s[p.i]
		p.i++
		switch esc {
		case '"', '\\', '/':
			b = append(b, esc)
		case 'b':
			b = append(b, '\b')
		case 'f':
			b = append(b, '\f')
		case 'n':
			b = append(b, '\n')
		case 'r':
			b = append(b, '\r')
		case 't':
			b = append(b, '\t')
		case 'u':
			if p.i+4 > len(p.s) {
				return "", p.fail()
			}
			r, ok := hexVal(p.s[p.i : p.i+4])
			if !ok {
				return "", p.fail()
			}
			p.i += 4
			if r >= 0xD800 && r <= 0xDBFF && p.i+6 <= len(p.s) && p.s[p.i] == '\\' && p.s[p.i+1] == 'u' {
				if lo, ok := hexVal(p.s[p.i+2 : p.i+6]); ok && lo >= 0xDC00 && lo <= 0xDFFF {
					r = 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00)
					p.i += 6
				}
			}
			b = appendWTF8(b, r)
		default:
			return "", p.fail()
		}
	}
}

func (p *pyJSONParser) object(depth int) (any, error) {
	d := newPyDict()
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return d, nil
	}
	for {
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, p.fail()
		}
		p.i++
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, p.fail()
		}
		p.i++
		p.ws()
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		d.set(k, v)
		p.ws()
		if p.i >= len(p.s) {
			return nil, p.fail()
		}
		switch p.s[p.i] {
		case '}':
			p.i++
			return d, nil
		case ',':
			p.i++
			p.ws()
		default:
			return nil, p.fail()
		}
	}
}

func (p *pyJSONParser) array(depth int) (any, error) {
	out := []any{}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return out, nil
	}
	for {
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.ws()
		if p.i >= len(p.s) {
			return nil, p.fail()
		}
		switch p.s[p.i] {
		case ']':
			p.i++
			return out, nil
		case ',':
			p.i++
			p.ws()
		default:
			return nil, p.fail()
		}
	}
}

// pyJSONDumps is json.dumps. indent < 0 is the compact default (", ", ": ");
// indent >= 0 is json.dump(..., indent=N). Keys and values keep their order.
func pyJSONDumps(v any, indent int, ensureASCII bool) (string, error) {
	var b strings.Builder
	if err := pyJSONWrite(&b, v, indent, 0, ensureASCII); err != nil {
		return "", err
	}
	return b.String(), nil
}

func pyJSONWrite(b *strings.Builder, v any, indent, level int, ascii bool) error {
	newline := func(l int) {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(" ", indent*l))
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case *big.Int:
		b.WriteString(x.String())
	case int:
		b.WriteString(strconv.Itoa(x))
	case float64:
		switch {
		case math.IsNaN(x):
			b.WriteString("NaN")
		case math.IsInf(x, 1):
			b.WriteString("Infinity")
		case math.IsInf(x, -1):
			b.WriteString("-Infinity")
		default:
			b.WriteString(pyFloatRepr(x))
		}
	case string:
		pyJSONString(b, x, ascii)
	case []string:
		items := make([]any, len(x))
		for i, s := range x {
			items[i] = s
		}
		return pyJSONWrite(b, items, indent, level, ascii)
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteByte(',')
				if indent < 0 {
					b.WriteByte(' ')
				}
			}
			if indent >= 0 {
				newline(level + 1)
			}
			if err := pyJSONWrite(b, item, indent, level+1, ascii); err != nil {
				return err
			}
		}
		if indent >= 0 {
			newline(level)
		}
		b.WriteByte(']')
	case *pyDict:
		if len(x.keys) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				b.WriteByte(',')
				if indent < 0 {
					b.WriteByte(' ')
				}
			}
			if indent >= 0 {
				newline(level + 1)
			}
			pyJSONString(b, k, ascii)
			b.WriteString(": ")
			if err := pyJSONWrite(b, x.vals[k], indent, level+1, ascii); err != nil {
				return err
			}
		}
		if indent >= 0 {
			newline(level)
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("json: cannot encode %T", v)
	}
	return nil
}

// decodeWTF8 reads one code point, returning lone surrogates as themselves.
func decodeWTF8(s string) (int, int) {
	if len(s) >= 3 && s[0] == 0xED && s[1] >= 0xA0 && s[1] <= 0xBF && s[2] >= 0x80 && s[2] <= 0xBF {
		return int(s[0]&0x0F)<<12 | int(s[1]&0x3F)<<6 | int(s[2]&0x3F), 3
	}
	r, n := utf8.DecodeRuneInString(s)
	return int(r), n
}

func pyJSONString(b *strings.Builder, s string, ascii bool) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, n := decodeWTF8(s[i:])
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20 || (ascii && r >= 0x7f):
				if r > 0xFFFF {
					r -= 0x10000
					fmt.Fprintf(b, `\u%04x\u%04x`, 0xD800|(r>>10)&0x3FF, 0xDC00|r&0x3FF)
				} else {
					fmt.Fprintf(b, `\u%04x`, r)
				}
			default:
				b.WriteString(s[i : i+n])
			}
		}
		i += n
	}
	b.WriteByte('"')
}

// pyFloatRepr is repr(float) for a finite value: the shortest round-trip
// digits, positional between 1e-4 and 1e16, always with a decimal point.
func pyFloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	exp, _ := strconv.Atoi(sci[strings.IndexByte(sci, 'e')+1:])
	if exp < -4 || exp >= 16 {
		return sci
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

// pyStr is str(value) for a decoded JSON value, as an f-string renders it.
func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyRepr(v)
}

// pyRepr is repr(value) for a decoded JSON value.
func pyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case *big.Int:
		return x.String()
	case int:
		return strconv.Itoa(x)
	case float64:
		return pyFloatRepr(x)
	case string:
		return pyStrRepr(x)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = pyRepr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *pyDict:
		parts := make([]string, len(x.keys))
		for i, k := range x.keys {
			parts[i] = pyStrRepr(k) + ": " + pyRepr(x.vals[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

func pyStrRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for i := 0; i < len(s); {
		r, n := decodeWTF8(s[i:])
		switch {
		case r == int(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteByte(byte(r))
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteByte(byte(r))
		case (r >= 0xD800 && r <= 0xDFFF) || !unicode.IsPrint(rune(r)):
			switch {
			case r <= 0xff:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r <= 0xffff:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	b.WriteByte(quote)
	return b.String()
}

// pyTruthy is Python's bool() of a decoded JSON value.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case *big.Int:
		return x.Sign() != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *pyDict:
		return len(x.keys) > 0
	}
	return true
}

// pyIsSpace is str.isspace for one rune.
func pyIsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// pyStrip is str.strip() with no argument.
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pyUniversalNewlines is what reading a file in text mode does to line
// endings: "\r\n" and a lone "\r" both become "\n".
func pyUniversalNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// pyDecodeReplace is bytes.decode("utf-8", "replace"): one U+FFFD per
// maximal invalid subsequence, as CPython's decoder emits them.
func pyDecodeReplace(b []byte) string {
	var out strings.Builder
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || n > 1 {
			out.Write(b[i : i+n])
			i += n
			continue
		}
		out.WriteRune(utf8.RuneError)
		i += invalidPrefixLen(b[i:])
	}
	return out.String()
}

// invalidPrefixLen is the length of the maximal subpart of an ill-formed
// UTF-8 sequence starting at b[0] (Unicode §3.9, which CPython follows).
func invalidPrefixLen(b []byte) int {
	c := b[0]
	var need int
	lo, hi := byte(0x80), byte(0xBF)
	switch {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c == 0xE0:
		need, lo = 2, 0xA0
	case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
		need = 2
	case c == 0xED:
		need, hi = 2, 0x9F
	case c == 0xF0:
		need, lo = 3, 0x90
	case c >= 0xF1 && c <= 0xF3:
		need = 3
	case c == 0xF4:
		need, hi = 3, 0x8F
	default:
		return 1
	}
	n := 1
	for k := 0; k < need && n < len(b); k++ {
		if b[n] < lo || b[n] > hi {
			break
		}
		lo, hi = 0x80, 0xBF
		n++
	}
	return n
}

// pyHome is os.path.expanduser("~").
func pyHome() string {
	if filepath.Separator == '/' {
		if h, ok := os.LookupEnv("HOME"); ok {
			h = strings.TrimRight(h, "/")
			if h == "" {
				return "/"
			}
			return h
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return "~"
}

// pyExpanduser is os.path.expanduser for "~" and "~/…" (and "~user/…").
func pyExpanduser(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	i := strings.IndexAny(p, `/`+string(filepath.Separator))
	if i < 0 {
		i = len(p)
	}
	if i == 1 {
		return pyHome() + p[1:]
	}
	u, err := user.Lookup(p[1:i])
	if err != nil {
		return p
	}
	home := strings.TrimRight(u.HomeDir, "/")
	if home == "" {
		home = "/"
	}
	return home + p[i:]
}

// pyMakedirs is os.makedirs(path, mode, exist_ok=True): parents get the
// default mode, only the leaf gets mode.
func pyMakedirs(path string, mode os.FileMode) error {
	if st, err := os.Stat(path); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", path)
		}
		return nil
	}
	if parent := filepath.Dir(path); parent != path {
		if err := pyMakedirs(parent, 0o777); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, mode); err != nil && !os.IsExist(err) {
		return err
	} else if err != nil {
		if st, serr := os.Stat(path); serr != nil || !st.IsDir() {
			return err
		}
	}
	return nil
}

// pyReplaceFile is the scripts' mkstemp-in-dir, write, os.replace: the new
// file has mkstemp's 0600 mode, and a failed rename leaves the temp file
// behind exactly as the script would.
func pyReplaceFile(dir, dest string, data []byte) error {
	f, err := os.CreateTemp(dir, "tmp")
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), dest)
}

// pyJoin is os.path.join: unlike filepath.Join it never cleans the result,
// so a printed path is the one the script printed.
func pyJoin(elem ...string) string {
	if filepath.Separator != '/' {
		return filepath.Join(elem...)
	}
	path := ""
	for i, e := range elem {
		switch {
		case i == 0 || strings.HasPrefix(e, "/"):
			path = e
		case path == "" || strings.HasSuffix(path, "/"):
			path += e
		default:
			path += "/" + e
		}
	}
	return path
}

// pyDirname is os.path.dirname.
func pyDirname(p string) string {
	if filepath.Separator != '/' {
		return filepath.Dir(p)
	}
	i := strings.LastIndex(p, "/") + 1
	head := p[:i]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head
}

// pyShlexQuote is shlex.quote.
func pyShlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !(r < 0x80 && (r == '_' || r == '@' || r == '%' || r == '+' || r == '=' || r == ':' ||
			r == ',' || r == '.' || r == '/' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))) {
			return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
		}
	}
	return s
}
