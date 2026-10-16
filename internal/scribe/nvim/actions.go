package nvim

import (
	"context"
	"fmt"
	"strings"
)

// actionLua is the one dispatcher behind the editor tool. It runs inside
// Neovim with the action name and its parameters as ... and returns a table
// {ok = bool, text = string}. Lua errors are caught and reported as text.
const actionLua = `
local action, p = ...
local function ret(ok, text) return { ok = ok, text = text } end
local function rel(path) return vim.fn.fnamemodify(path, ":.") end
local function bufname() return vim.api.nvim_buf_get_name(0) end
local function nlines(b) return vim.api.nvim_buf_line_count(b or 0) end
local function clients() return vim.lsp.get_clients({ bufnr = 0 }) end
local function clamp(v, lo, hi) return math.max(lo, math.min(hi, v)) end

-- lsp runs one request against the current buffer's clients and returns the
-- non-empty results, or nil and a reason.
local function lsp(method, params)
  local cs = clients()
  if #cs == 0 then return nil, "__NO_LSP__" end
  local res = vim.lsp.buf_request_sync(0, method, params, 4000)
  if not res then return nil, "the language server did not answer in time" end
  local out = {}
  for _, r in pairs(res) do
    if r.result ~= nil and r.result ~= vim.NIL then table.insert(out, r.result) end
  end
  return out, nil, cs[1].offset_encoding
end

local function position(enc)
  return vim.lsp.util.make_position_params(0, enc or "utf-16")
end

local function loc_text(uri, range)
  local path = rel(vim.uri_to_fname(uri))
  return string.format("%s:%d:%d", path, range.start.line + 1, range.start.character + 1)
end

local function symbol_lines(list, depth, out)
  for _, s in ipairs(list) do
    local kind = vim.lsp.protocol.SymbolKind[s.kind] or tostring(s.kind)
    local line = s.range and s.range.start.line or (s.location and s.location.range.start.line) or 0
    table.insert(out, string.format("%s%d %s %s", string.rep("  ", depth), line + 1, kind, s.name))
    if s.children then symbol_lines(s.children, depth + 1, out) end
  end
  return out
end

local function lines_out(from, to)
  local n = nlines(0)
  local s, e = p.startLine or 1, p.endLine or n
  s, e = clamp(s, 1, n), clamp(e, 1, n)
  if e < s then return ret(false, "endLine is before startLine") end
  local out = {}
  for i, l in ipairs(vim.api.nvim_buf_get_lines(0, s - 1, e, false)) do
    table.insert(out, string.format("%d\t%s", s + i - 1, l))
  end
  return ret(true, string.format("%s (%d lines in file)\n%s", rel(bufname()), n, table.concat(out, "\n")))
end

local A = {}

A.read = function() return lines_out() end

A.edit = function()
  local name = bufname()
  if name == "" then return ret(false, "this buffer has no file to save to") end
  local n = nlines(0)
  local s = p.startLine
  if not s then return ret(false, "edit needs startLine") end
  local e = p.endLine or s
  if s < 1 or s > n or e < s or e > n then
    return ret(false, string.format("lines %d-%d are outside 1-%d", s, e, n))
  end
  local text = p.replacement or ""
  local lines = {}
  if text ~= "" then lines = vim.split(text, "\n", { plain = true }) end
  vim.api.nvim_buf_set_lines(0, s - 1, e, false, lines)
  vim.cmd("silent write")
  return ret(true, string.format("Replaced lines %d-%d with %d line(s) (saved)", s, e, #lines))
end

A.navigate = function()
  if p.search and p.search ~= "" then
    local ok = vim.fn.search(p.search, "w")
    if ok == 0 then return ret(false, "pattern not found: " .. p.search) end
  elseif p.line then
    local n = nlines(0)
    local col = p.col or 1
    vim.api.nvim_win_set_cursor(0, { clamp(p.line, 1, n), math.max(0, col - 1) })
  end
  local pos = vim.api.nvim_win_get_cursor(0)
  return ret(true, string.format("%s:%d:%d", rel(bufname()), pos[1], pos[2] + 1))
end

A.goto_cursor = A.navigate

A.select = function()
  local n = nlines(0)
  local s, e = p.startLine or 1, p.endLine or p.startLine or 1
  s, e = clamp(s, 1, n), clamp(e, 1, n)
  vim.api.nvim_buf_set_mark(0, "<", s, 0, {})
  vim.api.nvim_buf_set_mark(0, ">", e, 0, {})
  return ret(true, string.format("Selected lines %d-%d (the user can reselect with gv)", s, e))
end

A.yank = function()
  if not p.text then return ret(false, "yank needs text") end
  local reg = p.register or "+"
  local ok, err = pcall(vim.fn.setreg, reg, p.text)
  if not ok then return ret(false, "could not write register " .. reg .. ": " .. tostring(err)) end
  return ret(true, string.format("Copied %d line(s) to register %s", #vim.split(p.text, "\n", { plain = true }), reg))
end

A.diagnostics = function()
  local ds = vim.diagnostic.get(0)
  if #ds == 0 then return ret(true, "No diagnostics in " .. rel(bufname())) end
  local out = {}
  for _, d in ipairs(ds) do
    local sev = vim.diagnostic.severity[d.severity] or "INFO"
    table.insert(out, string.format("%d:%d %s %s: %s", d.lnum + 1, d.col + 1, sev, d.source or "", d.message:gsub("\n", " ")))
  end
  return ret(true, table.concat(out, "\n"))
end

A.symbols = function()
  local res, why, enc = lsp("textDocument/documentSymbol", { textDocument = vim.lsp.util.make_text_document_params(0) })
  if not res then return ret(false, why) end
  local out = {}
  for _, r in ipairs(res) do symbol_lines(r, 0, out) end
  if #out == 0 then return ret(true, "No symbols") end
  return ret(true, table.concat(out, "\n"))
end

A.hover = function()
  local res, why, enc = lsp("textDocument/hover", position())
  if not res then return ret(false, why) end
  for _, r in ipairs(res) do
    local lines = vim.lsp.util.convert_input_to_markdown_lines(r.contents or {})
    if #lines > 0 then return ret(true, table.concat(lines, "\n")) end
  end
  return ret(true, "No hover information at this position")
end

local function locations(res)
  local out = {}
  for _, r in ipairs(res) do
    local list = r
    if r.uri or r.targetUri then list = { r } end
    for _, l in ipairs(list) do
      local uri = l.uri or l.targetUri
      local range = l.range or l.targetSelectionRange or l.targetRange
      if uri and range then table.insert(out, { uri = uri, range = range }) end
    end
  end
  return out
end

local function list_locations(method, extra)
  local params = position()
  if extra then for k, v in pairs(extra) do params[k] = v end end
  local res, why, enc = lsp(method, params)
  if not res then return nil, why, enc end
  return locations(res), nil, enc
end

A.definition = function()
  local locs, why, enc = list_locations("textDocument/definition")
  if not locs then return ret(false, why) end
  if #locs == 0 then return ret(true, "No definition found") end
  if p.jump then
    pcall(vim.lsp.util.jump_to_location, { uri = locs[1].uri, range = locs[1].range }, enc or "utf-16")
  end
  local out = {}
  for _, l in ipairs(locs) do table.insert(out, loc_text(l.uri, l.range)) end
  return ret(true, table.concat(out, "\n"))
end

A.references = function()
  local locs, why = list_locations("textDocument/references", { context = { includeDeclaration = true } })
  if not locs then return ret(false, why) end
  if #locs == 0 then return ret(true, "No references found") end
  local out = {}
  for _, l in ipairs(locs) do table.insert(out, loc_text(l.uri, l.range)) end
  return ret(true, table.concat(out, "\n"))
end

A.rename = function()
  if not p.newName or p.newName == "" then return ret(false, "rename needs newName") end
  local params = position()
  params.newName = p.newName
  local res, why, enc = lsp("textDocument/rename", params)
  if not res then return ret(false, why) end
  if #res == 0 then return ret(false, "the language server returned no edits") end
  local files = {}
  for _, edit in ipairs(res) do
    for uri, _ in pairs(edit.changes or {}) do files[uri] = true end
    for _, dc in ipairs(edit.documentChanges or {}) do
      if dc.textDocument then files[dc.textDocument.uri] = true end
    end
    vim.lsp.util.apply_workspace_edit(edit, enc or "utf-16")
  end
  local count = 0
  for _ in pairs(files) do count = count + 1 end
  return ret(true, string.format("Renamed to %s in %d file(s) (unsaved until written)", p.newName, count))
end

A.format = function()
  if #clients() == 0 then return ret(false, "__NO_LSP__") end
  vim.lsp.buf.format({ async = false, timeout_ms = 3000 })
  return ret(true, "Formatted " .. rel(bufname()) .. " (not saved)")
end

A.buffers = function()
  local cur = vim.api.nvim_get_current_buf()
  local out = {}
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    if vim.bo[b].buflisted then
      local name = vim.api.nvim_buf_get_name(b)
      if name == "" then name = "[No Name]" else name = rel(name) end
      local flags = {}
      if b == cur then table.insert(flags, "current") end
      if vim.bo[b].modified then table.insert(flags, "modified") end
      table.insert(out, string.format("%s%s", name, #flags > 0 and (" [" .. table.concat(flags, ", ") .. "]") or ""))
    end
  end
  if #out == 0 then return ret(true, "No buffers") end
  return ret(true, table.concat(out, "\n"))
end

A.cursor_context = function()
  local pos = vim.api.nvim_win_get_cursor(0)
  local line = vim.api.nvim_buf_get_lines(0, pos[1] - 1, pos[1], false)[1] or ""
  local out = { string.format("file: %s", bufname() == "" and "[No Name]" or rel(bufname())),
    string.format("cursor: %d:%d", pos[1], pos[2] + 1), "line: " .. line }
  local s = vim.api.nvim_buf_get_mark(0, "<")
  local e = vim.api.nvim_buf_get_mark(0, ">")
  if s[1] > 0 and e[1] > 0 then
    local sel = vim.api.nvim_buf_get_lines(0, s[1] - 1, e[1], false)
    table.insert(out, string.format("selection: lines %d-%d", s[1], e[1]))
    table.insert(out, table.concat(sel, "\n"))
  end
  return ret(true, table.concat(out, "\n"))
end

A.quickfix = function()
  local items = vim.fn.getqflist()
  if #items == 0 then return ret(true, "The quickfix list is empty") end
  local out = {}
  for _, it in ipairs(items) do
    local file = it.bufnr > 0 and rel(vim.api.nvim_buf_get_name(it.bufnr)) or "?"
    table.insert(out, string.format("%s:%d: %s", file, it.lnum, it.text))
  end
  return ret(true, table.concat(out, "\n"))
end

A.terminal_output = function()
  local count = p.count or 100
  local target
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    if vim.bo[b].buftype == "terminal" and (target == nil or b == vim.api.nvim_get_current_buf()) then
      target = b
    end
  end
  if not target then return ret(false, "no terminal buffer is open in the editor") end
  local all = vim.api.nvim_buf_get_lines(target, 0, -1, false)
  while #all > 0 and all[#all]:match("^%s*$") do table.remove(all) end
  local from = math.max(1, #all - count + 1)
  return ret(true, table.concat(vim.list_slice(all, from, #all), "\n"))
end

if not A[action] then return ret(false, "unknown action " .. tostring(action)) end
local ok, res = pcall(A[action])
if not ok then return ret(false, tostring(res)) end
return res
`

