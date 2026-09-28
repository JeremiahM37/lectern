package helpers

// JSON exactly as the Python helpers read and write it. A Go port has to
// print what json.dumps printed — key order, ", " separators, \u escapes,
// 1.0 rather than 1 — and some helpers rewrite a user's own file (Gemini's
// settings.json), where a float must stay a float and duplicate keys resolve
// the way Python's dict does. encoding/json does neither, so values here
// are: nil, bool, *big.Int, float64, string, []any and *pyObj (ordered).
// The encoder also takes int, int64 and []string for convenience.

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// pyObj is a dict: insertion-ordered, re-setting a key keeps its place.
type pyObj struct {
	keys []string
	vals map[string]any
}

// newObj builds an object from alternating keys and values.
func newObj(kv ...any) *pyObj {
	o := &pyObj{vals: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

func (o *pyObj) Set(k string, v any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *pyObj) Get(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[k]
	return v, ok
}

// Val is dict.get(k): nil when absent.
func (o *pyObj) Val(k string) any {
	v, _ := o.Get(k)
	return v
}

// Str is the value when it is a string, else "".
func (o *pyObj) Str(k string) string {
	s, _ := o.Val(k).(string)
	return s
}

func (o *pyObj) Has(k string) bool {
	_, ok := o.Get(k)
	return ok
}

func (o *pyObj) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, key := range o.keys {
		if key == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *pyObj) Keys() []string { return append([]string(nil), o.keys...) }

func (o *pyObj) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Copy is dict(o): a shallow copy.
func (o *pyObj) Copy() *pyObj {
	c := &pyObj{vals: map[string]any{}}
	for _, k := range o.keys {
		c.Set(k, o.vals[k])
	}
	return c
}

// Update is dict.update(other).
func (o *pyObj) Update(other *pyObj) {
	for _, k := range other.keys {
		o.Set(k, other.vals[k])
	}
}

// ---- encoding ---------------------------------------------------------------

// pyDumps is json.dumps(v) with its default separators.
func pyDumps(v any) string { return pyDumpsWith(v, ", ", ": ", -1) }

// pyDumpsCompact is json.dumps(v, separators=(',', ':')).
func pyDumpsCompact(v any) string { return pyDumpsWith(v, ",", ":", -1) }

// pyDumpsIndent is json.dumps(v, indent=n), whose item separator is ",".
func pyDumpsIndent(v any, n int) string { return pyDumpsWith(v, ",", ": ", n) }

func pyDumpsWith(v any, item, key string, indent int) string {
	var b strings.Builder
	e := pyEncoder{b: &b, item: item, key: key, indent: indent}
	e.value(v, 0)
	return b.String()
}

type pyEncoder struct {
	b         *strings.Builder
	item, key string
	indent    int
}

func (e *pyEncoder) newline(depth int) {
	if e.indent >= 0 {
		e.b.WriteByte('\n')
		e.b.WriteString(strings.Repeat(" ", e.indent*depth))
	}
}

func (e *pyEncoder) value(v any, depth int) {
	switch x := v.(type) {
	case nil:
		e.b.WriteString("null")
	case bool:
		if x {
			e.b.WriteString("true")
		} else {
			e.b.WriteString("false")
		}
	case int:
		e.b.WriteString(strconv.Itoa(x))
	case int64:
		e.b.WriteString(strconv.FormatInt(x, 10))
	case uint64:
		e.b.WriteString(strconv.FormatUint(x, 10))
	case *big.Int:
		e.b.WriteString(x.String())
	case float64:
		e.b.WriteString(pyJSONFloat(x))
	case string:
		e.b.WriteString(pyQuoteJSON(x))
	case []string:
		list := make([]any, len(x))
		for i, s := range x {
			list[i] = s
		}
		e.value(list, depth)
	case []any:
		if len(x) == 0 {
			e.b.WriteString("[]")
			return
		}
		e.b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				e.b.WriteString(e.item)
			}
			e.newline(depth + 1)
			e.value(item, depth+1)
		}
		e.newline(depth)
		e.b.WriteByte(']')
	case *pyObj:
		if x.Len() == 0 {
			e.b.WriteString("{}")
			return
		}
		e.b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				e.b.WriteString(e.item)
			}
			e.newline(depth + 1)
			e.b.WriteString(pyQuoteJSON(k))
			e.b.WriteString(e.key)
			e.value(x.vals[k], depth+1)
		}
		e.newline(depth)
		e.b.WriteByte('}')
	default:
		panic(fmt.Sprintf("pyDumps: unsupported %T", v))
	}
}

