// Package nvim drives an editor that Scribe runs beside the chat. The editor
// runs on a pseudo-terminal, as any program does; when it is Neovim it also
// listens on a Unix socket, and this package talks to it over msgpack-RPC.
// Through that connection Scribe reloads a buffer after the agent writes its
// file, moves the cursor to the lines the agent changed, hears about the
// user's saves, and answers the agent's editor tool (see tool.go).
package nvim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/term"
)

// Timeouts. Connecting waits for Neovim to create its socket; a call to a
// running editor is short, because the editor is on the user's screen.
const (
	connectWait = 10 * time.Second
	callWait    = 3 * time.Second
	// toolWriteWindow is how long after the agent writes a file a save
	// notification for it is still taken as the agent's own write.
	toolWriteWindow = 2 * time.Second
)

// setupLua runs once per Neovim: it asks Neovim to tell Scribe about each
// save. The notification reaches Scribe only when Scribe has subscribed.
const setupLua = `
if vim.g.scribe_nvim_setup then return true end
vim.g.scribe_nvim_setup = 1
vim.api.nvim_create_autocmd("BufWritePost", {
  callback = function(ev)
    local name = vim.api.nvim_buf_get_name(ev.buf)
    if name ~= "" then vim.rpcnotify(0, "scribe_written", name) end
  end,
})
return true
`

// followLua moves the editor to a file the agent changed. A buffer the user
// has unsaved edits in is never replaced or reloaded: the agent's change
// stays on disk and the user's edits stay in the buffer.
const followLua = `
local path, line = ...
local target
for _, b in ipairs(vim.api.nvim_list_bufs()) do
  if vim.api.nvim_buf_is_loaded(b) and vim.api.nvim_buf_get_name(b) == path then
    target = b
    break
  end
end
local cur = vim.api.nvim_get_current_buf()
if target and vim.bo[target].modified then return "modified" end
if cur ~= target then
  if vim.bo[cur].modified then return "busy" end
  vim.cmd("silent! edit " .. vim.fn.fnameescape(path))
  cur = vim.api.nvim_get_current_buf()
elseif target then
  vim.api.nvim_buf_call(target, function() vim.cmd("silent! edit") end)
end
if line and line > 0 then
  local n = vim.api.nvim_buf_line_count(cur)
  pcall(vim.api.nvim_win_set_cursor, 0, { math.min(line, n), 0 })
  vim.cmd("normal! zz")
end
return "followed"
`

var (
	mu      sync.Mutex
	current *Session // the editor the user has open, nil when none
	onWrite func(abs string)

	sockSeq atomic.Uint64
)

// Current is the open editor's session, or nil when no editor is open.
func Current() *Session {
	mu.Lock()
	defer mu.Unlock()
	return current
}

// SetWriteHook sets the function called with the absolute path of each file
// the user saves in Neovim. The agent's own writes through the editor tool
// are not reported. nil clears it.
func SetWriteHook(fn func(abs string)) {
	mu.Lock()
	defer mu.Unlock()
	onWrite = fn
}

// Session is one editor run on a pane. Pane is what the TUI draws. The RPC
// connection is only set up for Neovim, once it is listening.
type Session struct {
	Pane   *term.Pane
	socket string // empty when the editor is not Neovim

	ready chan struct{} // closed when the connection is set up or has failed
	err   error         // why the connection could not be set up

	mu        sync.Mutex
	client    *Client
	closed    bool
	toolWrite map[string]time.Time // abs path to when the agent last wrote it
}

// IsNvim reports whether argv runs Neovim.
func IsNvim(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	base := filepath.Base(argv[0])
	return base == "nvim" || base == "nvim.exe"
}

// Start runs argv in dir on a pseudo-terminal of w x h cells. When argv is
// Neovim it is given --listen with a socket of its own, and the RPC
// connection is made in the background. Start makes the session the
// Current one.
func Start(argv []string, dir string, w, h int) (*Session, error) {
	s := &Session{ready: make(chan struct{}), toolWrite: map[string]time.Time{}}
	run := argv
	if IsNvim(argv) {
		sweepStale()
		s.socket = socketPath()
		run = append([]string{argv[0], "--listen", s.socket}, argv[1:]...)
	}
	p, err := term.New(run, dir, w, h)
	if err != nil {
		return nil, err
	}
	s.Pane = p
	if s.socket == "" {
		s.err = errors.New("the editor is not Neovim, so it has no RPC connection")
		close(s.ready)
	} else {
		go s.connect()
	}
	mu.Lock()
	current = s
	mu.Unlock()
	return s, nil
}

