package nvim

import (
	"bytes"
	"reflect"
	"testing"
)

func TestMsgpackKnownBytes(t *testing.T) {
	cases := []struct {
		v    any
		want []byte
	}{
		{nil, []byte{0xc0}},
		{true, []byte{0xc3}},
		{false, []byte{0xc2}},
		{0, []byte{0x00}},
		{127, []byte{0x7f}},
		{128, []byte{0xcc, 0x80}},
		{256, []byte{0xcd, 0x01, 0x00}},
		{-1, []byte{0xff}},
		{-33, []byte{0xd0, 0xdf}},
		{-200, []byte{0xd1, 0xff, 0x38}},
		{"hi", []byte{0xa2, 'h', 'i'}},
		{[]byte{1, 2}, []byte{0xc4, 2, 1, 2}},
		{[]any{int64(1), "a"}, []byte{0x92, 0x01, 0xa1, 'a'}},
		{map[string]any{"a": true}, []byte{0x81, 0xa1, 'a', 0xc3}},
		{Ext{Type: 0, Data: []byte{7}}, []byte{0xd4, 0x00, 0x07}},
	}
	for _, c := range cases {
		got, err := Pack(c.v)
		if err != nil {
			t.Fatalf("pack %v: %v", c.v, err)
		}
		if !bytes.Equal(got, c.want) {
			t.Fatalf("pack %#v = % x, want % x", c.v, got, c.want)
		}
	}
}

func TestMsgpackRoundTrip(t *testing.T) {
	long := string(bytes.Repeat([]byte("x"), 70000))
	in := []any{
		nil, true, int64(-70000), int64(1 << 40), "héllo", long,
		[]byte("raw"), 2.5, []any{}, map[string]any{"k": []any{int64(3)}},
		Ext{Type: 2, Data: []byte{1, 2, 3, 4, 5}},
	}
	b, err := Pack(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Unpack(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip changed the value:\n in %#v\nout %#v", in, out)
	}
}

func TestMsgpackUnsignedAboveInt64(t *testing.T) {
	b := []byte{0xcf, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	v, err := Unpack(b)
	if err != nil {
		t.Fatal(err)
	}
	if v != uint64(1<<64-1) {
		t.Fatalf("got %#v", v)
	}
}

func TestMsgpackRejectsTruncatedAndDeep(t *testing.T) {
	if _, err := Unpack([]byte{0xa5, 'a'}); err == nil {
		t.Fatal("truncated string decoded")
	}
	if _, err := Unpack([]byte{0xc1}); err == nil {
		t.Fatal("reserved byte decoded")
	}
	deep := bytes.Repeat([]byte{0x91}, maxDepth+5)
	deep = append(deep, 0x01)
	if _, err := Unpack(deep); err == nil {
		t.Fatal("too deep nesting decoded")
	}
	if _, err := Unpack([]byte{0x81, 0x01, 0x02}); err == nil {
		t.Fatal("non-string map key decoded")
	}
}
