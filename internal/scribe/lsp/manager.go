package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// requestTimeout bounds slow operations (rename, references, workspace
	// symbols, formatting, code actions). fastTimeout bounds the lookups an
	// editor expects to answer at once; see timeoutFor.
	requestTimeout = 30 * time.Second
	fastTimeout    = 10 * time.Second
	initTimeout    = 30 * time.Second
	diagQuiet      = 300 * time.Millisecond
	diagMax        = 2 * time.Second
	shutdownGrace  = 2 * time.Second
	// failCooldown is how long a server that failed to start is left alone
	// before another start is tried. Each try costs an init timeout.
	failCooldown = 60 * time.Second
	// maxOpenDocs caps the documents a server holds open. The least recently
	// used one is closed first, so a long session does not grow the server.
	maxOpenDocs = 50
)

// timeoutFor is the per-request timeout for an LSP method.
func timeoutFor(method string) time.Duration {
	switch method {
	case "textDocument/definition", "textDocument/hover", "textDocument/documentSymbol",
		"textDocument/typeDefinition", "textDocument/implementation":
		return fastTimeout
	}
	return requestTimeout
}

// startFailure remembers a server that could not start, for failCooldown.
type startFailure struct {
	at  time.Time
	err error
}

// Manager owns the language servers for one workspace. Servers start lazily
// per (language, project root), stay alive until Shutdown, and a crashed
// server is restarted once.
type Manager struct {
	workdir  string
	mu       sync.Mutex
	sessions map[string]*session
	disabled map[string]bool // language keys turned off in settings
	failed   map[string]startFailure
	closed   bool
	status   sessionStatus // read by Sessions without waiting for starts
}

// NewManager returns a Manager for the workspace rooted at workdir.
func NewManager(workdir string) *Manager {
	if abs, err := filepath.Abs(workdir); err == nil {
		workdir = abs
	}
	m := &Manager{workdir: workdir, sessions: map[string]*session{}, failed: map[string]startFailure{}}
	m.status.init()
	return m
}

// session is one running server with the documents it has open.
type session struct {
	spec     *serverSpec
	root     string
	client   *Client
	cmd      *exec.Cmd
	stderr   *tailBuffer
	restarts int
	command  string // "bin arg ..." as started, for status views
	bin      string // the binary as found, for the orphan record
	// startTicks is the process start time read right after start, so a
	// record can tell this server from a later process with the same pid.
	startTicks uint64

	mu   sync.Mutex // guards docs and document notifications
	docs map[string]*docState
}

type docState struct {
	version  int
	text     string    // what the server last saw
	lastUsed time.Time // last syncDoc, for closing the least recently used
}

// syncResult is the state of a file on its server after syncDoc.
type syncResult struct {
	uri     string
	text    string
	changed bool   // a didOpen or didChange was sent
	seq     uint64 // publish count before the notification
}

func (s *session) alive() bool {
	select {
	case <-s.client.Done():
		return false
	default:
		return true
	}
}

func (s *session) call(ctx context.Context, method string, params, result any) error {
	limit := timeoutFor(method)
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	err := s.client.Call(ctx, method, params, result)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s did not answer %s within %s", s.spec.Language, method, limit)
	}
	if err != nil && errors.Is(err, ErrServerExited) && s.stderr.String() != "" {
		return fmt.Errorf("%w: %s", err, s.stderr.String())
	}
	return err
}

// syncDoc makes the server's view of abs match the disk: didOpen the first
// time, didChange with the full text after that, and nothing when unchanged.
func (s *session) syncDoc(abs string) (syncResult, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return syncResult{}, err
	}
	text := string(data)
	uri := pathToURI(abs)
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, _ := s.client.diagState(uri)
	res := syncResult{uri: uri, text: text, seq: seq}
	st, ok := s.docs[abs]
	if !ok {
		s.closeIdle(abs)
		info, _ := languageForPath(abs)
		st = &docState{version: 1, text: text, lastUsed: time.Now()}
		s.docs[abs] = st
		res.changed = true
		return res, s.client.Notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{"uri": uri, "languageId": info.id, "version": st.version, "text": text},
		})
	}
	st.lastUsed = time.Now()
	if st.text == text {
		return res, nil
	}
	st.version++
	st.text = text
	res.changed = true
	return res, s.client.Notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": st.version},
		"contentChanges": []map[string]any{{"text": text}},
	})
}

