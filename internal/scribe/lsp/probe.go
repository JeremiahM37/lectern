package lsp

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Spec 04 section 6.1: /diagnose probes each backend against a real file, with
// a 10 s budget for starting the server and 5 s for the operation.
const (
	ProbeInitTimeout = 10 * time.Second
	ProbeOpTimeout   = 5 * time.Second
)

// ProbeResult is one per-operation probe of a language server.
type ProbeResult struct {
	Language    string
	File        string
	Unsupported bool          // no server is catalogued for the file
	InitErr     error         // the server did not start within ProbeInitTimeout
	OpErr       error         // documentSymbol failed or did not answer within ProbeOpTimeout
	Symbols     int           // symbols the server returned for the file
	Elapsed     time.Duration // time for the operation alone
}

// Passed says the operation answered in time.
func (p ProbeResult) Passed() bool {
	return !p.Unsupported && p.InitErr == nil && p.OpErr == nil
}

// Probe starts the server for abs's language and asks it for the outline of
// abs. The start and the request each get their own budget, so a server that
// starts slowly is not charged against the request.
func (m *Manager) Probe(ctx context.Context, abs string) ProbeResult {
	res := ProbeResult{File: filepath.Base(abs)}
	info, ok := languageForPath(abs)
	if !ok {
		res.Unsupported = true
		return res
	}
	res.Language = info.key

	initCtx, cancelInit := context.WithTimeout(ctx, ProbeInitTimeout)
	_, err := m.sessionFor(initCtx, m.abs(abs))
	cancelInit()
	if err != nil {
		res.InitErr = err
		return res
	}

	opCtx, cancelOp := context.WithTimeout(ctx, ProbeOpTimeout)
	defer cancelOp()
	start := time.Now()
	syms, err := m.DocumentSymbols(opCtx, abs)
	res.Elapsed = time.Since(start)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("no answer within " + ProbeOpTimeout.String())
		}
		res.OpErr = err
		return res
	}
	res.Symbols = len(syms)
	return res
}

// probeSkipDirs are directories a sample search never enters.
var probeSkipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "dist": true, "build": true,
}

// SampleFile returns a file of the given language under root, for a probe to
// ask about. It walks at most 2000 entries and skips hidden and dependency
// folders. ok is false when the project has no such file.
func SampleFile(root, language string) (string, bool) {
	var found string
	seen := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return fs.SkipAll
		}
		seen++
		if seen > 2000 {
			return fs.SkipAll
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || probeSkipDirs[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		if info, ok := languageForPath(path); ok && info.key == language {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", false
	}
	if _, err := os.Stat(found); err != nil {
		return "", false
	}
	return found, true
}
