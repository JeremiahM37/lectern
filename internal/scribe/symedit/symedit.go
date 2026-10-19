// Package symedit is Scribe's symbol-addressed editing tool, ast_edit. It
// finds a definition by name and rewrites only that span, so the model does
// not have to reproduce surrounding text. Go is located with go/parser and
// other languages with repomap's extractors.
package symedit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/web"
)

// Register adds ast_edit, move_symbol and rename_file to r.
func Register(r *tools.Registry) {
	r.Register(&astEdit{})
	r.Register(&moveSymbolTool{})
	r.Register(&renameFileTool{})
}

const toolName = "ast_edit"

var actionNames = []string{
	"replace", "replace_body", "insert_before", "insert_after", "delete", "rename_local", "replace_in_body", "add_import",
	"insert_text",
	"add_named_import", "remove_named_import", "remove_import", "set_module_specifier",
	"set_return_type", "set_async", "add_parameter", "remove_parameter", "set_type", "set_initializer", "remove_initializer", "rename",
	"add_method", "add_property", "remove_member",
	"set_extends", "create_file",
	"set_static", "add_getter", "add_setter", "add_constructor", "add_decorator", "remove_decorator",
}

const description = "Edit one definition by name instead of matching text. symbol is a name such as ParseList, " +
	"a method as Server.Start, or a kind-qualified name such as \"class Foo\". Actions: replace (new_code is the full " +
	"definition, doc comment included), replace_body (new_code is the body only, without braces), insert_before, " +
	"insert_after, delete, rename_local (old_name to new_name inside the symbol), replace_in_body (old_string to " +
	"new_string, must match once inside the symbol; with end_string, the span from old_string through end_string " +
	"is replaced), add_import (new_code is the import; no symbol), insert_text (new_code at a file anchor: index 0 " +
	"for the top, -1 for the bottom, or a line; or value after-imports / before-exports; no symbol). " +
	"Module edits for TypeScript and JavaScript (no symbol; symbol is the module path, new_code the names or the new " +
	"path): add_named_import, remove_named_import, remove_import, set_module_specifier. Micro edits: " +
	"set_return_type, set_async, add_parameter and remove_parameter (new_code is the parameter name), set_type, " +
	"set_initializer, remove_initializer, and rename (declaration name only; new_name). Member edits on a class: " +
	"add_method and add_property (new_code is the member), remove_member (symbol Owner.member). " +
	"set_extends (class or interface; new_code is the base, Python parents or the extends clause; empty new_code removes it). " +
	"add_getter, add_setter and add_constructor (class; new_code is the full declaration, like add_method). " +
	"set_static (member; new_code \"false\" makes it an instance member again). " +
	"symbol may also be a moniker from a symbol listing, such as internal/app.go#function:ParseList, which names one definition exactly. " +
	"add_decorator and remove_decorator (Python, TypeScript, JavaScript, Java; new_code is the decorator, such as @dataclass or Injectable). " +
	"create_file makes a new file whose full content is new_code, with no symbol; it cannot be combined with other operations. " +
	"Several changes to one file can go in edits, applied atomically. Set apply to false to preview the diff without " +
	"writing. The file must be read first. " +
	"Go, TypeScript/JavaScript, Python, Rust, Java, Kotlin, C#, Swift, C/C++, Ruby, PHP, Lua, Zig and Elixir are supported."

// op is one operation. Top-level arguments and each entry of edits share it.
type op struct {
	Action    string `json:"action"`
	Symbol    string `json:"symbol"`
	NewCode   string `json:"new_code"`
	OldString string `json:"old_string"`
	EndString string `json:"end_string"`
	NewString string `json:"new_string"`
	OldName   string `json:"old_name"`
	NewName   string `json:"new_name"`
	// Index is the insert_text anchor: a line number, 0 (top) or -1 (bottom).
	// Raw so that a numeric string is accepted too.
	Index json.RawMessage `json:"index"`
	Value string          `json:"value"`
}

