package drivers

import (
	"context"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// tailer replays the scheduler's own drainEvents contract (see
// internal/scheduler/scheduler.go) for a driver's independent poll loop:
// re-read from the last consumed byte offset and hand back only complete
// lines. A trailing partial line is simply re-read next poll — the offset
// only advances past bytes a caller has actually consumed, so nothing is
// lost and nothing needs a separate in-memory carry-over buffer.
type tailer struct {
	ex     executor.Executor
	path   string
	offset int64
}

// lines returns any newly-complete lines since the last call, or "" if there
// is nothing new yet (including a trailing partial line still being written).
func (t *tailer) lines(ctx context.Context) (string, error) {
	chunk, err := t.ex.ReadFile(ctx, t.path, t.offset)
	if err != nil || len(chunk) == 0 {
		return "", err
	}
	nl := lastNewline(chunk)
	if nl < 0 {
		return "", nil
	}
	t.offset += int64(nl) + 1
	return string(chunk[:nl+1]), nil
}

func lastNewline(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i
		}
	}
	return -1
}
