// Package symedit is Scribe's symbol-addressed editing tool, ast_edit. It
// finds a definition by name and rewrites only that span, so the model does
// not have to reproduce surrounding text. Go is located with go/parser and
// other languages with repomap's extractors.
package symedit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
)

// Register adds ast_edit to r.
func Register(r *tools.Registry) { r.Register(&astEdit{}) }

const toolName = "ast_edit"

var actionNames = []string{"replace", "replace_body", "insert_before", "insert_after", "delete", "rename_local", "replace_in_body", "add_import"}

const description = "Edit one definition by name instead of matching text. symbol is a name such as ParseList, " +
	"a method as Server.Start, or a kind-qualified name such as \"class Foo\". Actions: replace (new_code is the full " +
	"definition, doc comment included), replace_body (new_code is the body only, without braces), insert_before, " +
	"insert_after, delete, rename_local (old_name to new_name inside the symbol), replace_in_body (old_string to " +
	"new_string, must match once inside the symbol), add_import (new_code is the import; no symbol). " +
	"Several changes to one file can go in edits, applied atomically. The file must be read first. " +
	"Go, TypeScript/JavaScript, Python, Rust, Java, Kotlin, C#, Swift, C/C++, Ruby, PHP, Lua, Zig and Elixir are supported."

// op is one operation. Top-level arguments and each entry of edits share it.
type op struct {
	Action    string `json:"action"`
	Symbol    string `json:"symbol"`
	NewCode   string `json:"new_code"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
	OldName   string `json:"old_name"`
	NewName   string `json:"new_name"`
}

type args struct {
	Path  string          `json:"path"`
	Edits json.RawMessage `json:"edits"`
	op
}

type astEdit struct{}

func (t *astEdit) Spec() llm.ToolSpec {
	item := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":     tools.Enum("Operation.", actionNames...),
			"symbol":     tools.Str("Definition to edit, e.g. ParseList, Server.Start or class Foo."),
			"new_code":   tools.Str("New code for replace, replace_body, insert_before, insert_after and add_import."),
			"old_string": tools.Str("replace_in_body: exact text to find inside the symbol."),
			"new_string": tools.Str("replace_in_body: replacement text."),
			"old_name":   tools.Str("rename_local: identifier to rename."),
			"new_name":   tools.Str("rename_local: new identifier."),
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
			"old_string": tools.Str("replace_in_body: exact text to find inside the symbol."),
			"new_string": tools.Str("replace_in_body: replacement text."),
			"old_name":   tools.Str("rename_local: identifier to rename."),
			"new_name":   tools.Str("rename_local: new identifier."),
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
	fi, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return tools.Errorf("File not found: %s. %s edits existing files; use write_file to create one.", rel, toolName)
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
	for i, o := range ops {
		next, msg, name, err := ed.apply(cur, o)
		if err != nil {
			if len(ops) > 1 {
				return tools.Errorf("%s: edit %d of %d (%s): %v\nNO edits were applied (atomic rollback).", rel, i+1, len(ops), o.Action, err)
			}
			return tools.Errorf("%s: %v. No changes were written.", rel, err)
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
		return tools.Result{Output: fmt.Sprintf("No changes to %s: %s.", rel, strings.Join(msgs, "; ")), Summary: "no changes"}
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

// plan validates the arguments and returns the operations in order.
func (a args) plan() ([]op, error) {
	var list []op
	batch := len(bytes.TrimSpace(a.Edits)) > 0 && string(bytes.TrimSpace(a.Edits)) != "null"
	if batch {
		if a.op != (op{}) {
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
		if err := o.check(); err != nil {
			if len(list) > 1 {
				return nil, fmt.Errorf("edit %d: %v", i+1, err)
			}
			return nil, err
		}
	}
	return list, nil
}

func (o op) check() error {
	known := false
	for _, n := range actionNames {
		known = known || n == o.Action
	}
	if !known {
		return fmt.Errorf("unknown action %q. Use one of: %s", o.Action, strings.Join(actionNames, ", "))
	}
	if o.Action != "add_import" && strings.TrimSpace(o.Symbol) == "" {
		return fmt.Errorf("symbol is required for %s", o.Action)
	}
	switch o.Action {
	case "replace", "insert_before", "insert_after", "add_import":
		if isBlank(o.NewCode) {
			return fmt.Errorf("new_code is required for %s", o.Action)
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
	if isSecretName(filepath.Base(abs)) && !approve(ctx, env, "sensitive", "Edit sensitive file", rel) {
		return "", "", fmt.Errorf("Denied: edit to sensitive file %s", rel)
	}
	if !within(realPath(wd), realPath(abs)) && env.Mode != "auto" && !approve(ctx, env, "write_outside", "Write outside project directory", abs) {
		return "", "", fmt.Errorf("Denied: %s outside project directory → %s", toolName, rel)
	}
	return abs, rel, nil
}

func approve(ctx context.Context, env *tools.Env, kind, title, detail string) bool {
	if env.Host == nil {
		return false
	}
	return env.Host.Approve(ctx, tools.Approval{Tool: toolName, Kind: kind, Title: title, Detail: detail}) != tools.Deny
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