// pyJSONFloat is how json.dumps writes a float: repr, with NaN and the
// infinities spelled the way JavaScript does.
func pyJSONFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return wPyFloatRepr(f)
}

// wPyFloatRepr is repr(float): the shortest round-tripping digits, in
// positional form while the decimal point sits within 16 digits, else
// exponential with at least two exponent digits.
func wPyFloatRepr(f float64) string {
	if math.IsNaN(f) {
		return "nan"
	}
	if math.IsInf(f, 0) {
		if f > 0 {
			return "inf"
		}
		return "-inf"
	}
	sign := ""
	if math.Signbit(f) {
		sign = "-"
		f = -f
	}
	if f == 0 {
		return sign + "0.0"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
	mant, expText, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expText)
	digits := strings.Replace(mant, ".", "", 1)
	decpt := exp + 1
	if decpt > -4 && decpt <= 16 {
		switch {
		case decpt <= 0:
			return sign + "0." + strings.Repeat("0", -decpt) + digits
		case decpt >= len(digits):
			return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
		default:
			return sign + digits[:decpt] + "." + digits[decpt:]
		}
	}
	m := digits[:1]
	if len(digits) > 1 {
		m += "." + digits[1:]
	}
	es := "+"
	if exp < 0 {
		es = "-"
		exp = -exp
	}
	return fmt.Sprintf("%s%se%s%02d", sign, m, es, exp)
}

// pyQuoteJSON is json.dumps(s) with ensure_ascii. Invalid UTF-8 bytes are
// the surrogate escapes Python decoded them to (a file name that is not
// UTF-8), and an encoded surrogate is the lone surrogate a JSON input held.
func pyQuoteJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
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
				if c < 0x20 || c == 0x7f {
					fmt.Fprintf(&b, `\u%04x`, c)
				} else {
					b.WriteByte(c)
				}
			}
			i++
			continue
		}
		if sur, ok := wtf8Surrogate(s[i:]); ok {
			fmt.Fprintf(&b, `\u%04x`, sur)
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			fmt.Fprintf(&b, `\u%04x`, 0xdc00+int(c))
			i++
			continue
		}
		if r > 0xffff {
			r1, r2 := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, r1, r2)
		} else {
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		i += size
	}
	b.WriteByte('"')
	return b.String()
}

// wtf8Surrogate reads a surrogate code point encoded like any other
// three-byte sequence (how pyLoads keeps a lone \ud800 it parsed).
func wtf8Surrogate(s string) (rune, bool) {
	if len(s) < 3 || s[0] != 0xed || s[1] < 0xa0 || s[1] > 0xbf || s[2] < 0x80 || s[2] > 0xbf {
		return 0, false
	}
	return rune(0xd000) | rune(s[1]&0x3f)<<6 | rune(s[2]&0x3f), true
}

func wtf8Encode(r rune) string {
	return string([]byte{0xe0 | byte(r>>12), 0x80 | byte(r>>6)&0x3f, 0x80 | byte(r)&0x3f})
}

// ---- decoding ---------------------------------------------------------------

// pyJSONError is json.JSONDecodeError; its text is Python's.
type pyJSONError struct{ msg string }

func (e *pyJSONError) Error() string { return e.msg }

// pyLoads is json.loads(s).
func pyLoads(s string) (any, error) {
	d := &pyDecoder{doc: []rune(s)}
	d.ws()
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	d.ws()
	if d.pos != len(d.doc) {
		return nil, d.fail("Extra data", d.pos)
	}
	return v, nil
}

type pyDecoder struct {
	doc []rune
	pos int
}

func (d *pyDecoder) fail(msg string, pos int) error {
	line := 1
	last := -1
	for i := 0; i < pos && i < len(d.doc); i++ {
		if d.doc[i] == '\n' {
			line++
			last = i
		}
	}
	return &pyJSONError{fmt.Sprintf("%s: line %d column %d (char %d)", msg, line, pos-last, pos)}
}

func (d *pyDecoder) ws() {
	for d.pos < len(d.doc) {
		switch d.doc[d.pos] {
		case ' ', '\t', '\n', '\r':
			d.pos++
		default:
			return
		}
	}
}

func (d *pyDecoder) peek() rune {
	if d.pos < len(d.doc) {
		return d.doc[d.pos]
	}
	return -1
}

