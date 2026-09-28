package helpers

// The Python helpers these ports replace print json.dumps output that the
// control plane parses, and some of them hash or store text built with
// json.dumps. Go's encoding/json neither keeps object key order nor formats
// numbers and strings the way Python does, so the ports use this small JSON
// model instead: objects keep insertion order (Python dicts), integers keep
// their exact digits, and the encoder reproduces json.dumps byte for byte.

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// pyDict is a JSON object in Python dict order: a key keeps the position of
// its first insertion, and assigning it again only replaces the value.
type pyDict struct {
	keys []string
	vals map[string]any
}

func newDict() *pyDict { return &pyDict{vals: map[string]any{}} }

// dict builds a pyDict from alternating keys and values.
func dict(kv ...any) *pyDict {
	d := newDict()
	for i := 0; i+1 < len(kv); i += 2 {
		d.set(kv[i].(string), kv[i+1])
	}
	return d
}

func (d *pyDict) get(k string) (any, bool) {
	v, ok := d.vals[k]
	return v, ok
}

// getOr is dict.get(k, def).
func (d *pyDict) getOr(k string, def any) any {
	if v, ok := d.vals[k]; ok {
		return v
	}
	return def
}

func (d *pyDict) set(k string, v any) {
	if _, ok := d.vals[k]; !ok {
		d.keys = append(d.keys, k)
	}
	d.vals[k] = v
}

func (d *pyDict) setdefault(k string, v any) {
	if _, ok := d.vals[k]; !ok {
		d.set(k, v)
	}
}

func (d *pyDict) del(k string) {
	if _, ok := d.vals[k]; !ok {
		return
	}
	delete(d.vals, k)
	for i, key := range d.keys {
		if key == k {
			d.keys = append(d.keys[:i], d.keys[i+1:]...)
			break
		}
	}
}

func (d *pyDict) len() int { return len(d.keys) }

// pyInt is a JSON integer with its exact decimal digits (Python ints are
// unbounded).
type pyInt string

var errJSON = errors.New("invalid JSON")

// jsonLoadsBytes is json.loads(bytes): the encoding is detected the way
// Python does, then the text is parsed.
func jsonLoadsBytes(b []byte) (any, error) {
	var text string
	switch {
	case hasPrefix(b, "\x00\x00\xfe\xff"), hasPrefix(b, "\xff\xfe\x00\x00"):
		return nil, errJSON // UTF-32 transcripts do not exist in practice
	case hasPrefix(b, "\xfe\xff"), hasPrefix(b, "\xff\xfe"):
		s, ok := decodeUTF16(b[2:], b[0] == 0xfe)
		if !ok {
			return nil, errJSON
		}
		text = s
	case hasPrefix(b, "\xef\xbb\xbf"):
		b = b[3:]
		if !utf8.Valid(b) {
			return nil, errJSON
		}
		text = string(b)
	case len(b) >= 4 && (b[0] == 0 || b[1] == 0):
		if (b[0] == 0 && b[1] == 0) || (b[1] == 0 && b[2] == 0 && b[3] == 0) {
			return nil, errJSON
		}
		s, ok := decodeUTF16(b, b[0] == 0)
		if !ok {
			return nil, errJSON
		}
		text = s
	case len(b) == 2 && (b[0] == 0 || b[1] == 0):
		s, ok := decodeUTF16(b, b[0] == 0)
		if !ok {
			return nil, errJSON
		}
		text = s
	default:
		if !utf8.Valid(b) {
			return nil, errJSON
		}
		text = string(b)
	}
	return parseJSON(text)
}

func hasPrefix(b []byte, p string) bool { return strings.HasPrefix(string(b), p) }

func decodeUTF16(b []byte, bigEndian bool) (string, bool) {
	if len(b)%2 != 0 {
		return "", false
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		if bigEndian {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			u[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(u)), true
}

// jsonLoadsStr is json.loads(str), which, unlike the bytes form, refuses a
// leading byte order mark.
func jsonLoadsStr(s string) (any, error) {
	if strings.HasPrefix(s, "\ufeff") {
		return nil, errJSON
	}
	return parseJSON(s)
}

func parseJSON(s string) (any, error) {
	p := jsonParser{s: s}
	p.ws()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, errJSON
	}
	return v, nil
}

type jsonParser struct {
	s string
	i int
}

func (p *jsonParser) ws() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *jsonParser) lit(word string) bool {
	if strings.HasPrefix(p.s[p.i:], word) {
		p.i += len(word)
		return true
	}
	return false
}

