package nvim

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
)

// ToolName is the editor tool's name.
const ToolName = "editor"

// toolWait bounds one editor action. An action is on the user's screen, so
// it returns quickly or it is stuck.
const toolWait = 5 * time.Second

// Result tags the editor tool sets, besides the ones the fs tools use.
const (
	tagEditorDisabled = "editor access disabled"
	tagEditorNotOpen  = "editor not open"
)

// actions the tool accepts, in the order the schema lists them.
var actions = []string{
	"read", "edit", "navigate", "diagnostics", "symbols", "hover", "references",
	"definition", "rename", "format", "select", "goto_cursor", "yank",
	"cursor_context", "buffers", "quickfix", "terminal_output", "highlight",
	"code_actions", "apply_code_action",
}

// readOnlyActions never change a file. They may move the cursor or copy to
// the clipboard, which plan mode allows.
var readOnlyActions = map[string]bool{
	"read": true, "diagnostics": true, "symbols": true, "hover": true,
	"references": true, "definition": true, "navigate": true, "goto_cursor": true,
	"select": true, "yank": true, "cursor_context": true, "buffers": true,
	"quickfix": true, "terminal_output": true, "highlight": true,
	"code_actions": true,
}

// Args is the editor tool's input.
type Args struct {
	Action      string  `json:"action"`
	File        string  `json:"file"`
	StartLine   int     `json:"startLine"`
	EndLine     int     `json:"endLine"`
	Replacement *string `json:"replacement"`
	Line        int     `json:"line"`
	Col         int     `json:"col"`
	Search      string  `json:"search"`
	NewName     string  `json:"newName"`
	Jump        bool    `json:"jump"`
	Text        *string `json:"text"`
	Register    string  `json:"register"`
	Count       int     `json:"count"`
	Index       int     `json:"index"`
	Title       string  `json:"title"`
}

type tool struct{}

// Tool returns the editor tool for the agent.
func Tool() tools.Tool { return tool{} }

// Register adds the editor tool to r.
func Register(r *tools.Registry) {
	r.Register(Tool())
	// The agent's reads see unsaved editor text (tools/fs checks this hook).
	fs.SetUnsavedReader(UnsavedContent)
}

func (tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: ToolName,
		Description: "Work in the editor the user has open beside the chat (Neovim): read the buffer, edit lines " +
			"(the edit is saved), navigate, read diagnostics, symbols, hover, definitions and references through the " +
			"language server, rename, format, select, copy to the clipboard, list buffers, the quickfix list and the " +
			"terminal's output, highlight a range of lines in the buffer for the user to see, and the server's code actions for " +
			"lines (code_actions lists them; apply_code_action applies one by index or title, after the write rules check the files it changes). file switches the editor to that file first. Fails with \"no editor is open\" when the " +
			"user has not opened one. Prefer edit_file for plain edits when the editor is not needed.",
		Schema: tools.Schema(map[string]any{
			"action":      tools.Enum("What to do.", actions...),
			"file":        tools.Str("Switch the editor to this file first (relative to the project root)."),
			"startLine":   tools.Int("First line, 1-based. For read, edit, select, highlight and the range of read."),
			"endLine":     tools.Int("Last line, 1-based, inclusive. Defaults to startLine."),
			"replacement": tools.Str("edit: the new text for lines startLine-endLine. Empty deletes them."),
			"line":        tools.Int("navigate, goto_cursor: line, 1-based."),
			"col":         tools.Int("navigate, goto_cursor: column, 1-based."),
			"search":      tools.Str("navigate: move to the next match of this pattern."),
			"newName":     tools.Str("rename: the new symbol name."),
			"jump":        tools.Bool("definition: jump to the first result."),
			"text":        tools.Str("yank: the text to copy."),
			"register":    tools.Str("yank: the register. Default + (the system clipboard)."),
			"count":       tools.Int("terminal_output: how many lines. Default 100."),
			"index":       tools.Int("apply_code_action: the number of the action from code_actions."),
			"title":       tools.Str("apply_code_action: part of the action's title, when index is not given."),
		}, "action"),
	}
}

func (tool) ReadOnly() bool { return false }

// ReadOnlyCall reports whether this action only reads.
func (tool) ReadOnlyCall(args json.RawMessage) bool {
	var a Args
	if json.Unmarshal(args, &a) != nil {
		return false
	}
	return readOnlyActions[a.Action]
}