// switchLua opens file in the editor, unless the current buffer has unsaved
// edits: then the user's buffer stays and the switch is refused.
const switchLua = `
local path = ...
local cur = vim.api.nvim_get_current_buf()
if vim.api.nvim_buf_get_name(cur) == path then return { ok = true, text = "" } end
if vim.bo[cur].modified then
  return { ok = false, text = "the editor's current buffer has unsaved changes; the user must save or discard them before the editor can open " .. vim.fn.fnamemodify(path, ":.") }
end
local ok, err = pcall(vim.cmd, "edit " .. vim.fn.fnameescape(path))
if not ok then return { ok = false, text = tostring(err) } end
return { ok = true, text = "" }
`

// currentNameLua is the current buffer's file name, or "".
const currentNameLua = `return vim.api.nvim_buf_get_name(0)`

// luaResult reads the {ok, text} table the Lua side returns.
func luaResult(v any) (bool, string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return false, "", fmt.Errorf("unexpected editor reply %T", v)
	}
	okv, _ := m["ok"].(bool)
	text, _ := m["text"].(string)
	return okv, text, nil
}

// runAction runs one editor action and returns its text, or an error.
func (s *Session) runAction(ctx context.Context, action string, params map[string]any) (string, error) {
	if params == nil {
		params = map[string]any{}
	}
	v, err := s.Lua(ctx, actionLua, action, params)
	if err != nil {
		return "", err
	}
	ok, text, err := luaResult(v)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%s", strings.TrimSpace(text))
	}
	return text, nil
}
