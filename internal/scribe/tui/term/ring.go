package term

import "sync"

// RawRingSize is how many bytes of a program's raw output a pane keeps for
// recent-output readers. It is bytes, not lines: the scrollback already holds
// rendered lines, and this keeps the escape sequences as the program sent them.
const RawRingSize = 16 << 10

// rawRing keeps the newest bytes written to it, up to its capacity. It is
// fed by the pane's reader goroutine and read from anywhere, so it has its
// own lock rather than sharing the emulator's.
type rawRing struct {
	mu    sync.Mutex
	buf   []byte
	start int // index of the oldest byte once the ring is full
	n     int // bytes held, at most len(buf)
}

func newRawRing(size int) *rawRing {
	return &rawRing{buf: make([]byte, size)}
}

// Write appends p, dropping the oldest bytes when the ring is full. It always
// reports len(p) written.
func (r *rawRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	size := len(r.buf)
	if size == 0 {
		return len(p), nil
	}
	// A write at least as large as the ring replaces all of it.
	if len(p) >= size {
		copy(r.buf, p[len(p)-size:])
		r.start = 0
		r.n = size
		return len(p), nil
	}
	end := (r.start + r.n) % size
	first := copy(r.buf[end:], p)
	copy(r.buf, p[first:])
	if r.n+len(p) <= size {
		r.n += len(p)
	} else {
		// The write overwrote the oldest bytes, so the oldest moves forward.
		r.start = (r.start + r.n + len(p) - size) % size
		r.n = size
	}
	return len(p), nil
}

// Bytes returns a copy of the held bytes, oldest first.
func (r *rawRing) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, r.n)
	size := len(r.buf)
	if r.n == 0 {
		return out
	}
	first := min(r.n, size-r.start)
	copy(out, r.buf[r.start:r.start+first])
	copy(out[first:], r.buf[:r.n-first])
	return out
}