type args struct {
	Path  string          `json:"path"`
	Edits json.RawMessage `json:"edits"`
	// Apply false checks the edit and returns its diff without writing.
	// Nil means true, so existing calls still write.
	Apply *bool `json:"apply"`
	op
}

type astEdit struct{}

func (t *astEdit) Spec() llm.ToolSpec {
	item := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":     tools.Enum("Operation.", actionNames...),
			"symbol":     tools.Str("Definition to edit, e.g. ParseList, Server.Start or class Foo."),
			"new_code":   tools.Str("New code for replace, replace_body, insert_before, insert_after, add_import, create_file (the whole file) and set_extends (the base)."),
			"old_string": tools.Str("replace_in_body: text to find inside the symbol (the start anchor when end_string is given)."),
			"end_string": tools.Str("replace_in_body: end anchor. With it, the span from old_string through end_string is replaced by new_string. Both must be unique inside the symbol."),
			"new_string": tools.Str("replace_in_body: replacement text."),
			"old_name":   tools.Str("rename_local: identifier to rename."),
			"new_name":   tools.Str("rename_local and rename: new identifier."),
			"index":      tools.Int("insert_text: 0 for the top of the file, -1 for the bottom, or a 1-based line to insert before."),
			"value":      tools.Str("insert_text: after-imports or before-exports (instead of index)."),
		},
		"required": []string{"action"},
	}
	return llm.ToolSpec{
		Name:        toolName,
		Description: description,
		Schema: tools.Schema(map[string]any{
			"path":       tools.Str("File to edit, relative to the project root."),
			"symbol":     tools.Str("Definition to edit (single-operation form)."),
			"action":     tools.Enum("Operation (single-operation form).", actionNames...),
			"new_code":   tools.Str("New code (single-operation form)."),
			"old_string": tools.Str("replace_in_body: exact text to find inside the symbol (the start anchor when end_string is given)."),
			"end_string": tools.Str("replace_in_body: end anchor for a range replace (see edits)."),
			"new_string": tools.Str("replace_in_body: replacement text."),
			"old_name":   tools.Str("rename_local: identifier to rename."),
			"new_name":   tools.Str("rename_local and rename: new identifier."),
			"index":      tools.Int("insert_text: 0 for the top of the file, -1 for the bottom, or a 1-based line to insert before."),
			"value":      tools.Str("insert_text: after-imports or before-exports (instead of index)."),
			"apply":      tools.Bool("Set false to preview: the diff is returned and nothing is written. Default true."),
			"edits":      tools.Arr("Several operations on this file, applied in order. All or nothing.", item),
		}, "path"),
	}
}

func (t *astEdit) ReadOnly() bool { return false }