func (p *jsonParser) value(depth int) (any, error) {
	// Python raises RecursionError near its recursion limit; treat an
	// absurdly deep document as undecodable instead.
	if depth > 900 || p.i >= len(p.s) {
		return nil, errJSON
	}
	switch c := p.s[p.i]; {
	case c == '"':
		return p.str()
	case c == '{':
		p.i++
		d := newDict()
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
			return d, nil
		}
		for {
			if p.i >= len(p.s) || p.s[p.i] != '"' {
				return nil, errJSON
			}
			k, err := p.str()
			if err != nil {
				return nil, err
			}
			p.ws()
			if p.i >= len(p.s) || p.s[p.i] != ':' {
				return nil, errJSON
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
				return nil, errJSON
			}
			if p.s[p.i] == '}' {
				p.i++
				return d, nil
			}
			if p.s[p.i] != ',' {
				return nil, errJSON
			}
			p.i++
			p.ws()
		}
	case c == '[':
		p.i++
		list := []any{}
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == ']' {
			p.i++
			return list, nil
		}
		for {
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
			p.ws()
			if p.i >= len(p.s) {
				return nil, errJSON
			}
			if p.s[p.i] == ']' {
				p.i++
				return list, nil
			}
			if p.s[p.i] != ',' {
				return nil, errJSON
			}
			p.i++
			p.ws()
		}
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
	default:
		return p.number()
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (p *jsonParser) number() (any, error) {
	start, i, s := p.i, p.i, p.s
	if i < len(s) && s[i] == '-' {
		i++
	}
	switch {
	case i < len(s) && s[i] >= '1' && s[i] <= '9':
		for i++; i < len(s) && isDigit(s[i]); i++ {
		}
	case i < len(s) && s[i] == '0':
		i++
	default:
		return nil, errJSON
	}
	float := false
	if i+1 < len(s) && s[i] == '.' && isDigit(s[i+1]) {
		float = true
		for i += 2; i < len(s) && isDigit(s[i]); i++ {
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		e := i
		i++
		if i < len(s) && (s[i] == '-' || s[i] == '+') {
			i++
		}
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if isDigit(s[i-1]) {
			float = true
		} else {
			i = e
		}
	}
	p.i = i
	text := s[start:i]
	if float {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, errJSON
		}
		return f, nil
	}
	if len(strings.TrimPrefix(text, "-")) > 4300 {
		return nil, errJSON // Python's int string-conversion limit
	}
	if text == "-0" {
		text = "0"
	}
	return pyInt(text), nil
}

func (p *jsonParser) str() (string, error) {
	p.i++ // opening quote
	var b strings.Builder
	for {
		start := p.i
		for p.i < len(p.s) && p.s[p.i] != '"' && p.s[p.i] != '\\' && p.s[p.i] >= 0x20 {
			p.i++
		}
		b.WriteString(p.s[start:p.i])
		if p.i >= len(p.s) || p.s[p.i] < 0x20 {
			return "", errJSON
		}
		if p.s[p.i] == '"' {
			p.i++
			return b.String(), nil
		}
		p.i++
		if p.i >= len(p.s) {
			return "", errJSON
		}
		c := p.s[p.i]
		p.i++
		switch c {
		case '"', '\\', '/':
			b.WriteByte(c)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r, ok := p.hex4()
			if !ok {
				return "", errJSON
			}
			if r >= 0xd800 && r <= 0xdbff && strings.HasPrefix(p.s[p.i:], "\\u") {
				save := p.i
				p.i += 2
				if lo, ok := p.hex4(); ok && lo >= 0xdc00 && lo <= 0xdfff {
					r = utf16.DecodeRune(r, lo)
				} else {
					p.i = save
				}
			}
			// A lone surrogate has no UTF-8 form; Python keeps it, Go
			// cannot, so it becomes U+FFFD here.
			b.WriteRune(r)
		default:
			return "", errJSON
		}
	}
}

func (p *jsonParser) hex4() (rune, bool) {
	if p.i+4 > len(p.s) {
		return 0, false
	}
	v, err := strconv.ParseUint(p.s[p.i:p.i+4], 16, 32)
	if err != nil {
		return 0, false
	}
	p.i += 4
	return rune(v), true
}

// jsonDumps is json.dumps(v, ensure_ascii=ascii) with the default separators.
func jsonDumps(v any, ascii bool) string {
	var b strings.Builder
	dumpValue(&b, v, ascii)
	return b.String()
}

func dumpValue(b *strings.Builder, v any, ascii bool) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case pyInt:
		b.WriteString(string(x))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		switch {
		case math.IsNaN(x):
			b.WriteString("NaN")
		case math.IsInf(x, 1):
			b.WriteString("Infinity")
		case math.IsInf(x, -1):
			b.WriteString("-Infinity")
		default:
			b.WriteString(floatRepr(x))
		}
	case string:
		dumpString(b, x, ascii)
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			dumpValue(b, item, ascii)
		}
		b.WriteByte(']')
	case []string:
		items := make([]any, len(x))
		for i, s := range x {
			items[i] = s
		}
		dumpValue(b, items, ascii)
	case *pyDict:
		b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			dumpString(b, k, ascii)
			b.WriteString(": ")
			dumpValue(b, x.vals[k], ascii)
		}
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("helpers: cannot encode %T", v))
	}
}

