package helpers

// datetime.fromisoformat(text).timestamp() and float(text) as Python 3.13
// computes them, for catalog agents whose session listings carry creation
// times as strings.

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
)

// isoTimestamp is datetime.fromisoformat(text), made UTC when naive, then
// .timestamp(). ok is false where Python raised ValueError.
func isoTimestamp(text string) (float64, bool) {
	if runeLen(text) < 7 {
		return 0, false
	}
	s := text
	at := func(i int) byte {
		if i < len(s) {
			return s[i]
		}
		return 0
	}
	sep := isoSeparator(s, at)
	if sep < 0 {
		return 0, false
	}
	year, month, day, ok := isoDate(s, sep, at)
	if !ok {
		return 0, false
	}
	var hour, minute, second, micro, tzOffset, tzMicro int
	if len(s) > sep {
		// The separator may be any one character; in UTF-8 its lead byte
		// says how many bytes to skip.
		width := 1
		switch c := s[sep]; {
		case c&0x80 == 0:
		case c&0xf0 == 0xe0:
			width = 3
		case c&0xf0 == 0xf0:
			width = 4
		default:
			width = 2
		}
		var rv int
		rv, hour, minute, second, micro, tzOffset, tzMicro = isoTime(s[min(len(s), sep+width):])
		if rv < 0 {
			return 0, false
		}
	}
	if year < 1 || year > 9999 || month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month) ||
		hour > 23 || minute > 59 || second > 59 {
		return 0, false
	}
	// timezone() refuses offsets of a day or more.
	if off := int64(tzOffset)*1_000_000 + int64(tzMicro); off <= -86400_000_000 || off >= 86400_000_000 {
		return 0, false
	}
	days := int64(ymdToOrd(year, month, day) - ymdToOrd(1970, 1, 1))
	us := big.NewInt(days*86400 + int64(hour*3600+minute*60+second) - int64(tzOffset))
	us.Mul(us, big.NewInt(1_000_000))
	us.Add(us, big.NewInt(int64(micro-tzMicro)))
	f, _ := new(big.Rat).SetFrac(us, big.NewInt(1_000_000)).Float64()
	return f, true
}

func isoSeparator(s string, at func(int) byte) int {
	n := len(s)
	if n == 7 {
		return 7
	}
	if at(4) == '-' {
		if at(5) != 'W' {
			return 10
		}
		if n < 8 {
			return -1
		}
		if n > 8 && at(8) == '-' {
			if n == 9 {
				return -1
			}
			if n > 10 && nIsDigit(at(10)) {
				return 8
			}
			return 10
		}
		return 8
	}
	if at(4) == 'W' {
		idx := 7
		for ; idx < n; idx++ {
			if !nIsDigit(s[idx]) {
				break
			}
		}
		if idx < 9 {
			return idx
		}
		if idx%2 == 0 {
			return 7
		}
		return 8
	}
	return 8
}

// digitsAt reads n ASCII digits at s[i:], as the C parser's parse_digits.
func digitsAt(at func(int) byte, i, n int) (int, int, bool) {
	v := 0
	for k := 0; k < n; k++ {
		c := at(i + k)
		if !nIsDigit(c) {
			return 0, i, false
		}
		v = v*10 + int(c-'0')
	}
	return v, i + n, true
}

func isoDate(s string, sep int, at func(int) byte) (int, int, int, bool) {
	year, p, ok := digitsAt(at, 0, 4)
	if !ok {
		return 0, 0, 0, false
	}
	usesSep := at(p) == '-'
	if usesSep {
		p++
	}
	if at(p) == 'W' {
		p++
		week, q, ok := digitsAt(at, p, 2)
		if !ok {
			return 0, 0, 0, false
		}
		p = q
		weekday := 1
		if p < sep {
			if usesSep {
				if at(p) != '-' {
					return 0, 0, 0, false
				}
				p++
			}
			if weekday, _, ok = digitsAt(at, p, 1); !ok {
				return 0, 0, 0, false
			}
		}
		return isoToYMD(year, week, weekday)
	}
	month, p, ok := digitsAt(at, p, 2)
	if !ok {
		return 0, 0, 0, false
	}
	if usesSep {
		if at(p) != '-' {
			return 0, 0, 0, false
		}
		p++
	}
	day, _, ok := digitsAt(at, p, 2)
	return year, month, day, ok
}

// isoTime parses [HH[:MM[:SS[.ffffff]]]][+HH[:MM...]]; rv is the C
// parser's return code (negative on failure).
func isoTime(t string) (rv, hour, minute, second, micro, tzOffset, tzMicro int) {
	tz := 0
	for {
		c := byte(0)
		if tz < len(t) {
			c = t[tz]
		}
		if c == 'Z' || c == '+' || c == '-' {
			break
		}
		tz++
		if tz >= len(t) {
			break
		}
	}
	at := func(i int) byte {
		if i < len(t) {
			return t[i]
		}
		return 0
	}
	rv, hour, minute, second, micro = hhmmssff(at, 0, tz)
	if rv < 0 {
		return rv, 0, 0, 0, 0, 0, 0
	}
	if tz >= len(t) {
		if rv == 1 {
			return -5, 0, 0, 0, 0, 0, 0
		}
		return 0, hour, minute, second, micro, 0, 0
	}
	if t[tz] == 'Z' {
		if at(tz+1) != 0 {
			return -5, 0, 0, 0, 0, 0, 0
		}
		return 1, hour, minute, second, micro, 0, 0
	}
	sign := 1
	if t[tz] == '-' {
		sign = -1
	}
	r, th, tm, ts, tu := hhmmssff(at, tz+1, len(t))
	if r != 0 {
		return -5, 0, 0, 0, 0, 0, 0
	}
	return 1, hour, minute, second, micro, sign * (th*3600 + tm*60 + ts), sign * tu
}