// closeIdle makes room for one more open document by closing the least
// recently used ones, so the server holds at most maxOpenDocs. Caller holds
// s.mu. A closed file is reopened by syncDoc the next time it is needed.
func (s *session) closeIdle(keep string) {
	for len(s.docs) >= maxOpenDocs {
		var victim string
		for p, st := range s.docs {
			if p == keep {
				continue
			}
			if victim == "" || st.lastUsed.Before(s.docs[victim].lastUsed) {
				victim = p
			}
		}
		if victim == "" {
			return
		}
		delete(s.docs, victim)
		_ = s.client.Notify("textDocument/didClose", map[string]any{
			"textDocument": map[string]any{"uri": pathToURI(victim)},
		})
	}
}

// FileChanged tells the servers that abs changed on disk. A file open on a
// server gets the new text as didChange; a file that is not open gets a
// workspace/didChangeWatchedFiles notification so the server re-reads it.
// Call it for every file a tool writes.
func (m *Manager) FileChanged(abs string) {
	abs = m.abs(abs)
	for _, s := range m.running() {
		if !within(s.root, abs) {
			continue
		}
		s.mu.Lock()
		_, open := s.docs[abs]
		s.mu.Unlock()
		if open {
			_, _ = s.syncDoc(abs)
			continue
		}
		_ = s.client.Notify("workspace/didChangeWatchedFiles", map[string]any{
			"changes": []map[string]any{{"uri": pathToURI(abs), "type": 2}},
		})
	}
}

func (m *Manager) abs(path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.workdir, path)
	}
	return filepath.Clean(path)
}

// sessionFor returns the running server for abs, starting or restarting it.
func (m *Manager) sessionFor(ctx context.Context, abs string) (*session, error) {
	info, ok := languageForPath(abs)
	if !ok {
		return nil, fmt.Errorf("no language server is catalogued for %s", filepath.Base(abs))
	}
	spec := serverSpecs[info.key]
	if m.isDisabled(info.key) {
		return nil, fmt.Errorf("the %s language server is turned off in settings (disabled_lsp_servers in config.json). Remove it there to use it", spec.Language)
	}
	return m.sessionForSpec(ctx, spec, m.rootFor(spec, abs))
}

// SetDisabled turns off the language servers named by their language keys
// (such as "go" or "typescript"). A disabled server is never started; asking
// for one says it is turned off. Passing nothing re-enables every server.
func (m *Manager) SetDisabled(keys []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disabled = map[string]bool{}
	for _, k := range keys {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			m.disabled[k] = true
		}
	}
}

// isDisabled reports whether the server for language key is turned off.
func (m *Manager) isDisabled(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disabled[key]
}

// Warm starts the servers for the given languages at the workspace root, so
// the first symbol or diagnostic request does not wait for a cold start. Only
// languages that have a server spec and are not disabled are started. Errors
// are not reported: a server that cannot start is reported when it is used.
// Call it from a goroutine; it returns when every start has been tried.
func (m *Manager) Warm(ctx context.Context, languages []string) {
	for _, lang := range languages {
		spec, ok := serverSpecs[lang]
		if !ok || m.isDisabled(lang) || ctx.Err() != nil {
			continue
		}
		_, _ = m.sessionForSpec(ctx, spec, m.workdir)
	}
}

// sessionForSpec returns the running server for spec at root, starting it when
// needed.
func (m *Manager) sessionForSpec(ctx context.Context, spec *serverSpec, root string) (*session, error) {
	key := spec.Language + "\x00" + root

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("language servers have been shut down")
	}
	prev := m.sessions[key]
	if prev != nil && prev.alive() {
		return prev, nil
	}
	if f, ok := m.failed[key]; ok && time.Since(f.at) < failCooldown {
		return nil, fmt.Errorf("the %s language server failed to start %s ago and is not retried for %s: %w",
			spec.Language, time.Since(f.at).Round(time.Second), failCooldown, f.err)
	}
	restarts := 0
	if prev != nil {
		if prev.restarts >= 1 {
			err := fmt.Errorf("%s language server crashed again after a restart; not restarting it again. Last output: %s",
				spec.Language, prev.stderr.String())
			m.noteFailed(key, spec, root, err)
			return nil, err
		}
		restarts = prev.restarts + 1
	}
	m.noteStarting(key, spec, root)
	s, err := startSession(ctx, spec, root, restarts)
	if err != nil {
		var missing *NotInstalledError
		if !errors.As(err, &missing) {
			m.failed[key] = startFailure{at: time.Now(), err: err}
		}
		m.noteFailed(key, spec, root, err)
		return nil, err
	}
	delete(m.failed, key)
	m.sessions[key] = s
	m.noteStarted(key, s)
	return s, nil
}

