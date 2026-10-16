package nvim

// A small MessagePack codec, enough for Neovim's msgpack-RPC: nil, bool,
// integers, floats, strings, binary, arrays, maps and extension values.
// Decoded values are nil, bool, int64, uint64 (only above MaxInt64), float64,
// string, []byte, []any, map[string]any and Ext.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Ext is a MessagePack extension value. Neovim uses extension types for
// buffer, window and tabpage handles.
type Ext struct {
	Type int8
	Data []byte
}

// maxDepth bounds nesting while decoding, so a hostile peer cannot exhaust
// the stack.
const maxDepth = 128

var errTooDeep = errors.New("msgpack: nesting too deep")

// encode appends the MessagePack form of v to b.
func encode(b []byte, v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return append(b, 0xc0), nil
	case bool:
		if x {
			return append(b, 0xc3), nil
		}
		return append(b, 0xc2), nil
	case int:
		return encodeInt(b, int64(x)), nil
	case int8:
		return encodeInt(b, int64(x)), nil
	case int16:
		return encodeInt(b, int64(x)), nil
	case int32:
		return encodeInt(b, int64(x)), nil
	case int64:
		return encodeInt(b, x), nil
	case uint:
		return encodeUint(b, uint64(x)), nil
	case uint8:
		return encodeUint(b, uint64(x)), nil
	case uint16:
		return encodeUint(b, uint64(x)), nil
	case uint32:
		return encodeUint(b, uint64(x)), nil
	case uint64:
		return encodeUint(b, x), nil
	case float32:
		b = append(b, 0xca)
		return binary.BigEndian.AppendUint32(b, math.Float32bits(x)), nil
	case float64:
		b = append(b, 0xcb)
		return binary.BigEndian.AppendUint64(b, math.Float64bits(x)), nil
	case string:
		return encodeStr(b, x), nil
	case []byte:
		return encodeBin(b, x), nil
	case Ext:
		return encodeExt(b, x), nil
	case []any:
		b = encodeLen(b, len(x), 0x90, 0xdc, 0xdd)
		var err error
		for _, e := range x {
			if b, err = encode(b, e); err != nil {
				return nil, err
			}
		}
		return b, nil
	case map[string]any:
		b = encodeLen(b, len(x), 0x80, 0xde, 0xdf)
		var err error
		for k, e := range x {
			b = encodeStr(b, k)
			if b, err = encode(b, e); err != nil {
				return nil, err
			}
		}
		return b, nil
	}
	return nil, fmt.Errorf("msgpack: cannot encode %T", v)
}

func encodeInt(b []byte, v int64) []byte {
	switch {
	case v >= 0:
		return encodeUint(b, uint64(v))
	case v >= -32:
		return append(b, byte(int8(v)))
	case v >= math.MinInt8:
		return append(b, 0xd0, byte(int8(v)))
	case v >= math.MinInt16:
		return binary.BigEndian.AppendUint16(append(b, 0xd1), uint16(int16(v)))
	case v >= math.MinInt32:
		return binary.BigEndian.AppendUint32(append(b, 0xd2), uint32(int32(v)))
	default:
		return binary.BigEndian.AppendUint64(append(b, 0xd3), uint64(v))
	}
}

func encodeUint(b []byte, v uint64) []byte {
	switch {
	case v < 128:
		return append(b, byte(v))
	case v <= math.MaxUint8:
		return append(b, 0xcc, byte(v))
	case v <= math.MaxUint16:
		return binary.BigEndian.AppendUint16(append(b, 0xcd), uint16(v))
	case v <= math.MaxUint32:
		return binary.BigEndian.AppendUint32(append(b, 0xce), uint32(v))
	default:
		return binary.BigEndian.AppendUint64(append(b, 0xcf), v)
	}
}

