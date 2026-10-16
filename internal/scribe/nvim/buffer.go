package nvim

import (
	"context"
	"strings"
	"sync/atomic"
)

// readsInFlight counts reads of buffer content in progress. The spec skips
// Neovim when two or more reads are running at once, so parallel reads do
// not queue behind the editor.
var readsInFlight atomic.Int32

// bufferLua returns the text of the loaded buffer for path when it has
// unsaved edits, and {ok = false} otherwise.
const bufferLua = `
local path = ...
for _, b in ipairs(vim.api.nvim_list_bufs()) do
  if vim.api.nvim_buf_is_loaded(b) and vim.api.nvim_buf_get_name(b) == path and vim.bo[b].modified then
    return { ok = true, text = table.concat(vim.api.nvim_buf_get_lines(b, 0, -1, false), "\n") .. "\n" }
  end
end
return { ok = false, text = "" }
`

// UnsavedContent returns what the editor holds for abs when the user has
// unsaved edits in it, so an agent read sees those edits and not the file on
// disk. It reports false when the file is not open with changes, when no
// editor is open, or when two or more reads are already running. Callers
// fall back to the disk in every case but the first.
func UnsavedContent(abs string) (string, bool) {
	s := Current()
	if s == nil || s.socket == "" {
		return "", false
	}
	if readsInFlight.Add(1) > 1 {
		readsInFlight.Add(-1)
		return "", false
	}
	defer readsInFlight.Add(-1)
	ctx, cancel := context.WithTimeout(context.Background(), callWait)
	defer cancel()
	v, err := s.Lua(ctx, bufferLua, abs)
	if err != nil {
		return "", false
	}
	ok, text, err := luaResult(v)
	if err != nil || !ok {
		return "", false
	}
	return strings.Clone(text), true
}
