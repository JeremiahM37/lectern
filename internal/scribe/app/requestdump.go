package app

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// requestDumpEnv switches the per-request dump on. It is an environment
// switch, not a config key, so a debugging run needs no config change.
const requestDumpEnv = "SCRIBE_REQUEST_DUMP"

// requestDump is the lazily opened dump file of the session it was opened for.
// on is the /session export api switch; the environment variable also turns it
// on.
type requestDump struct {
	mu     sync.Mutex
	on     bool
	id     string // session id the file belongs to
	file   *os.File
	dumper func(step int, modelRef string, req llm.Request)
}

// requestDump returns the hook the lead agent calls before each model request,
// or nil for a session that is not saved. The hook writes only while the dump
// is on (SCRIBE_REQUEST_DUMP=1 or /session export api on): each request is
// appended as one JSON line to <session dir>/requests.jsonl, with headers
// removed and secrets redacted (agent.DumpRequests). The file is opened on
// the first write and kept open, so a long dump costs one descriptor.
func (a *App) requestDump() func(int, string, llm.Request) {
	if a.opts.Ephemeral || a.Store == nil || a.Meta.ID == "" {
		return nil
	}
	return func(step int, ref string, req llm.Request) {
		if w := a.dumpWriter(); w != nil {
			w(step, ref, req)
		}
	}
}

// dumpOn reports whether requests are being written.
func (a *App) dumpOn() bool {
	a.dump.mu.Lock()
	defer a.dump.mu.Unlock()
	return a.dump.on || os.Getenv(requestDumpEnv) == "1"
}

// dumpWriter returns the writer for the current session while the dump is on,
// opening the file when needed. A failure to open never stops the session.
func (a *App) dumpWriter() func(int, string, llm.Request) {
	d := &a.dump
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.on && os.Getenv(requestDumpEnv) != "1" {
		return nil
	}
	if d.dumper != nil && d.id == a.Meta.ID {
		return d.dumper
	}
	f, err := os.OpenFile(a.dumpPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	if d.file != nil {
		_ = d.file.Close()
	}
	d.id, d.file = a.Meta.ID, f
	d.dumper = agent.DumpRequests(f)
	return d.dumper
}

func (a *App) dumpPath() string { return filepath.Join(a.Store.Dir(a.Meta.ID), "requests.jsonl") }

// APIDump reports whether the per-request dump is on (/session export api).
func (b *scribeBackend) APIDump() bool { return b.current().dumpOn() }

// SetAPIDump turns the per-request dump on or off and returns the file it
// appends to while on.
func (b *scribeBackend) SetAPIDump(on bool) (string, error) {
	a := b.current()
	if a.opts.Ephemeral || a.Store == nil || a.Meta.ID == "" {
		return "", errors.New("this session is not saved, so there is nowhere to write the request dump")
	}
	a.dump.mu.Lock()
	a.dump.on = on
	a.dump.mu.Unlock()
	if !on {
		return "", nil
	}
	return a.dumpPath(), nil
}
