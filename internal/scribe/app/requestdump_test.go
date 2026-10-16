package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
)

// The request dump is off unless SCRIBE_REQUEST_DUMP=1, and when on it writes
// one JSON line per request into the saved session's directory.
func TestRequestDumpIsOffUnlessSwitchedOn(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	a := &App{Workdir: root, Store: session.NewStore(root), Meta: session.Meta{ID: "s1"}}

	t.Setenv(requestDumpEnv, "")
	if err := os.MkdirAll(a.Store.Dir("s1"), 0o700); err != nil {
		t.Fatal(err)
	}
	a.requestDump()(1, "ollama/m", llm.Request{})
	if _, err := os.Stat(filepath.Join(a.Store.Dir("s1"), "requests.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("the dump must write nothing without the switch: %v", err)
	}
	t.Setenv(requestDumpEnv, "1")
	a.opts.Ephemeral = true
	if a.requestDump() != nil {
		t.Fatal("an ephemeral session has no directory to dump into")
	}
}

func TestRequestDumpWritesJSONLinesIntoTheSessionDir(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	a := &App{Workdir: root, Store: session.NewStore(root), Meta: session.Meta{ID: "s1"}}
	if err := os.MkdirAll(a.Store.Dir("s1"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(requestDumpEnv, "1")

	hook := a.requestDump()
	if hook == nil {
		t.Fatal("the dump should be on with the switch and a saved session")
	}
	hook(1, "ollama/m", llm.Request{})
	if again := a.requestDump(); again == nil {
		t.Fatal("a second call must return the same hook")
	}
	a.requestDump()(2, "ollama/m", llm.Request{})

	raw, err := os.ReadFile(filepath.Join(a.Store.Dir("s1"), "requests.jsonl"))
	if err != nil {
		t.Fatalf("dump file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"step":1`) || !strings.Contains(lines[1], `"step":2`) {
		t.Fatalf("dump lines = %q", lines)
	}
}

// /session export api switches the dump on and off at runtime.
func TestRequestDumpFollowsTheRuntimeSwitch(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	a := &App{Workdir: root, Store: session.NewStore(root), Meta: session.Meta{ID: "s1"}}
	if err := os.MkdirAll(a.Store.Dir("s1"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(requestDumpEnv, "")
	b := &scribeBackend{app: a}
	hook := a.requestDump()
	path, err := b.SetAPIDump(true)
	if err != nil || !b.APIDump() {
		t.Fatalf("switch on: %q %v", path, err)
	}
	hook(1, "m", llm.Request{})
	if _, err := b.SetAPIDump(false); err != nil {
		t.Fatal(err)
	}
	hook(2, "m", llm.Request{})
	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), "\n"); n != 1 {
		t.Fatalf("one request was sent while on, file has %d lines", n)
	}
}
