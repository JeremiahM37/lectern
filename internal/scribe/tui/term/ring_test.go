package term

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// The reference keeps everything and takes the tail, which is what the ring
// must equal after any sequence of writes.
func tailOf(all []byte, n int) []byte {
	if len(all) <= n {
		return all
	}
	return all[len(all)-n:]
}

func TestRawRingKeepsLatestBytesAcrossManyWrites(t *testing.T) {
	const size = 16384
	r := newRawRing(size)
	var all []byte
	// Varied sizes, including writes that wrap the ring several times and
	// writes at least as large as the whole ring.
	sizes := []int{1, 7, 100, 4095, 4096, 9000, 16383, 16384, 16385, 40000, 3, 0, 12000}
	for round := 0; round < 40; round++ {
		for i, n := range sizes {
			chunk := make([]byte, n)
			for j := range chunk {
				chunk[j] = byte('a' + (round*31+i*7+j)%26)
			}
			all = append(all, chunk...)
			if _, err := r.Write(chunk); err != nil {
				t.Fatal(err)
			}
			if got, want := r.Bytes(), tailOf(all, size); !bytes.Equal(got, want) {
				t.Fatalf("after %d bytes: ring holds %d bytes, want the last %d; contents differ",
					len(all), len(got), len(want))
			}
		}
	}
	if got := len(r.Bytes()); got != size {
		t.Fatalf("ring holds %d bytes after overflow, want %d", got, size)
	}
}

func TestRawRingBytesIsACopy(t *testing.T) {
	r := newRawRing(8)
	_, _ = r.Write([]byte("abcdef"))
	got := r.Bytes()
	got[0] = 'X'
	if string(r.Bytes()) != "abcdef" {
		t.Fatalf("mutating Bytes() changed the ring: %q", r.Bytes())
	}
}

func TestRawRingEmpty(t *testing.T) {
	if got := newRawRing(16).Bytes(); len(got) != 0 {
		t.Fatalf("empty ring returned %q", got)
	}
}

func TestPaneKeepsLast16KOfProgramOutput(t *testing.T) {
	p := startPTY(t, 40, 4, "sh", "-c", "seq 1 20000")
	// seq prints one line per number and the pty turns each newline into CRLF.
	var all strings.Builder
	for i := 1; i <= 20000; i++ {
		fmt.Fprintf(&all, "%d\r\n", i)
	}
	want := tailOf([]byte(all.String()), RawRingSize)

	waitFor(t, "end of output", func() bool {
		return bytes.HasSuffix(p.RawOutput(), []byte("20000\r\n"))
	})
	got := p.RawOutput()
	if len(got) != RawRingSize {
		t.Fatalf("RawOutput is %d bytes, want %d", len(got), RawRingSize)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("RawOutput is not the last 16 KB of the program's output")
	}
}

func TestPaneRawOutputKeepsEscapeSequences(t *testing.T) {
	p := startPTY(t, 20, 4, "sh", "-c", `printf '\033[31mred\033[0m'; sleep 5`)
	waitFor(t, "output", func() bool { return bytes.Contains(p.RawOutput(), []byte("red")) })
	if !bytes.Contains(p.RawOutput(), []byte("\x1b[31mred")) {
		t.Fatalf("raw output lost the SGR sequence: %q", p.RawOutput())
	}
}
