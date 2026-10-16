package repomap

import (
	"encoding/binary"
	"errors"
)

// The cache stores one fileInfo per row. JSON decoding of those rows was
// about 40% of a warm rebuild, so rows use a small hand-written binary layout
// instead: a format byte, then uvarint counts and length-prefixed strings.
// Decoding is bounds-checked. Any truncated or unknown row fails, and the
// caller parses the file again, so a damaged cache costs time, not data.

// codecVersion is the first byte of every row. Bump it with schemaVersion.
const codecVersion = 1

var errCorruptRow = errors.New("repomap: corrupt cache row")

type encoder struct{ b []byte }

func (e *encoder) uv(x uint64)  { e.b = binary.AppendUvarint(e.b, x) }
func (e *encoder) int(x int)    { e.b = binary.AppendVarint(e.b, int64(x)) }
func (e *encoder) str(s string) { e.uv(uint64(len(s))); e.b = append(e.b, s...) }

func (e *encoder) flag(v bool) {
	if v {
		e.b = append(e.b, 1)
	} else {
		e.b = append(e.b, 0)
	}
}

func (e *encoder) strs(ss []string) {
	e.uv(uint64(len(ss)))
	for _, s := range ss {
		e.str(s)
	}
}

// encodeInfo serialises the extraction of one file. Symbol byte offsets are
// not stored; they only serve extraction.
func encodeInfo(fi *fileInfo) ([]byte, error) {
	e := &encoder{b: make([]byte, 0, 128+48*len(fi.Symbols))}
	e.b = append(e.b, codecVersion)
	e.str(fi.Path)
	e.str(fi.Lang)
	e.int(fi.Lines)
	e.str(fi.Doc)
	e.uv(uint64(len(fi.Symbols)))
	for _, s := range fi.Symbols {
		e.str(s.Name)
		e.str(s.Kind)
		e.str(s.File)
		e.int(s.Line)
		e.int(s.EndLine)
		e.str(s.Signature)
		e.flag(s.Exported)
		e.str(s.Parent)
		e.str(s.BodyHash)
		e.str(s.calls)
	}
	e.uv(uint64(len(fi.Imports)))
	for _, im := range fi.Imports {
		e.str(im.Kind)
		e.str(im.Spec)
		e.strs(im.Names)
	}
	e.uv(uint64(len(fi.Idents)))
	for name, n := range fi.Idents {
		e.str(name)
		e.uv(uint64(n))
	}
	return e.b, nil
}

type decoder struct {
	b   []byte
	err error
	// intern shares one copy of each short string. Kinds, paths and names
	// repeat across a file's symbols and imports, so this saves most of the
	// allocations a row decode would make.
	intern map[string]string
}

func (d *decoder) fail() {
	if d.err == nil {
		d.err = errCorruptRow
	}
	d.b = nil
}

func (d *decoder) uv() uint64 {
	if d.err != nil {
		return 0
	}
	x, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.fail()
		return 0
	}
	d.b = d.b[n:]
	return x
}

func (d *decoder) int() int {
	if d.err != nil {
		return 0
	}
	x, n := binary.Varint(d.b)
	if n <= 0 {
		d.fail()
		return 0
	}
	d.b = d.b[n:]
	return int(x)
}

// count reads an item count. Every item takes at least one byte, so a count
// larger than the remaining input is corrupt; checking first keeps a bad row
// from asking for a huge allocation.
func (d *decoder) count() int {
	n := d.uv()
	if d.err != nil {
		return 0
	}
	if n > uint64(len(d.b)) {
		d.fail()
		return 0
	}
	return int(n)
}

func (d *decoder) str() string {
	n := d.uv()
	if d.err != nil {
		return ""
	}
	if n > uint64(len(d.b)) {
		d.fail()
		return ""
	}
	raw := d.b[:n]
	d.b = d.b[n:]
	if n > 64 {
		return string(raw)
	}
	if s, ok := d.intern[string(raw)]; ok {
		return s
	}
	s := string(raw)
	d.intern[s] = s
	return s
}

func (d *decoder) flag() bool {
	if d.err != nil {
		return false
	}
	if len(d.b) == 0 {
		d.fail()
		return false
	}
	v := d.b[0] != 0
	d.b = d.b[1:]
	return v
}

func (d *decoder) strs() []string {
	n := d.count()
	if n == 0 || d.err != nil {
		return nil
	}
	out := make([]string, n)
	for i := range out {
		out[i] = d.str()
	}
	return out
}

// decodeInfo parses a row written by encodeInfo.
func decodeInfo(b []byte) (*fileInfo, error) {
	if len(b) == 0 || b[0] != codecVersion {
		return nil, errCorruptRow
	}
	d := &decoder{b: b[1:], intern: make(map[string]string, 64)}
	fi := &fileInfo{}
	fi.Path = d.str()
	fi.Lang = d.str()
	fi.Lines = d.int()
	fi.Doc = d.str()
	if n := d.count(); n > 0 && d.err == nil {
		fi.Symbols = make([]Symbol, n)
		for i := range fi.Symbols {
			s := &fi.Symbols[i]
			s.Name = d.str()
			s.Kind = d.str()
			s.File = d.str()
			s.Line = d.int()
			s.EndLine = d.int()
			s.Signature = d.str()
			s.Exported = d.flag()
			s.Parent = d.str()
			s.BodyHash = d.str()
			s.calls = d.str()
		}
	}
	if n := d.count(); n > 0 && d.err == nil {
		fi.Imports = make([]rawImport, n)
		for i := range fi.Imports {
			fi.Imports[i] = rawImport{Kind: d.str(), Spec: d.str(), Names: d.strs()}
		}
	}
	n := d.count()
	fi.Idents = make(map[string]int, n)
	for i := 0; i < n && d.err == nil; i++ {
		name := d.str()
		fi.Idents[name] = int(d.uv())
	}
	if d.err != nil {
		return nil, d.err
	}
	if len(d.b) != 0 {
		return nil, errCorruptRow
	}
	return fi, nil
}