func encodeLen(b []byte, n int, fix, c16, c32 byte) []byte {
	switch {
	case fix == 0x90 && n < 16, fix == 0x80 && n < 16:
		return append(b, fix|byte(n))
	case n <= math.MaxUint16:
		return binary.BigEndian.AppendUint16(append(b, c16), uint16(n))
	default:
		return binary.BigEndian.AppendUint32(append(b, c32), uint32(n))
	}
}

func encodeStr(b []byte, s string) []byte {
	n := len(s)
	switch {
	case n < 32:
		b = append(b, 0xa0|byte(n))
	case n <= math.MaxUint8:
		b = append(b, 0xd9, byte(n))
	case n <= math.MaxUint16:
		b = binary.BigEndian.AppendUint16(append(b, 0xda), uint16(n))
	default:
		b = binary.BigEndian.AppendUint32(append(b, 0xdb), uint32(n))
	}
	return append(b, s...)
}

func encodeBin(b []byte, p []byte) []byte {
	n := len(p)
	switch {
	case n <= math.MaxUint8:
		b = append(b, 0xc4, byte(n))
	case n <= math.MaxUint16:
		b = binary.BigEndian.AppendUint16(append(b, 0xc5), uint16(n))
	default:
		b = binary.BigEndian.AppendUint32(append(b, 0xc6), uint32(n))
	}
	return append(b, p...)
}

func encodeExt(b []byte, e Ext) []byte {
	n := len(e.Data)
	switch n {
	case 1:
		b = append(b, 0xd4, byte(e.Type))
	case 2:
		b = append(b, 0xd5, byte(e.Type))
	case 4:
		b = append(b, 0xd6, byte(e.Type))
	case 8:
		b = append(b, 0xd7, byte(e.Type))
	case 16:
		b = append(b, 0xd8, byte(e.Type))
	default:
		switch {
		case n <= math.MaxUint8:
			b = append(b, 0xc7, byte(n), byte(e.Type))
		case n <= math.MaxUint16:
			b = binary.BigEndian.AppendUint16(append(b, 0xc8), uint16(n))
			b = append(b, byte(e.Type))
		default:
			b = binary.BigEndian.AppendUint32(append(b, 0xc9), uint32(n))
			b = append(b, byte(e.Type))
		}
	}
	return append(b, e.Data...)
}

// decoder reads MessagePack values from a stream.
type decoder struct {
	r *bufio.Reader
}

func (d *decoder) read(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(d.r, buf); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return buf, nil
}