func (d *pyDecoder) has(lit string) bool {
	r := []rune(lit)
	if d.pos+len(r) > len(d.doc) {
		return false
	}
	for i, c := range r {
		if d.doc[d.pos+i] != c {
			return false
		}
	}
	return true
}

func (d *pyDecoder) value() (any, error) {
	start := d.pos
	switch c := d.peek(); {
	case c == '"':
		return d.str()
	case c == '{':
		return d.object()
	case c == '[':
		return d.array()
	case d.has("null"):
		d.pos += 4
		return nil, nil
	case d.has("true"):
		d.pos += 4
		return true, nil
	case d.has("false"):
		d.pos += 5
		return false, nil
	case d.has("NaN"):
		d.pos += 3
		return math.NaN(), nil
	case d.has("Infinity"):
		d.pos += 8
		return math.Inf(1), nil
	case d.has("-Infinity"):
		d.pos += 9
		return math.Inf(-1), nil
	case c == '-' || (c >= '0' && c <= '9'):
		if v, ok := d.number(); ok {
			return v, nil
		}
	}
	return nil, d.fail("Expecting value", start)
}

func (d *pyDecoder) digits() int {
	n := 0
	for d.pos < len(d.doc) && d.doc[d.pos] >= '0' && d.doc[d.pos] <= '9' {
		d.pos++
		n++
	}
	return n
}

func (d *pyDecoder) number() (any, bool) {
	start := d.pos
	if d.peek() == '-' {
		d.pos++
	}
	switch {
	case d.peek() == '0':
		d.pos++
	case d.peek() >= '1' && d.peek() <= '9':
		d.digits()
	default:
		d.pos = start
		return nil, false
	}
	float := false
	if d.peek() == '.' {
		save := d.pos
		d.pos++
		if d.digits() == 0 {
			d.pos = save
		} else {
			float = true
		}
	}
	if c := d.peek(); c == 'e' || c == 'E' {
		save := d.pos
		d.pos++
		if c := d.peek(); c == '+' || c == '-' {
			d.pos++
		}
		if d.digits() == 0 {
			d.pos = save
		} else {
			float = true
		}
	}
	text := string(d.doc[start:d.pos])
	if float {
		f, _ := strconv.ParseFloat(text, 64) // out of range is ±Inf, as float() gives
		return f, true
	}
	n, _ := new(big.Int).SetString(text, 10)
	return n, true
}

func (d *pyDecoder) str() (string, error) {
	begin := d.pos
	d.pos++
	var b strings.Builder
	for {
		if d.pos >= len(d.doc) {
			return "", d.fail("Unterminated string starting at", begin)
		}
		c := d.doc[d.pos]
		switch {
		case c == '"':
			d.pos++
			return b.String(), nil
		case c == '\\':
			d.pos++
			if d.pos >= len(d.doc) {
				return "", d.fail("Unterminated string starting at", begin)
			}
			esc := d.doc[d.pos]
			switch esc {
			case '"', '\\', '/':
				b.WriteRune(esc)
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
				r, ok := d.hex4(d.pos + 1)
				if !ok {
					return "", d.fail("Invalid \\uXXXX escape", d.pos)
				}
				d.pos += 4
				if r >= 0xd800 && r <= 0xdbff && d.pos+2 < len(d.doc) && d.doc[d.pos+1] == '\\' && d.doc[d.pos+2] == 'u' {
					if lo, ok := d.hex4(d.pos + 3); ok && lo >= 0xdc00 && lo <= 0xdfff {
						b.WriteRune(utf16.DecodeRune(r, lo))
						d.pos += 6
						break
					}
				}
				if r >= 0xd800 && r <= 0xdfff {
					b.WriteString(wtf8Encode(r))
				} else {
					b.WriteRune(r)
				}
			default:
				return "", d.fail("Invalid \\escape", d.pos-1)
			}
			d.pos++
		case c < 0x20:
			return "", d.fail("Invalid control character at", d.pos)
		default:
			if c >= 0xd800 && c <= 0xdfff {
				b.WriteString(wtf8Encode(c))
			} else {
				b.WriteRune(c)
			}
			d.pos++
		}
	}
}