func (t *astEdit) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) (res tools.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = tools.Errorf("%s failed unexpectedly: %v", toolName, r)
		}
	}()
	if env == nil {
		return tools.Errorf("%s: no environment", toolName)
	}
	var a args
	if err := tools.Decode(raw, &a); err != nil {
		return fail(err)
	}
	ops, err := a.plan()
	if err != nil {
		return fail(err)
	}
	abs, rel, err := gate(ctx, env, a.Path)
	if err != nil {
		return fail(err)
	}
	for _, o := range ops {
		if o.Action == "create_file" {
			if len(ops) > 1 {
				return fail(fmt.Errorf("create_file must be the only operation in a call: it writes a whole new file"))
			}
			return createFile(ctx, env, a, abs, rel)
		}
	}
	fi, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return tools.Errorf("File not found: %s. %s edits existing files; to make a new file, use action create_file with new_code as its full content (or write_file).", rel, toolName)
	}
	if err != nil {
		return fail(err)
	}
	if fi.IsDir() {
		return tools.Errorf("%s is a directory.", rel)
	}
	if env.Files != nil {
		if !env.Files.WasRead(abs) {
			return tools.Errorf("%s has not been read this session. Read it first so the edit is based on its current content.", rel)
		}
		if env.Files.StaleSinceRead(abs) {
			return tools.Errorf("%s changed on disk since you read it. Read it again, then retry the edit.", rel)
		}
	}
	lang, ok := languages[strings.ToLower(filepath.Ext(abs))]
	if !ok {
		return tools.Errorf("%s only supports source files it can parse. Use edit_file for %q.", toolName, rel)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return tools.Errorf("read %s: %v", rel, err)
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return tools.Errorf("Cannot edit binary file: %s.", rel)
	}
	crlf := bytes.Contains(data, []byte("\r\n"))
	orig := strings.ReplaceAll(string(data), "\r\n", "\n")

	ed := &editor{lang: lang, rel: rel, base: filepath.Base(abs)}
	defer ed.close()
	cur := orig
	var msgs, names []string
	allNoop := true
	for i, o := range ops {
		next, msg, name, err := ed.apply(cur, o)
		if err != nil {
			if len(ops) > 1 {
				return tools.Errorf("%s: edit %d of %d (%s): %v\nNO edits were applied (atomic rollback).", rel, i+1, len(ops), o.Action, err)
			}
			return tools.Errorf("%s: %v. No changes were written.", rel, err)
		}
		if next == cur {
			// The operation changed nothing. It is a success only when it
			// says so explicitly (add_import: "already present").
			if !explicitNoop(msg) {
				allNoop = false
			}
			msg = "no-op: " + msg
		}
		cur = next
		msgs = append(msgs, msg)
		if !contains(names, name) {
			names = append(names, name)
		}
	}
	final, err := validate(ed, orig, cur)
	if err != nil {
		if len(ops) > 1 {
			return tools.Errorf("%s: %v NO edits were applied (atomic rollback).", rel, err)
		}
		return tools.Errorf("%s: %v No changes were written.", rel, err)
	}
	if final == orig {
		if allNoop {
			return tools.Result{Output: fmt.Sprintf("No changes to %s: %s.", rel, strings.Join(msgs, "; ")), Summary: "no changes"}
		}
		return tools.Result{
			Output:  fmt.Sprintf("No changes produced by the operations on %s: %s. The new code is identical to what is there. Check the action and new_code, or read the file again.", rel, strings.Join(msgs, "; ")),
			Summary: "no changes", IsError: true,
		}
	}
	if apply := a.Apply == nil || *a.Apply; !apply {
		diff, added, removed := fs.UnifiedDiff(rel, orig, final)
		return tools.Result{
			Output:  fmt.Sprintf("Preview, not written (apply is false): %s in %s. Pass apply: true, or leave it out, to write it.", strings.Join(msgs, "; "), rel),
			Summary: fmt.Sprintf("preview +%d -%d in %s", added, removed, strings.Join(names, ", ")),
			Diff:    diff,
		}
	}
	out := final
	if crlf {
		out = strings.ReplaceAll(final, "\n", "\r\n")
	}
	if env.Files != nil {
		env.Files.BeforeWrite(abs)
	}
	if err := os.WriteFile(abs, []byte(out), fi.Mode().Perm()); err != nil {
		return tools.Errorf("write %s: %v", rel, err)
	}
	if env.Files != nil {
		env.Files.MarkRead(abs)
	}
	diff, added, removed := fs.UnifiedDiff(rel, orig, final)
	return tools.Result{
		Output:  fmt.Sprintf("%s in %s.", strings.Join(msgs, "; "), rel),
		Summary: fmt.Sprintf("+%d -%d in %s", added, removed, strings.Join(names, ", ")),
		Diff:    diff,
		Files:   []string{rel},
	}
}

// explicitNoop reports whether an operation that left the text unchanged said
// so in the words "already present". That is success: the file already has the
// requested state. An unchanged result without those words means the edit did
// nothing, and the batch is reported as an error so the model reads again.
func explicitNoop(msg string) bool {
	return strings.Contains(msg, "already present")
}