func (tool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	switch GetAccess() {
	case AccessOff:
		return tools.Result{Output: "Editor tool is disabled.", IsError: true, Tag: tagEditorDisabled}
	case AccessWhenOpen:
		if Current() == nil {
			return tools.Result{Output: "Editor tool requires the editor panel to be open. Use edit_file instead, or open the editor panel.",
				IsError: true, Tag: tagEditorNotOpen}
		}
	}
	var a Args
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("bad arguments: %v", err)
	}
	if !validAction(a.Action) {
		return tools.Errorf("unknown action %q; use one of: %s", a.Action, strings.Join(actions, ", "))
	}
	s := Current()
	if s == nil {
		return tools.Result{Output: "No editor is open. Ask the user to open it (Ctrl+E or /editor), or use edit_file instead.",
			IsError: true, Tag: tagEditorNotOpen}
	}
	if s.socket == "" {
		return tools.Errorf("the open editor is not Neovim, so the editor tool cannot reach it")
	}
	cctx, cancel := context.WithTimeout(ctx, toolWait)
	defer cancel()

	workdir := ""
	if env != nil {
		workdir = env.Workdir
	}
	if a.File != "" {
		abs := a.File
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(workdir, a.File)
		}
		abs = filepath.Clean(abs)
		if err := fs.CheckForbidden(env, abs); err != nil {
			return tools.Result{Output: err.Error(), IsError: true, Tag: "forbidden"}
		}
		ok, text, err := s.switchTo(cctx, abs)
		if err != nil {
			return tools.Errorf("%v", err)
		}
		if !ok {
			return tools.Result{Output: text, IsError: true}
		}
	}
	// The current buffer is checked too: the user may have it open already.
	if name, err := s.currentName(cctx); err != nil {
		return tools.Errorf("%v", err)
	} else if name != "" {
		if err := fs.CheckForbidden(env, name); err != nil {
			return tools.Result{Output: err.Error(), IsError: true, Tag: "forbidden"}
		}
	}

	params := map[string]any{}
	if a.StartLine > 0 {
		params["startLine"] = int64(a.StartLine)
	}
	if a.EndLine > 0 {
		params["endLine"] = int64(a.EndLine)
	}
	if a.Replacement != nil {
		params["replacement"] = *a.Replacement
	}
	if a.Line > 0 {
		params["line"] = int64(a.Line)
	}
	if a.Col > 0 {
		params["col"] = int64(a.Col)
	}
	if a.Search != "" {
		params["search"] = a.Search
	}
	if a.NewName != "" {
		params["newName"] = a.NewName
	}
	if a.Jump {
		params["jump"] = true
	}
	if a.Text != nil {
		params["text"] = *a.Text
	}
	if a.Register != "" {
		params["register"] = a.Register
	}
	if a.Count > 0 {
		params["count"] = int64(a.Count)
	}

	var abs string
	if a.Action == "edit" {
		abs = s.currentAbs(cctx)
		if abs != "" {
			if env != nil && env.Files != nil {
				env.Files.BeforeWrite(abs)
			}
			s.markToolWrite(abs)
		}
	}
	if a.Action == "apply_code_action" || a.Action == "rename" {
		// Look first: the files the edit would change must pass the write
		// rules (secret files, .git, CI and container definitions) before any
		// of them is edited. A preview that fails is left to the real call,
		// which reports the error in its usual words.
		params["preview"] = true
		prev, err := s.runAction(cctx, a.Action, params)
		delete(params, "preview")
		if err == nil {
			var pv struct {
				Title string   `json:"title"`
				Files []string `json:"files"`
			}
			if json.Unmarshal([]byte(prev), &pv) == nil {
				for _, f := range pv.Files {
					if env == nil {
						return tools.Errorf("%s needs a project environment to check its files", a.Action)
					}
					if err := fs.CheckWrite(ctx, env, ToolName, f); err != nil {
						return tools.Result{Output: fmt.Sprintf("%v\nNo change was made. '%s' changes this file, so it was not applied.", err, pv.Title), IsError: true, Tag: "forbidden"}
					}
				}
			}
		}
	}
	text, err := s.runAction(cctx, a.Action, params)
	if err != nil {
		if strings.Contains(err.Error(), "__NO_LSP__") {
			return tools.Result{Output: "no LSP client attached for this buffer", IsError: true}
		}
		return tools.Errorf("%s: %v", a.Action, err)
	}
	res := tools.Result{Output: text, Summary: a.Action}
	if a.Action == "edit" && abs != "" {
		res.Files = []string{relTo(workdir, abs)}
	}
	return res
}

func validAction(a string) bool {
	for _, v := range actions {
		if v == a {
			return true
		}
	}
	return false
}

// relTo is abs relative to workdir when it is inside it, else abs.
func relTo(workdir, abs string) string {
	if workdir == "" {
		return abs
	}
	if r, err := filepath.Rel(workdir, abs); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return abs
}