func hhmmssff(at func(int) byte, p, end int) (rv, hour, minute, second, micro int) {
	vals := [3]int{}
	hasSep := true
	i := 0
	for ; i < 3; i++ {
		v, q, ok := digitsAt(at, p, 2)
		if !ok {
			return -3, 0, 0, 0, 0
		}
		vals[i] = v
		c := at(q)
		p = q + 1
		if i == 0 {
			hasSep = c == ':'
		}
		if p >= end {
			if c != 0 {
				rv = 1
			}
			return rv, vals[0], vals[1], vals[2], 0
		}
		if hasSep && c == ':' {
			continue
		}
		if c == '.' || c == ',' {
			break
		}
		if !hasSep {
			p--
		} else {
			return -4, 0, 0, 0, 0
		}
	}
	remains := end - p
	toParse := min(remains, 6)
	v, q, ok := digitsAt(at, p, toParse)
	if !ok {
		return -3, 0, 0, 0, 0
	}
	if toParse < 6 {
		v *= []int{100000, 10000, 1000, 100, 10}[toParse-1]
	}
	for nIsDigit(at(q)) {
		q++
	}
	if at(q) != 0 {
		rv = 1
	}
	return rv, vals[0], vals[1], vals[2], v
}

func isLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

func daysInMonth(y, m int) int {
	if m == 2 && isLeap(y) {
		return 29
	}
	return []int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[m]
}

// ymdToOrd is the proleptic Gregorian ordinal, 0001-01-01 being 1.
func ymdToOrd(y, m, d int) int {
	y1 := y - 1
	days := y1*365 + y1/4 - y1/100 + y1/400
	for k := 1; k < m; k++ {
		days += daysInMonth(y, k)
	}
	return days + d
}

func ordToYMD(ord int) (int, int, int) {
	y := ord / 366
	if y < 1 {
		y = 1
	}
	for ymdToOrd(y+1, 1, 1) <= ord {
		y++
	}
	for ymdToOrd(y, 1, 1) > ord {
		y--
	}
	m := 1
	for m < 12 && ymdToOrd(y, m+1, 1) <= ord {
		m++
	}
	return y, m, ord - ymdToOrd(y, m, 1) + 1
}

func isoToYMD(year, week, day int) (int, int, int, bool) {
	if year < 1 || year > 9999 {
		return 0, 0, 0, false
	}
	if week <= 0 || week >= 53 {
		ok := false
		if week == 53 {
			first := (ymdToOrd(year, 1, 1) + 6) % 7
			ok = first == 3 || (first == 2 && isLeap(year))
		}
		if !ok {
			return 0, 0, 0, false
		}
	}
	if day <= 0 || day >= 8 {
		return 0, 0, 0, false
	}
	first := ymdToOrd(year, 1, 1)
	monday := first - (first+6)%7
	if (first+6)%7 > 3 {
		monday += 7
	}
	y, m, d := ordToYMD(monday + (week-1)*7 + day - 1)
	return y, m, d, true
}

// pyFloatParse is float(text) for a str: digits with single underscores,
// an optional exponent, or inf/infinity/nan in any case.
func pyFloatParse(text string) (float64, bool) {
	s := strings.TrimSpace(pyNumberASCII(text))
	body := strings.TrimLeft(s, "+-")
	if len(s)-len(body) > 1 || body == "" {
		return 0, false
	}
	switch strings.ToLower(body) {
	case "inf", "infinity":
		if s[0] == '-' {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	case "nan":
		return math.NaN(), true
	}
	// Underscores may only separate two digits.
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case nIsDigit(c), c == '.', c == 'e', c == 'E', c == '+', c == '-':
		case c == '_':
			if i == 0 || i == len(body)-1 || !nIsDigit(body[i-1]) || !nIsDigit(body[i+1]) {
				return 0, false
			}
		default:
			return 0, false
		}
	}
	clean := strings.ReplaceAll(s, "_", "")
	// strconv accepts hex floats and "0x", Python's float() does not.
	if strings.ContainsAny(clean, "xXpP") {
		return 0, false
	}
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f, true
		}
		return 0, false
	}
	return f, true
}

// pyNumberASCII is how int() and float() read a str: Unicode decimal
// digits become ASCII digits, Unicode whitespace a space, and any other
// non-ASCII character something no number contains.
func pyNumberASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case nPyIsSpace(r):
			b.WriteByte(' ')
		case unicode.IsDigit(r):
			k := rune(0)
			for unicode.IsDigit(r - k - 1) {
				k++
			}
			b.WriteByte(byte('0' + k%10))
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}
