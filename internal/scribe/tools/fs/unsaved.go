package fs

import (
	"io"
	"os"
	"strings"
	"sync"
)

// The editor (nvim) can hold text the user has not saved. Reads check it first,
// so the agent sees what the user sees; the disk is read when there is none.
// The editor package installs the hook when it is registered, which keeps this
// package free of an import of the editor.
var (
	unsavedMu     sync.Mutex
	unsavedReader func(abs string) (string, bool)
)

// SetUnsavedReader installs the function that returns a file's unsaved editor
// text. ok is false when the file has no unsaved changes. nil removes it.
func SetUnsavedReader(fn func(abs string) (string, bool)) {
	unsavedMu.Lock()
	defer unsavedMu.Unlock()
	unsavedReader = fn
}

// unsavedText is the unsaved text of abs, when the editor has one.
func unsavedText(abs string) (string, bool) {
	unsavedMu.Lock()
	fn := unsavedReader
	unsavedMu.Unlock()
	if fn == nil {
		return "", false
	}
	return fn(abs)
}

// readSeekCloser is what a read streams from: the file, or the unsaved text.
type readSeekCloser interface {
	io.ReadSeeker
	io.Closer
}

// openForRead opens abs for a read, preferring the editor's unsaved text.
func openForRead(abs string) (readSeekCloser, error) {
	if text, ok := unsavedText(abs); ok {
		return nopCloser{strings.NewReader(text)}, nil
	}
	return os.Open(abs)
}

// readFileForRead is the whole content of abs for a read, preferring the
// editor's unsaved text.
func readFileForRead(abs string) ([]byte, error) {
	if text, ok := unsavedText(abs); ok {
		return []byte(text), nil
	}
	return os.ReadFile(abs)
}

type nopCloser struct{ io.ReadSeeker }

func (nopCloser) Close() error { return nil }