// plan validates the arguments and returns the operations in order.
func (a args) plan() ([]op, error) {
	var list []op
	batch := len(bytes.TrimSpace(a.Edits)) > 0 && string(bytes.TrimSpace(a.Edits)) != "null"
	if batch {
		if !a.op.isZero() {
			return nil, fmt.Errorf("pass either the single-operation fields or edits, not both")
		}
		var err error
		if list, err = decodeEdits(a.Edits); err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("edits must contain at least one operation")
		}
	} else {
		list = []op{a.op}
	}
	for i := range list {
		o := &list[i]
		o.NewCode, o.OldString, o.NewString = toLF(o.NewCode), toLF(o.OldString), toLF(o.NewString)
		// Models often put the new name or parameter name in new_code or
		// old_name; accept those for the operations that take one name.
		switch o.Action {
		case "rename":
			if strings.TrimSpace(o.NewName) == "" {
				o.NewName = strings.TrimSpace(o.NewCode)
			}
		case "remove_parameter":
			if strings.TrimSpace(o.NewCode) == "" {
				o.NewCode = o.OldName
			}
		}
		if err := o.check(); err != nil {
			if len(list) > 1 {
				return nil, fmt.Errorf("edit %d: %v", i+1, err)
			}
			return nil, err
		}
	}
	return list, nil
}

// isZero reports whether no field of o was set.
func (o op) isZero() bool {
	return reflect.ValueOf(o).IsZero()
}

func (o op) check() error {
	known := false
	for _, n := range actionNames {
		known = known || n == o.Action
	}
	if !known {
		return fmt.Errorf("unknown action %q. Use one of: %s", o.Action, strings.Join(actionNames, ", "))
	}
	if !fileLevelAction(o.Action) && strings.TrimSpace(o.Symbol) == "" {
		return fmt.Errorf("symbol is required for %s", o.Action)
	}
	switch o.Action {
	case "replace", "insert_before", "insert_after", "add_import", "add_method", "add_property", "add_parameter",
		"set_return_type", "set_type", "set_initializer", "insert_text", "create_file",
		"add_getter", "add_setter", "add_constructor", "add_decorator", "remove_decorator":
		if isBlank(o.NewCode) {
			return fmt.Errorf("new_code is required for %s", o.Action)
		}
	case "rename":
		if strings.TrimSpace(o.NewName) == "" {
			return fmt.Errorf("rename needs new_name, the new name for the declaration")
		}
	case "remove_parameter":
		if strings.TrimSpace(o.NewCode) == "" {
			return fmt.Errorf("remove_parameter needs new_code: the name of the parameter to remove")
		}
	case "rename_local":
		if o.OldName == "" || o.NewName == "" {
			return fmt.Errorf("rename_local needs old_name and new_name")
		}
		if o.OldName == o.NewName {
			return fmt.Errorf("old_name and new_name are identical")
		}
	case "replace_in_body":
		if o.OldString == "" {
			return fmt.Errorf("replace_in_body needs old_string")
		}
		if o.OldString == o.NewString {
			return fmt.Errorf("old_string and new_string are identical, so there is nothing to change")
		}
	}
	return nil
}

func decodeEdits(raw json.RawMessage) ([]op, error) {
	s := bytes.TrimSpace(raw)
	if len(s) > 0 && s[0] == '"' {
		var inner string
		if err := json.Unmarshal(s, &inner); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
		s = []byte(inner)
	}
	var list []op
	if err := json.Unmarshal(s, &list); err != nil {
		return nil, fmt.Errorf("invalid edits: %w", err)
	}
	return list, nil
}