// socketPath is a fresh socket path in the temp directory. Unix socket paths
// are short, so the name stays compact.
func socketPath() string {
	name := "scribe-nvim-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(sockSeq.Add(1), 10) + "-" +
		strconv.FormatInt(time.Now().UnixNano()%1_000_000_000, 36) + ".sock"
	return filepath.Join(os.TempDir(), name)
}

// connect waits for Neovim to listen, dials it and sets up the autocmds.
func (s *Session) connect() {
	defer close(s.ready)
	deadline := time.Now().Add(connectWait)
	for {
		if _, err := os.Stat(s.socket); err == nil {
			break
		}
		if exited, _ := s.Pane.Exited(); exited {
			s.err = errors.New("the editor exited before it listened")
			return
		}
		if time.Now().After(deadline) {
			s.err = errors.New("the editor did not listen on its socket in time")
			return
		}
		select {
		case <-s.Pane.Done():
		case <-time.After(30 * time.Millisecond):
		}
	}
	c, err := Dial(s.socket, s.onNotify)
	if err != nil {
		s.err = fmt.Errorf("connect to the editor: %w", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callWait)
	defer cancel()
	if _, err := c.Call(ctx, "nvim_subscribe", "scribe_written"); err != nil {
		_ = c.Close()
		s.err = fmt.Errorf("subscribe to editor events: %w", err)
		return
	}
	if _, err := c.Call(ctx, "nvim_exec_lua", setupLua, []any{}); err != nil {
		_ = c.Close()
		s.err = fmt.Errorf("set up the editor: %w", err)
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = c.Close()
		return
	}
	s.client = c
	s.mu.Unlock()
}

// wait returns the connection, or why there is none.
func (s *Session) wait(ctx context.Context) (*Client, error) {
	select {
	case <-s.ready:
	case <-ctx.Done():
		return nil, fmt.Errorf("the editor is still starting: %w", ctx.Err())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if s.client == nil {
		if s.err != nil {
			return nil, s.err
		}
		return nil, errors.New("the editor has no RPC connection")
	}
	return s.client, nil
}

// Call makes an RPC call to the editor, waiting for the connection first.
func (s *Session) Call(ctx context.Context, method string, args ...any) (any, error) {
	c, err := s.wait(ctx)
	if err != nil {
		return nil, err
	}
	return c.Call(ctx, method, args...)
}

// Lua runs code in the editor with args available as ... and returns what
// the code returns.
func (s *Session) Lua(ctx context.Context, code string, args ...any) (any, error) {
	if args == nil {
		args = []any{}
	}
	return s.Call(ctx, "nvim_exec_lua", code, args)
}

// onNotify receives the editor's notifications on the read loop.
func (s *Session) onNotify(method string, args []any) {
	if method != "scribe_written" || len(args) == 0 {
		return
	}
	path, ok := args[0].(string)
	if !ok || path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	at, ours := s.toolWrite[abs]
	s.mu.Unlock()
	if ours && time.Since(at) < toolWriteWindow {
		return // the agent's own write, not the user's
	}
	mu.Lock()
	fn := onWrite
	mu.Unlock()
	if fn != nil {
		fn(abs)
	}
}

// markToolWrite records that the agent is writing abs now.
func (s *Session) markToolWrite(abs string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolWrite[abs] = time.Now()
}

// Follow shows the agent's change to abs in the editor: the file is opened
// or reloaded and the cursor moves to line (1-based; 0 keeps the cursor).
// It reports what it did: "followed", "modified" (the user has unsaved edits
// in that file) or "busy" (the user has unsaved edits in another file). It
// returns at once when no RPC connection is up.
func (s *Session) Follow(abs string, line int) string {
	if s == nil || s.socket == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), callWait)
	defer cancel()
	v, err := s.Lua(ctx, followLua, abs, int64(line))
	if err != nil {
		return ""
	}
	res, _ := v.(string)
	return res
}

// Follow is Session.Follow on the open editor. It does nothing when no
// editor is open, and runs the RPC work off the caller's goroutine, so the
// agent loop does not wait for the screen.
func Follow(abs string, line int) {
	s := Current()
	if s == nil || s.socket == "" {
		return
	}
	go s.Follow(abs, line)
}

// Close ends the editor: the process is killed, the connection and socket are
// released, and the session stops being the Current one.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	c := s.client
	s.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	_ = s.Pane.Close()
	if s.socket != "" {
		_ = os.Remove(s.socket)
	}
	mu.Lock()
	if current == s {
		current = nil
	}
	mu.Unlock()
}

// Closed reports whether Close has run.
func (s *Session) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
