package lsp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// nopWriteCloser swallows what a Client writes, so tests can drive a session
// without a server process.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func fakeSession(t *testing.T) *session {
	t.Helper()
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return &session{
		spec:   serverSpecs["go"],
		root:   t.TempDir(),
		client: NewClient(r, nopWriteCloser{io.Discard}),
		docs:   map[string]*docState{},
	}
}

func TestTimeoutForFastAndSlowOps(t *testing.T) {
	if got := timeoutFor("textDocument/hover"); got != fastTimeout {
		t.Fatalf("hover timeout = %s, want %s", got, fastTimeout)
	}
	if got := timeoutFor("textDocument/definition"); got != fastTimeout {
		t.Fatalf("definition timeout = %s, want %s", got, fastTimeout)
	}
	for _, m := range []string{"textDocument/rename", "textDocument/references", "workspace/symbol", "textDocument/codeAction"} {
		if got := timeoutFor(m); got != requestTimeout {
			t.Fatalf("%s timeout = %s, want %s", m, got, requestTimeout)
		}
	}
}

func TestOpenDocumentsCappedLRU(t *testing.T) {
	s := fakeSession(t)
	dir := t.TempDir()
	var paths []string
	for i := 0; i <= maxOpenDocs; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%02d.go", i))
		if err := os.WriteFile(p, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	for _, p := range paths[:maxOpenDocs] {
		if _, err := s.syncDoc(p); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // distinct lastUsed stamps
	}
	// Touch the first file so the second one is now the least recently used.
	if _, err := s.syncDoc(paths[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.syncDoc(paths[maxOpenDocs]); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	n := len(s.docs)
	_, hasFirst := s.docs[paths[0]]
	_, hasSecond := s.docs[paths[1]]
	_, hasNew := s.docs[paths[maxOpenDocs]]
	s.mu.Unlock()
	if n != maxOpenDocs {
		t.Fatalf("open documents = %d, want %d", n, maxOpenDocs)
	}
	if !hasFirst || hasSecond || !hasNew {
		t.Fatalf("LRU kept first=%v second=%v new=%v; want first kept, second closed, new open", hasFirst, hasSecond, hasNew)
	}
}

func TestFailedStartCooldownBlocksRetry(t *testing.T) {
	m := NewManager(t.TempDir())
	abs := filepath.Join(m.workdir, "main.go")
	spec := serverSpecs["go"]
	key := spec.Language + "\x00" + m.rootFor(spec, abs)
	m.failed[key] = startFailure{at: time.Now(), err: fmt.Errorf("boom")}

	_, err := m.sessionFor(context.Background(), abs)
	if err == nil || !contains(err.Error(), "not retried") || !contains(err.Error(), "boom") {
		t.Fatalf("sessionFor during cooldown = %v, want a cooldown error that names the cause", err)
	}

	// Once the cooldown has passed the start is attempted again. With no
	// gopls on this machine that surfaces as the install hint, not the old error.
	m.failed[key] = startFailure{at: time.Now().Add(-2 * failCooldown), err: fmt.Errorf("boom")}
	_, err = m.sessionFor(context.Background(), abs)
	if err != nil && contains(err.Error(), "not retried") {
		t.Fatalf("cooldown still applied after it expired: %v", err)
	}
}

func contains(s, sub string) bool { return len(sub) == 0 || indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestLookBinaryFindsMasonInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // nothing on PATH
	bin := filepath.Join(home, ".local", "share", "nvim", "mason", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "fake-lsp-server"
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := lookBinary(name, t.TempDir())
	if !ok || got != filepath.Join(bin, name) {
		t.Fatalf("lookBinary = %q, %v; want the Mason bin copy", got, ok)
	}
}