// validate checks the result before anything is written. Go is formatted
// with go/format and must parse. Other languages must keep their brackets
// balanced when they were balanced before.
func validate(ed *editor, orig, final string) (string, error) {
	if ed.lang.fam == famGo {
		if _, err := parser.ParseFile(token.NewFileSet(), ed.rel, final, parser.AllErrors); err != nil {
			return "", fmt.Errorf("the result would not parse as Go: %s.", goErrors(err))
		}
		formatted, err := format.Source([]byte(final))
		if err != nil {
			return "", fmt.Errorf("the result would not parse as Go: %v.", err)
		}
		return string(formatted), nil
	}
	if _, msg := balance(mask(orig, ed.lang.lex)); msg == "" {
		if line, msg := balance(mask(final, ed.lang.lex)); msg != "" {
			return "", fmt.Errorf("the result would leave brackets unbalanced at line %d: %s.", line, msg)
		}
	}
	return final, nil
}

func goErrors(err error) string {
	el, ok := err.(scanner.ErrorList)
	if !ok || len(el) == 0 {
		return err.Error()
	}
	var parts []string
	for i, e := range el {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d more", len(el)-3))
			break
		}
		parts = append(parts, fmt.Sprintf("line %d: %s", e.Pos.Line, e.Msg))
	}
	return strings.Join(parts, "; ")
}

// gate applies the path rules that edits need: no writes into .git, approval
// for secret-looking files, and approval for writes outside the project.
func gate(ctx context.Context, env *tools.Env, p string) (string, string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", "", fmt.Errorf("path is required")
	}
	if env.Workdir == "" {
		return "", "", fmt.Errorf("no project directory is set")
	}
	wd := filepath.Clean(env.Workdir)
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(wd, abs)
	}
	abs = filepath.Clean(abs)
	rel := filepath.ToSlash(abs)
	if r, err := filepath.Rel(wd, abs); err == nil {
		rel = filepath.ToSlash(r)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".git" {
			return "", "", fmt.Errorf("Refusing to write inside .git (%s). Git internals are not edited directly.", rel)
		}
	}
	// The forbidden-path and secret-file rules the fs edit tools use: secret
	// names are refused, CI and container files need approval.
	if err := fs.CheckWrite(ctx, env, "edit", abs); err != nil {
		return "", "", err
	}
	if !within(realPath(wd), realPath(abs)) && env.Mode != "auto" {
		if msg := approve(ctx, env, "write_outside", "Write outside project directory", abs); msg != "" {
			return "", "", errors.New(msg)
		}
	}
	return abs, rel, nil
}

// approve asks the host about one action. It returns "" when the user allowed
// it, and otherwise the message the model reads, as fs does.
func approve(ctx context.Context, env *tools.Env, kind, title, detail string) string {
	what := strings.ToLower(title) + ": " + detail
	if env.Host == nil {
		return "Denied: " + what + ". No one is available to approve it."
	}
	rctx, reason := tools.WithReasonSlot(ctx)
	if env.Host.Approve(rctx, tools.Approval{Tool: toolName, Kind: kind, Title: title, Detail: detail}) == tools.Deny {
		return web.UserDeclined(what, reason())
	}
	tools.NoteApproval(ctx, reason())
	return ""
}

// realPath resolves symlinks in p. A path that does not exist yet is judged
// by its deepest existing ancestor.
func realPath(p string) string {
	cur, rest := p, ""
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func within(root, p string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// isSecretName mirrors the secret-looking file names that the file tools ask
// about before reading or writing.
func isSecretName(name string) bool {
	n := strings.ToLower(name)
	for _, g := range []string{".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "*.keystore", "*.jks",
		"id_rsa*", "id_ed25519*", "id_ecdsa*", "id_dsa*", "*credentials*", "*private_key*", "*private-key*",
		".npmrc", ".pypirc", ".netrc", ".htpasswd", "secret.*", "secrets.*", "token.json", "tokens.json", "service-account*.json"} {
		if ok, _ := filepath.Match(g, n); ok {
			return true
		}
	}
	return false
}

func fail(err error) tools.Result {
	return tools.Result{Output: err.Error(), IsError: true}
}

func toLF(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// fileLevelAction says whether an action works on the file as a whole, so it
// takes no symbol.
func fileLevelAction(action string) bool {
	switch action {
	case "add_import", "insert_text", "create_file":
		return true
	}
	return isModuleOp(action)
}