func (d *pyDecoder) hex4(at int) (rune, bool) {
	if at+4 > len(d.doc) {
		return 0, false
	}
	v, err := strconv.ParseUint(string(d.doc[at:at+4]), 16, 32)
	if err != nil || strings.ContainsAny(string(d.doc[at:at+4]), "+-xX_") {
		return 0, false
	}
	return rune(v), true
}

func (d *pyDecoder) object() (any, error) {
	d.pos++
	o := &pyObj{vals: map[string]any{}}
	d.ws()
	if d.peek() == '}' {
		d.pos++
		return o, nil
	}
	for {
		if d.peek() != '"' {
			return nil, d.fail("Expecting property name enclosed in double quotes", d.pos)
		}
		k, err := d.str()
		if err != nil {
			return nil, err
		}
		d.ws()
		if d.peek() != ':' {
			return nil, d.fail("Expecting ':' delimiter", d.pos)
		}
		d.pos++
		d.ws()
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		o.Set(k, v)
		d.ws()
		switch d.peek() {
		case '}':
			d.pos++
			return o, nil
		case ',':
			comma := d.pos
			d.pos++
			d.ws()
			if d.peek() == '}' {
				return nil, d.fail("Illegal trailing comma before end of object", comma)
			}
		default:
			return nil, d.fail("Expecting ',' delimiter", d.pos)
		}
	}
}

func (d *pyDecoder) array() (any, error) {
	d.pos++
	list := []any{}
	d.ws()
	if d.peek() == ']' {
		d.pos++
		return list, nil
	}
	for {
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		list = append(list, v)
		d.ws()
		switch d.peek() {
		case ']':
			d.pos++
			return list, nil
		case ',':
			comma := d.pos
			d.pos++
			d.ws()
			if d.peek() == ']' {
				return nil, d.fail("Illegal trailing comma before end of array", comma)
			}
		default:
			return nil, d.fail("Expecting ',' delimiter", d.pos)
		}
	}
}

// ---- Python value semantics ----------------------------------------------

// wPyEqual is == between JSON values: 1 == 1.0 == True, and dicts compare
// without regard to order.
func wPyEqual(a, b any) bool {
	if an, ok := pyNumber(a); ok {
		bn, ok := pyNumber(b)
		return ok && an.Cmp(bn) == 0 && !isNaN(a) && !isNaN(b)
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !wPyEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case *pyObj:
		y, ok := b.(*pyObj)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.keys {
			yv, ok := y.Get(k)
			if !ok || !wPyEqual(x.vals[k], yv) {
				return false
			}
		}
		return true
	}
	return false
}

func isNaN(v any) bool {
	f, ok := v.(float64)
	return ok && math.IsNaN(f)
}

// pyNumber is a numeric value (bool included, as in Python) as an exact
// rational, for comparison.
func pyNumber(v any) (*big.Rat, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return big.NewRat(1, 1), true
		}
		return new(big.Rat), true
	case int:
		return new(big.Rat).SetInt64(int64(x)), true
	case int64:
		return new(big.Rat).SetInt64(x), true
	case *big.Int:
		return new(big.Rat).SetInt(x), true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			// Infinities compare equal to themselves only; NaN never.
			r := new(big.Rat)
			if math.IsInf(x, 1) {
				r.SetInt(new(big.Int).Lsh(big.NewInt(1), 2000))
			} else if math.IsInf(x, -1) {
				r.SetInt(new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 2000)))
			}
			return r, true
		}
		return new(big.Rat).SetFloat64(x), true
	}
	return nil, false
}

// wPyTruthy is bool(v).
func wPyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int:
		return x != 0
	case *big.Int:
		return x.Sign() != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *pyObj:
		return x.Len() > 0
	}
	return true
}

// pyIntValue is the value of a JSON int (bool counts, as isinstance(v, int)
// does), and whether it is one that fits.
func pyIntValue(v any) (int64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case *big.Int:
		if x.IsInt64() {
			return x.Int64(), true
		}
	case int:
		return int64(x), true
	case int64:
		return x, true
	}
	return 0, false
}

// wPyStr is str(v) for a JSON value.
func wPyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return wPyRepr(v)
}

// wPyRepr is repr(v) for a JSON value.
func wPyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case *big.Int:
		return x.String()
	case float64:
		return wPyFloatRepr(x)
	case string:
		return pyReprString(x)
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = pyReprString(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = wPyRepr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *pyObj:
		parts := make([]string, 0, x.Len())
		for _, k := range x.keys {
			parts = append(parts, pyReprString(k)+": "+wPyRepr(x.vals[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}
