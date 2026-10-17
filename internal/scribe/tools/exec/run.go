package exec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// outputLimit caps what the model reads from one shell or project
	// command, in bytes. Past it the head and tail are kept and the full
	// text goes to a tee file.
	outputLimit = 16384
	// collectLimit bounds what a foreground call keeps per stream. Output
	// past it is dropped and the result says so.
	collectLimit = 256 << 10
	// killGrace is how long a process group gets between SIGTERM and SIGKILL.
	killGrace = 500 * time.Millisecond
	// waitDelay bounds how long Wait waits for output pipes to close after
	// the leader exits, so a backgrounded grandchild cannot hang a call.
	waitDelay = 2 * time.Second
)

// procSpec describes one process run.
type procSpec struct {
	argv         []string
	dir          string
	env          []string
	onLine       func(string)  // called per complete line; may be nil
	collectBytes int           // > 0 keeps output for the caller
	onStart      func(pid int) // called once the process has started; may be nil
}

// procResult is the outcome of runProcess.
type procResult struct {
	output    string
	truncated bool // collection stopped at collectLimit
	total     int  // bytes written, including any past collectLimit
	exitCode  int  // -1 when killed, signalled or never started
	cancelled bool // ctx ended before the process exited on its own
	err       error
}

// runProcess starts spec.argv in its own process group and waits for it. When
// ctx ends first the whole group gets SIGTERM, then SIGKILL after killGrace.
// Stragglers that outlive the leader are killed too.
func runProcess(ctx context.Context, spec procSpec) procResult {
	cmd := exec.Command(spec.argv[0], spec.argv[1:]...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	configureProcess(cmd)
	cmd.WaitDelay = waitDelay

	sink := &lineSink{onLine: spec.onLine, collectLimit: spec.collectBytes}
	cmd.Stdout = sink
	cmd.Stderr = sink

	if err := cmd.Start(); err != nil {
		return procResult{exitCode: -1, err: err}
	}
	pid := cmd.Process.Pid
	if spec.onStart != nil {
		spec.onStart(pid)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var cancelled bool
	select {
	case <-done:
	case <-ctx.Done():
		cancelled = true
		terminateGroup(pid)
		select {
		case <-done:
		case <-time.After(killGrace):
			killGroup(pid)
			<-done
		}
	}
	// Reap anything the group left behind, even after a clean exit.
	killGroup(pid)
	sink.flush()

	code := -1
	if cmd.ProcessState != nil && !cancelled {
		code = cmd.ProcessState.ExitCode()
	}
	out, truncated := sink.result()
	return procResult{output: out, truncated: truncated, total: sink.size(), exitCode: code, cancelled: cancelled}
}

// lineSink receives combined stdout and stderr. It splits lines for onLine and
// keeps up to collectLimit bytes when collectBytes > 0.
type lineSink struct {
	mu           sync.Mutex
	partial      []byte
	onLine       func(string)
	collectLimit int
	collected    bytes.Buffer
	total        int
	truncated    bool
}

func (s *lineSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total += len(p)
	if s.collectLimit > 0 {
		room := collectLimit - s.collected.Len()
		if len(p) <= room {
			s.collected.Write(p)
		} else {
			if room > 0 {
				s.collected.Write(p[:room])
			}
			s.truncated = true
		}
	}
	data := p
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := append(append([]byte(nil), s.partial...), data[:i]...)
		s.partial = s.partial[:0]
		s.emit(strings.TrimSuffix(string(line), "\r"))
		data = data[i+1:]
	}
	s.partial = append(s.partial, data...)
	return len(p), nil
}

// flush emits a final line that had no trailing newline.
func (s *lineSink) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.partial) > 0 {
		s.emit(string(s.partial))
		s.partial = nil
	}
}

func (s *lineSink) emit(line string) {
	if s.onLine != nil {
		s.onLine(line)
	}
}

// size returns how many bytes the process wrote, collected or not.
func (s *lineSink) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

func (s *lineSink) result() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collected.String(), s.truncated
}

// truncateOutput caps s at outputLimit bytes; see truncateTo.
func truncateOutput(s string) (string, int) {
	return truncateTo(s, outputLimit)
}

// truncateTo caps s at limit bytes, keeping the head (a quarter of the limit)
// and the tail (five eighths) with a marker that says how much was dropped.
// Cuts land on line breaks where possible and never split a UTF-8 sequence.
func truncateTo(s string, limit int) (string, int) {
	if len(s) <= limit {
		return s, 0
	}
	h := limit / 4
	if i := strings.LastIndexByte(s[:h], '\n'); i >= 0 {
		h = i + 1
	} else {
		for h > 0 && !utf8.RuneStart(s[h]) {
			h--
		}
	}
	t := len(s) - limit*5/8
	if i := strings.IndexByte(s[t:], '\n'); i >= 0 {
		t += i + 1
	} else {
		for t < len(s) && !utf8.RuneStart(s[t]) {
			t++
		}
	}
	dropped := s[h:t]
	lines := strings.Count(dropped, "\n")
	marker := fmt.Sprintf("\n… %d lines omitted …\n", lines)
	if lines == 0 {
		marker = fmt.Sprintf("\n… %d bytes omitted …\n", len(dropped))
	}
	return s[:h] + marker + s[t:], lines
}
