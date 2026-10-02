package ptyhost

import (
	"bytes"
	"testing"
)

func TestAttachKeysAtEveryPacketBoundary(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		detach   bool
	}{
		{"hello\x1ddignored", "hello", true},
		{"\x1d\x1dtext", "\x1dtext", false},
		{"\x1dq", "\x1dq", false},
		{"before\x1cafter", "beforeafter", false},
		{"\x1d\x1c", "\x1d", false},
	} {
		for split := 0; split <= len(tc.in); split++ {
			var keys attachKeys
			out, detached := keys.feed([]byte(tc.in[:split]))
			if !detached {
				b, d := keys.feed([]byte(tc.in[split:]))
				out = append(out, b...)
				detached = d
			}
			if !bytes.Equal(out, []byte(tc.want)) || detached != tc.detach {
				t.Fatalf("%q split %d: %q detached=%v", tc.in, split, out, detached)
			}
		}
	}
}