func dumpString(b *strings.Builder, s string, ascii bool) {
	b.WriteByte('"')
	for _, r := range s {
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
			case r < 0x20:
				fmt.Fprintf(b, `\u%04x`, r)
			case !ascii || (r >= 0x20 && r <= 0x7e):
				b.WriteRune(r)
			case r < 0x10000:
				fmt.Fprintf(b, `\u%04x`, r)
			default:
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(b, `\u%04x\u%04x`, hi, lo)
			}
		}
	}
	b.WriteByte('"')
}

// floatRepr is Python's repr(float): the shortest round-tripping digits,
// scientific notation outside 1e-4 <= |x| < 1e16, and always a decimal
// point otherwise.
func floatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	mant, expText, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expText)
	digits := strings.Replace(mant, ".", "", 1)
	decpt := exp + 1
	if decpt <= -4 || decpt > 16 {
		out := digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		esign := "+"
		if exp < 0 {
			esign, exp = "-", -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, out, esign, exp)
	}
	switch {
	case decpt <= 0:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
	default:
		return sign + digits[:decpt] + "." + digits[decpt:]
	}
}

// pyStr is str(v) for a decoded JSON value.
func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyRepr(v)
}

// pyRepr is repr(v) for a decoded JSON value.
func pyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case pyInt:
		return string(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return floatRepr(x)
	case string:
		return strRepr(x)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = pyRepr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *pyDict:
		parts := make([]string, len(x.keys))
		for i, k := range x.keys {
			parts[i] = strRepr(k) + ": " + pyRepr(x.vals[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

// strRepr is repr(str).
func strRepr(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range s {
		switch {
		case r == quote || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < ' ' || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f || pyPrintable(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}

// pyPrintable is str.isprintable for one character: not a control, format,
// surrogate, private-use, unassigned or separator character (bar space).
func pyPrintable(r rune) bool {
	if r == ' ' {
		return true
	}
	if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Cs, unicode.Co, unicode.Zs, unicode.Zl, unicode.Zp) {
		return false
	}
	return unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S)
}

// truthy is Python's bool(v) for a decoded JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case pyInt:
		return x != "0"
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *pyDict:
		return x.len() > 0
	}
	return true
}

// pyEqualInt is v == n in Python for a decoded JSON value: 5, 5.0 and, for
// 0 and 1, false and true all compare equal to the int.
func pyEqualInt(v any, n int64) bool {
	switch x := v.(type) {
	case pyInt:
		return string(x) == strconv.FormatInt(n, 10)
	case float64:
		return x == float64(n) && math.Abs(x) < 1<<62
	case bool:
		return (x && n == 1) || (!x && n == 0)
	}
	return false
}

// isStr is isinstance(v, str).
func isStr(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// pyIsSpace is str.isspace for one character; Go's unicode.IsSpace lacks
// the four information separators Python counts.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip is str.strip().
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pySplit is str.split() with no separator.
func pySplit(s string) []string { return strings.FieldsFunc(s, pyIsSpace) }

// runeLen is len(str).
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// runePrefix is s[:n] on a Python str.
func runePrefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

// runeSlice is s[start:end] on a Python str with non-negative bounds.
func runeSlice(s string, start, end int) string {
	rs := []rune(s)
	if end > len(rs) {
		end = len(rs)
	}
	if start > end {
		return ""
	}
	return string(rs[start:end])
}

// runeIndex is str.find, counted in characters.
func runeIndex(s, sub string) int {
	i := strings.Index(s, sub)
	if i < 0 {
		return -1
	}
	return utf8.RuneCountInString(s[:i])
}

// pyIntParse is int(text) for a decimal string, with Python's error text.
func pyIntParse(text string) (int64, error) {
	s := strings.TrimSpace(pyNumberASCII(text))
	body := strings.TrimLeft(s, "+-")
	ok := len(s)-len(body) <= 1 && body != "" && !strings.HasPrefix(body, "_") && !strings.HasSuffix(body, "_") && !strings.Contains(body, "__")
	if ok {
		for _, r := range body {
			if r != '_' && (r < '0' || r > '9') {
				ok = false
				break
			}
		}
	}
	if ok {
		if n, err := strconv.ParseInt(strings.ReplaceAll(s, "_", ""), 10, 64); err == nil {
			return n, nil
		}
	}
	return 0, pyError("ValueError", "invalid literal for int() with base 10: "+strRepr(text))
}