func (d *decoder) u(n int) (uint64, error) {
	buf, err := d.read(n)
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, c := range buf {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

// decode reads one value.
func (d *decoder) decode(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errTooDeep
	}
	c, err := d.r.ReadByte()
	if err != nil {
		return nil, err
	}
	switch {
	case c < 0x80:
		return int64(c), nil
	case c >= 0xe0:
		return int64(int8(c)), nil
	case c&0xf0 == 0x80:
		return d.mapOf(int(c&0x0f), depth)
	case c&0xf0 == 0x90:
		return d.arrayOf(int(c&0x0f), depth)
	case c&0xe0 == 0xa0:
		return d.strOf(int(c & 0x1f))
	}
	switch c {
	case 0xc0:
		return nil, nil
	case 0xc2:
		return false, nil
	case 0xc3:
		return true, nil
	case 0xc4, 0xd9: // bin8 / str8
		n, err := d.u(1)
		if err != nil {
			return nil, err
		}
		return d.blob(c == 0xc4, int(n))
	case 0xc5, 0xda:
		n, err := d.u(2)
		if err != nil {
			return nil, err
		}
		return d.blob(c == 0xc5, int(n))
	case 0xc6, 0xdb:
		n, err := d.u(4)
		if err != nil {
			return nil, err
		}
		return d.blob(c == 0xc6, int(n))
	case 0xca:
		v, err := d.u(4)
		return float64(math.Float32frombits(uint32(v))), err
	case 0xcb:
		v, err := d.u(8)
		return math.Float64frombits(v), err
	case 0xcc:
		v, err := d.u(1)
		return int64(v), err
	case 0xcd:
		v, err := d.u(2)
		return int64(v), err
	case 0xce:
		v, err := d.u(4)
		return int64(v), err
	case 0xcf:
		v, err := d.u(8)
		if err != nil {
			return nil, err
		}
		if v > math.MaxInt64 {
			return v, nil
		}
		return int64(v), nil
	case 0xd0:
		v, err := d.u(1)
		return int64(int8(v)), err
	case 0xd1:
		v, err := d.u(2)
		return int64(int16(v)), err
	case 0xd2:
		v, err := d.u(4)
		return int64(int32(v)), err
	case 0xd3:
		v, err := d.u(8)
		return int64(v), err
	case 0xd4:
		return d.extOf(1)
	case 0xd5:
		return d.extOf(2)
	case 0xd6:
		return d.extOf(4)
	case 0xd7:
		return d.extOf(8)
	case 0xd8:
		return d.extOf(16)
	case 0xc7:
		n, err := d.u(1)
		if err != nil {
			return nil, err
		}
		return d.extVar(int(n))
	case 0xc8:
		n, err := d.u(2)
		if err != nil {
			return nil, err
		}
		return d.extVar(int(n))
	case 0xc9:
		n, err := d.u(4)
		if err != nil {
			return nil, err
		}
		return d.extVar(int(n))
	case 0xdc, 0xdd:
		w := 2
		if c == 0xdd {
			w = 4
		}
		n, err := d.u(w)
		if err != nil {
			return nil, err
		}
		return d.arrayOf(int(n), depth)
	case 0xde, 0xdf:
		w := 2
		if c == 0xdf {
			w = 4
		}
		n, err := d.u(w)
		if err != nil {
			return nil, err
		}
		return d.mapOf(int(n), depth)
	}
	return nil, fmt.Errorf("msgpack: unsupported type byte 0x%02x", c)
}

func (d *decoder) strOf(n int) (any, error) {
	buf, err := d.read(n)
	if err != nil {
		return nil, err
	}
	return string(buf), nil
}

func (d *decoder) blob(bin bool, n int) (any, error) {
	buf, err := d.read(n)
	if err != nil {
		return nil, err
	}
	if bin {
		return buf, nil
	}
	return string(buf), nil
}

func (d *decoder) arrayOf(n, depth int) (any, error) {
	out := make([]any, 0, min(n, 1024))
	for i := 0; i < n; i++ {
		v, err := d.decode(depth + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (d *decoder) mapOf(n, depth int) (any, error) {
	out := make(map[string]any, min(n, 1024))
	for i := 0; i < n; i++ {
		k, err := d.decode(depth + 1)
		if err != nil {
			return nil, err
		}
		ks, ok := k.(string)
		if !ok {
			return nil, fmt.Errorf("msgpack: map key of type %T, want string", k)
		}
		v, err := d.decode(depth + 1)
		if err != nil {
			return nil, err
		}
		out[ks] = v
	}
	return out, nil
}

func (d *decoder) extOf(n int) (any, error) {
	t, err := d.read(1)
	if err != nil {
		return nil, err
	}
	data, err := d.read(n)
	if err != nil {
		return nil, err
	}
	return Ext{Type: int8(t[0]), Data: data}, nil
}

func (d *decoder) extVar(n int) (any, error) {
	t, err := d.read(1)
	if err != nil {
		return nil, err
	}
	data, err := d.read(n)
	if err != nil {
		return nil, err
	}
	return Ext{Type: int8(t[0]), Data: data}, nil
}

// Unpack decodes one MessagePack value from b, which must hold exactly one.
func Unpack(b []byte) (any, error) {
	d := &decoder{r: bufio.NewReader(bytesReader(b))}
	return d.decode(0)
}

// Pack encodes v.
func Pack(v any) ([]byte, error) { return encode(nil, v) }