// rootFor finds the nearest ancestor of abs (within the workdir) that holds
// a project marker for spec, falling back to the workdir.
func (m *Manager) rootFor(spec *serverSpec, abs string) string {
	for dir := filepath.Dir(abs); within(m.workdir, dir); dir = filepath.Dir(dir) {
		if hasAnyMarker(dir, markersFor(spec.Language)) {
			return dir
		}
		if dir == m.workdir {
			break
		}
	}
	return m.workdir
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func startSession(ctx context.Context, spec *serverSpec, root string, restarts int) (*session, error) {
	bin, args, ok := findServer(spec, root)
	if !ok {
		return nil, notInstalled(spec)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = root
	setProcGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", bin, err)
	}
	client := NewClient(stdout, stdin)
	s := &session{spec: spec, root: root, client: client, cmd: cmd, stderr: stderr,
		restarts: restarts, command: strings.Join(append([]string{bin}, args...), " "), docs: map[string]*docState{},
		bin: bin, startTicks: procStartTicks(cmd.Process.Pid)}
	go func() {
		<-client.Done()
		_ = cmd.Wait()
	}()

	ictx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	var initResult json.RawMessage
	if err := client.Call(ictx, "initialize", initializeParams(root), &initResult); err != nil {
		s.kill()
		return nil, fmt.Errorf("%s language server (%s) failed to initialize: %w: %s",
			spec.Language, bin, err, stderr.String())
	}
	if err := client.Notify("initialized", struct{}{}); err != nil {
		s.kill()
		return nil, err
	}
	return s, nil
}

func initializeParams(root string) map[string]any {
	return map[string]any{
		"processId":  os.Getpid(),
		"clientInfo": map[string]any{"name": "lectern-scribe"},
		"rootPath":   root,
		"rootUri":    pathToURI(root),
		"workspaceFolders": []map[string]any{
			{"uri": pathToURI(root), "name": filepath.Base(root)},
		},
		"capabilities": map[string]any{
			"workspace": map[string]any{
				"configuration":         true,
				"workspaceFolders":      true,
				"symbol":                map[string]any{"dynamicRegistration": false},
				"didChangeWatchedFiles": map[string]any{"dynamicRegistration": false},
				"workspaceEdit":         map[string]any{"documentChanges": true},
			},
			"window": map[string]any{"workDoneProgress": true},
			"textDocument": map[string]any{
				"synchronization": map[string]any{"didSave": false, "willSave": false},
				"definition":      map[string]any{"linkSupport": true},
				"references":      map[string]any{},
				"hover":           map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"rename":          map[string]any{"prepareSupport": false},
				"documentSymbol":  map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"codeAction": map[string]any{
					"codeActionLiteralSupport": map[string]any{
						"codeActionKind": map[string]any{"valueSet": []string{"", "quickfix", "refactor", "source"}},
					},
					"isPreferredSupport": true,
					"disabledSupport":    true,
					"dataSupport":        true,
					"resolveSupport":     map[string]any{"properties": []string{"edit"}},
				},
				"callHierarchy":      map[string]any{"dynamicRegistration": false},
				"typeHierarchy":      map[string]any{"dynamicRegistration": false},
				"formatting":         map[string]any{},
				"rangeFormatting":    map[string]any{},
				"publishDiagnostics": map[string]any{"relatedInformation": true},
			},
		},
	}
}

// pid is the server process id, or 0 if it never started.
func (s *session) pid() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// openDocs counts the documents the server has open.
func (s *session) openDocs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.docs)
}

func (s *session) kill() {
	killTree(s.cmd)
}

// stop asks the server to shut down, then kills its process group after a
// grace period. The group is killed even when the server exited on its own,
// because the children it forked can outlive it.
func (s *session) stop() {
	if s.alive() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		_ = s.client.Call(ctx, "shutdown", nil, nil)
		cancel()
		_ = s.client.Notify("exit", nil)
		_ = s.client.Close()
	}
	select {
	case <-s.client.Done():
	case <-time.After(shutdownGrace):
		s.kill()
		select {
		case <-s.client.Done():
		case <-time.After(shutdownGrace):
		}
	}
	killTree(s.cmd)
}

// Shutdown stops every server. The Manager cannot be used afterwards.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	list := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.sessions = map[string]*session{}
	m.mu.Unlock()
	m.clearStatus()
	for _, s := range list {
		s.stop()
	}
	// The record stays until every server is stopped, so a crash during the
	// stops still leaves them for the next boot.
	m.releaseRecords()
}

// running returns the live sessions, in a stable order.
func (m *Manager) running() []*session {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.sessions))
	for k := range m.sessions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []*session
	for _, k := range keys {
		if s := m.sessions[k]; s.alive() {
			out = append(out, s)
		}
	}
	return out
}

// tailBuffer keeps the last max bytes written to it, for error messages.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if extra := t.buf.Len() - t.max; extra > 0 {
		t.buf.Next(extra)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(t.buf.String())
}
