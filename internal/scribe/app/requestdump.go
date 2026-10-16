package app

import (
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
type requestDump struct {
	mu     sync.Mutex
	id     string // session id the file belongs to
	file   *os.File
	dumper func(step int, modelRef string, req llm.Request)
}

// requestDump returns the hook the lead agent calls before each model request,
// or nil. The hook is on only when SCRIBE_REQUEST_DUMP=1 and the session is
// saved: each request is appended as one JSON line to
// <session dir>/requests.jsonl, with headers removed and secrets redacted
// (agent.DumpRequests). The file is opened once per session and kept open
// until the process exits, so a dump of a long session costs one descriptor.
func (a *App) requestDump() func(int, string, llm.Request) {
	if os.Getenv(requestDumpEnv) != "1" || a.opts.Ephemeral || a.Store == nil || a.Meta.ID == "" {
		return nil
	}
	d := &a.dump
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dumper != nil && d.id == a.Meta.ID {
		return d.dumper
	}
	path := filepath.Join(a.Store.Dir(a.Meta.ID), "requests.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil // a dump must never stop the session
	}
	if d.file != nil {
		_ = d.file.Close()
	}
	d.id, d.file = a.Meta.ID, f
	d.dumper = agent.DumpRequests(f)
	return d.dumper
}
