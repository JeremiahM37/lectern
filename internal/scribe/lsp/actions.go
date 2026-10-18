package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// rawAction is one entry of a textDocument/codeAction response. Raw keeps the
// entry as the server sent it, so it can be sent back to codeAction/resolve.
type rawAction struct {
	Title    string          `json:"title"`
	Kind     string          `json:"kind"`
	Edit     *WorkspaceEdit  `json:"edit"`
	Command  json.RawMessage `json:"command"`
	Disabled *struct {
		Reason string `json:"reason"`
	} `json:"disabled"`
	Data json.RawMessage `json:"data"`
	raw  json.RawMessage
}

// Actions lists the code actions the server offers for lines startLine to
// endLine of abs. only, when non-empty, restricts them to those kinds (and
// their sub-kinds). diagFilter, when set, sends the diagnostics on those lines
// whose message contains it as the context of the request, which is what
// servers need before they offer quick fixes. Actions that carry no edit are
// resolved with codeAction/resolve when the server supports it. Nothing is
// applied.
func (m *Manager) Actions(ctx context.Context, abs string, startLine, endLine int, only []string, diagFilter string) ([]CodeAction, error) {
	var diags []FileDiagnostic
	if diagFilter != "" {
		all, err := m.Diagnostics(ctx, abs)
		if err != nil {
			return nil, err
		}
		for _, d := range all {
			if d.Line < startLine || d.Line > endLine {
				continue
			}
			if strings.Contains(strings.ToLower(d.Message), strings.ToLower(diagFilter)) {
				diags = append(diags, d)
			}
		}
	}
	return m.actionsWith(ctx, abs, startLine, endLine, only, diags)
}

// actionsWith is Actions for diagnostics the caller already has. Each one is
// sent as the context of the request.
func (m *Manager) actionsWith(ctx context.Context, abs string, startLine, endLine int, only []string, diags []FileDiagnostic) ([]CodeAction, error) {
	var out []CodeAction
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		if _, _, ok := lineBounds(sr.text, startLine-1); !ok {
			return fmt.Errorf("line %d is past the end of %s", startLine, m.RelPath(abs))
		}
		lastStart, lastEnd, ok := lineBounds(sr.text, endLine-1)
		if !ok {
			return fmt.Errorf("line %d is past the end of %s", endLine, m.RelPath(abs))
		}
		rng := Range{
			Start: Position{Line: startLine - 1, Character: 0},
			End:   pointToPosition(sr.text, Point{Line: endLine, Column: utf8.RuneCountInString(sr.text[lastStart:lastEnd]) + 1}),
		}
		ctxDiags := make([]map[string]any, 0, len(diags))
		for _, d := range diags {
			ctxDiags = append(ctxDiags, lspDiagnostic(sr.text, d))
		}
		context := map[string]any{"diagnostics": ctxDiags}
		if len(only) > 0 {
			context["only"] = only
		}
		var raws []json.RawMessage
		if err := s.call(ctx, "textDocument/codeAction", map[string]any{
			"textDocument": map[string]any{"uri": sr.uri},
			"range":        rng,
			"context":      context,
		}, &raws); err != nil {
			return err
		}
		for _, raw := range raws {
			var a rawAction
			if err := json.Unmarshal(raw, &a); err != nil {
				continue
			}
			a.raw = raw
			if !kindWanted(a.Kind, only) {
				continue
			}
			if a.Edit == nil && len(a.Data) > 0 && a.Disabled == nil && !hasCommand(a.Command) {
				var resolved rawAction
				if err := s.call(ctx, "codeAction/resolve", json.RawMessage(raw), &resolved); err == nil && resolved.Edit != nil {
					a.Edit = resolved.Edit
				}
			}
			ca := CodeAction{Title: a.Title, Kind: a.Kind, Edit: a.Edit, Command: hasCommand(a.Command)}
			if a.Disabled != nil {
				ca.Disabled = a.Disabled.Reason
				if ca.Disabled == "" {
					ca.Disabled = "the server did not say why"
				}
			}
			out = append(out, ca)
		}
		return nil
	})
	return out, err
}

// kindWanted reports whether an action of kind matches one of only. Servers
// are asked for only kinds already, but not all of them honour it.
func kindWanted(kind string, only []string) bool {
	if len(only) == 0 || kind == "" {
		return true
	}
	for _, o := range only {
		if kind == o || strings.HasPrefix(kind, o+".") {
			return true
		}
	}
	return false
}

func hasCommand(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}

// lspDiagnostic turns a FileDiagnostic back into the wire form a server
// expects in a codeAction context.
func lspDiagnostic(text string, d FileDiagnostic) map[string]any {
	st := pointToPosition(text, Point{Line: d.Line, Column: d.Column})
	en := pointToPosition(text, Point{Line: d.EndLine, Column: d.EndCol})
	if d.EndLine == 0 {
		en = st
	}
	out := map[string]any{
		"range":    Range{Start: st, End: en},
		"message":  d.Message,
		"severity": severityNumber(d.Severity),
	}
	if d.Source != "" {
		out["source"] = d.Source
	}
	if d.Code != "" && d.Code != "<nil>" {
		out["code"] = d.Code
	}
	return out
}

func severityNumber(name string) int {
	switch name {
	case "warning":
		return 2
	case "info":
		return 3
	case "hint":
		return 4
	}
	return 1
}

// pickAction chooses one of actions by its 1-based number or by a title
// fragment. It returns an error that lists the choices when the answer is
// unclear.
func pickAction(actions []CodeAction, choose json.RawMessage) (CodeAction, error) {
	raw := strings.TrimSpace(string(choose))
	var s string
	if json.Unmarshal(choose, &s) == nil {
		raw = strings.TrimSpace(s)
	}
	if v, err := strconv.Atoi(raw); err == nil {
		if v < 1 || v > len(actions) {
			return CodeAction{}, fmt.Errorf("choose %d is not on the list: pick a number from 1 to %d", v, len(actions))
		}
		return actions[v-1], nil
	}
	var hits []CodeAction
	for _, a := range actions {
		if strings.Contains(strings.ToLower(a.Title), strings.ToLower(raw)) {
			hits = append(hits, a)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return CodeAction{}, fmt.Errorf("no action title contains %q. Pick a number from the list", raw)
	}
	var titles []string
	for _, h := range hits {
		titles = append(titles, h.Title)
	}
	return CodeAction{}, fmt.Errorf("%q matches %d actions (%s). Pick a number from the list", raw, len(hits), strings.Join(titles, "; "))
}

// describeActions renders the numbered list the model chooses from.
func describeActions(actions []CodeAction) string {
	var b strings.Builder
	for i, a := range actions {
		fmt.Fprintf(&b, "%d. %s", i+1, a.Title)
		if a.Kind != "" {
			fmt.Fprintf(&b, " [%s]", a.Kind)
		}
		switch {
		case a.Disabled != "":
			fmt.Fprintf(&b, " (unavailable: %s)", a.Disabled)
		case a.Edit == nil && a.Command:
			b.WriteString(" (runs a server command; cannot be applied here)")
		case a.Edit == nil:
			b.WriteString(" (no edit)")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// applicable reports whether an action can be applied by this tool.
func applicable(a CodeAction) error {
	if a.Disabled != "" {
		return fmt.Errorf("%q is not available: %s. Pick another action", a.Title, a.Disabled)
	}
	if a.Edit == nil {
		return fmt.Errorf("%q runs a server command rather than editing files, so it cannot be applied here. Pick another action, or make the change with edit_file", a.Title)
	}
	return nil
}

// --- code_action ------------------------------------------------------------

type codeActionTool struct{ m *Manager }

func (t *codeActionTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "code_action",
		Description: "The language server's code actions (quick fixes, refactors, source fixes) for a line or range, " +
			"and applying one. Call with only path and line to list them as a numbered list. To apply one, call again " +
			"with the same path, line and end_line and choose set to its number (or part of its title). " +
			"diagnostic narrows the fixes to the errors on those lines whose message contains the text. " +
			"Applying writes the edit to disk, like edit_file: read the file first. Set apply to false to preview the diff.",
		Schema: tools.Schema(map[string]any{
			"path":       tools.Str("File, relative to the project root."),
			"line":       tools.Int("1-based line the action applies to (the first line of the range)."),
			"end_line":   tools.Int("Last line of the range. Defaults to line."),
			"diagnostic": tools.Str("Text from the message of a diagnostic on those lines, so quick fixes for it are offered."),
			"only":       tools.Str("Kind to restrict the list to, such as quickfix or refactor."),
			"choose":     tools.Str("Number of the action from the list, or part of its title. Leave out to list."),
			"apply":      tools.Bool("Apply the chosen action (default true). false previews the diff only."),
		}, "path", "line"),
	}
}

func (t *codeActionTool) ReadOnly() bool { return false }

// ReadOnlyCall: listing is read-only; choosing an action writes.
func (t *codeActionTool) ReadOnlyCall(args json.RawMessage) bool {
	var a struct {
		Choose json.RawMessage `json:"choose"`
	}
	_ = tools.Decode(args, &a)
	return len(strings.TrimSpace(string(a.Choose))) == 0 || string(a.Choose) == "null"
}

type actionArgs struct {
	Path       string          `json:"path"`
	Line       int             `json:"line"`
	EndLine    int             `json:"end_line"`
	Diagnostic string          `json:"diagnostic"`
	Only       string          `json:"only"`
	Choose     json.RawMessage `json:"choose"`
	Apply      *bool           `json:"apply"`
}

func (t *codeActionTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var a actionArgs
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if a.Path == "" {
		return tools.Errorf("code_action needs path")
	}
	if a.Line < 1 {
		return tools.Errorf("code_action needs line: the 1-based line the action applies to")
	}
	end := a.EndLine
	if end == 0 {
		end = a.Line
	}
	if end < a.Line {
		return tools.Errorf("end_line %d is before line %d", end, a.Line)
	}
	workdir := workdirOf(t.m, env)
	abs := resolvePath(workdir, a.Path)
	var only []string
	if a.Only != "" {
		only = []string{a.Only}
	}
	where := fmt.Sprintf("%s lines %d-%d", t.m.RelPath(abs), a.Line, end)
	actions, err := t.m.Actions(ctx, abs, a.Line, end, only, a.Diagnostic)
	if err != nil {
		return missingServer(err)
	}
	if len(actions) == 0 {
		return tools.Result{Output: "The language server offers no code actions for " + where + ".", Summary: "none"}
	}
	if len(a.Choose) == 0 || string(a.Choose) == "null" {
		return tools.Result{Output: describeActions(actions) + "\nCall again with choose set to a number to apply one.", Summary: fmt.Sprintf("%d actions", len(actions))}
	}
	chosen, err := pickAction(actions, a.Choose)
	if err != nil {
		return tools.Result{Output: err.Error() + "\n" + describeActions(actions), IsError: true}
	}
	if err := applicable(chosen); err != nil {
		return tools.Errorf("%v", err)
	}
	apply := a.Apply == nil || *a.Apply
	changes, err := applyGuarded(ctx, t.m, env, *chosen.Edit, apply)
	if err != nil {
		return tools.Errorf("%q was not applied, and no file was changed: %v", chosen.Title, err)
	}
	return editResult(t.m, chosen.Title, changes, apply)
}

// editResult reports an applied or previewed edit.
func editResult(m *Manager, title string, changes []FileChange, applied bool) tools.Result {
	if len(changes) == 0 {
		return tools.Result{Output: fmt.Sprintf("%q changes nothing.", title), Summary: "no change"}
	}
	diff := diffSummary(changes, m.RelPath)
	files := make([]string, 0, len(changes))
	for _, c := range changes {
		files = append(files, m.RelPath(c.Path))
	}
	verb := "Applied"
	if !applied {
		verb = "Preview, not written (apply is false)"
	}
	out := fmt.Sprintf("%s: %s, changing %d file(s): %s", verb, title, len(files), strings.Join(files, ", "))
	if diff != "" {
		out += "\n\n" + diff
	}
	summary := fmt.Sprintf("%s in %d file(s)", strings.ToLower(verb), len(files))
	if applied {
		summary = fmt.Sprintf("applied in %d file(s)", len(files))
	}
	return tools.Result{Output: out, Summary: summary, Diff: diff, Files: files}
}

func workdirOf(m *Manager, env *tools.Env) string {
	if env != nil && env.Workdir != "" {
		return env.Workdir
	}
	return m.Workdir()
}

// --- refactor ---------------------------------------------------------------

// runFormat is the refactor tool's format and organize_imports. They share the
// format_file steps, so the result and its errors match format_file. Neither
// has a preview: formatFile writes the file as it goes.
func (t *refactorTool) runFormat(ctx context.Context, env *tools.Env, action, path string, start, end int, apply *bool) tools.Result {
	if apply != nil && !*apply {
		return tools.Errorf("%s writes the file directly and has no preview; leave apply out or set it to true", action)
	}
	if path == "" {
		return tools.Errorf("%s needs path", action)
	}
	ranged := start > 0 || end > 0
	if ranged && end == 0 {
		end = start
	}
	if action == "organize_imports" && ranged {
		return tools.Errorf("organize_imports works on the whole file; leave start_line and end_line out")
	}
	if ranged && (start < 1 || end < start) {
		return tools.Errorf("%s range needs 1 <= start_line <= end_line; got %d to %d", action, start, end)
	}
	abs := resolvePath(workdirOf(t.m, env), path)
	return formatFile(ctx, t.m, env, abs, formatOpts{
		organize:  action == "organize_imports",
		format:    action == "format",
		startLine: start,
		endLine:   end,
	})
}

// runFixImports is the refactor tool's fix_missing_imports. It applies the
// server's import fix through applyGuarded, so the file must be read first
// like any other edit.
func (t *refactorTool) runFixImports(ctx context.Context, env *tools.Env, path string, apply *bool) tools.Result {
	if path == "" {
		return tools.Errorf("fix_missing_imports needs path")
	}
	abs := resolvePath(workdirOf(t.m, env), path)
	rel := t.m.RelPath(abs)
	do := apply == nil || *apply
	title, changes, err := t.m.FixMissingImports(ctx, env, abs, do)
	if err != nil {
		if noLanguageServer(err) {
			lang := "this file type"
			if info, ok := languageForPath(abs); ok {
				lang = info.id
			}
			return tools.Errorf("fix_missing_imports is not supported for %s: no language server is set up for it, so no edit was made. Add the import by hand with edit_file.", lang)
		}
		return tools.Errorf("fix_missing_imports did not change %s: %v", rel, err)
	}
	if title == "" {
		return tools.Result{Output: fmt.Sprintf("The language server reports no missing names in %s, so no import was added and nothing was changed.", rel)}
	}
	return editResult(t.m, title+" ("+rel+")", changes, do)
}

// extractKinds are the server code-action kinds each refactor action uses.
// Kinds match by prefix (kindWanted), so refactor.inline also covers
// refactor.inline.call and friends.
var extractKinds = map[string][]string{
	"extract_function": {"refactor.extract.function"},
	"extract_variable": {"refactor.extract.variable", "refactor.extract.constant"},
	"extract_constant": {"refactor.extract.constant"},
	"inline":           {"refactor.inline"},
	"rewrite":          {"refactor.rewrite"},
}

// refactorActionNames are the action names the refactor tool accepts, in the
// order its schema lists them. format and organize_imports are not code
// actions: they run the same steps as format_file (see formatFile).
var refactorActionNames = []string{"extract_function", "extract_variable", "extract_constant", "inline", "rewrite", "format", "organize_imports", "fix_missing_imports"}

type refactorTool struct{ m *Manager }

func (t *refactorTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "refactor",
		Description: "Refactor with the language server. extract_function moves the given complete statements into a new " +
			"function; extract_variable moves a complete expression into a new variable; extract_constant does the same " +
			"for a constant; inline inlines the symbol or call at the lines given; rewrite applies the server's rewrite " +
			"at the lines (for example converting between equivalent forms). Give path, start_line and end_line " +
			"(end_line defaults to start_line). The server chooses any new name; rename it afterwards with rename_symbol " +
			"if the default does not fit. Set apply to false to preview. Read the file first. Languages whose server " +
			"does not offer the refactoring get an error and no edit. format formats the whole file with the " +
			"language server's formatter; give start_line and end_line to format only those lines. organize_imports " +
			"sorts and removes unused imports. format and organize_imports write the file directly, so they take no apply " +
			"preview. fix_missing_imports adds the imports the server reports as missing for undefined names, using the " +
			"server's own import fix (gopls organizes imports, which also drops unused ones; TypeScript adds only the " +
			"missing ones). It never guesses a module path: when several modules could supply the name it changes nothing " +
			"and lists the candidates. Only path is needed; apply false previews it.",
		Schema: tools.Schema(map[string]any{
			"action":     tools.Enum("The refactoring to run.", refactorActionNames...),
			"path":       tools.Str("File, relative to the project root."),
			"start_line": tools.Int("First line (1-based) of the code to extract, or of the lines to format."),
			"end_line":   tools.Int("Last line (1-based, inclusive). Defaults to start_line."),
			"apply":      tools.Bool("Apply the edit (default true). false previews the diff only. Not used by format or organize_imports."),
		}, "action", "path"),
	}
}

func (t *refactorTool) ReadOnly() bool { return false }

func (t *refactorTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var a struct {
		Action    string `json:"action"`
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
		Apply     *bool  `json:"apply"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	action := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(a.Action)), "-", "_")
	if action == "format" || action == "organize_imports" {
		return t.runFormat(ctx, env, action, a.Path, a.StartLine, a.EndLine, a.Apply)
	}
	if action == "fix_missing_imports" {
		return t.runFixImports(ctx, env, a.Path, a.Apply)
	}
	kinds, ok := extractKinds[action]
	if !ok {
		action = "extract_" + action
		kinds, ok = extractKinds[action]
	}
	if !ok {
		return tools.Errorf("refactor action must be one of %s; got %q", strings.Join(refactorActionNames, ", "), a.Action)
	}
	if a.Path == "" {
		return tools.Errorf("%s needs path", action)
	}
	if a.StartLine < 1 {
		return tools.Errorf("%s needs start_line: the first line (1-based) of the code to extract", action)
	}
	end := a.EndLine
	if end == 0 {
		end = a.StartLine
	}
	if end < a.StartLine {
		return tools.Errorf("end_line %d is before start_line %d", end, a.StartLine)
	}
	abs := resolvePath(workdirOf(t.m, env), a.Path)
	rel := t.m.RelPath(abs)
	lang := "this file type"
	if info, ok := languageForPath(abs); ok {
		lang = info.id
	}
	where := fmt.Sprintf("%s lines %d-%d", rel, a.StartLine, end)
	actions, err := t.m.Actions(ctx, abs, a.StartLine, end, kinds, "")
	if err != nil {
		if noLanguageServer(err) {
			return tools.Errorf("%s is not supported for %s: no language server is set up for it, so no edit was made. Extract the code by hand with edit_file.", action, lang)
		}
		return missingServer(err)
	}
	var chosen *CodeAction
	for i := range actions {
		if kindWanted(actions[i].Kind, kinds) && actions[i].Edit != nil && actions[i].Disabled == "" {
			chosen = &actions[i]
			break
		}
	}
	if chosen == nil {
		return tools.Errorf("%s is not supported for %s at %s: the language server offers no %s there, so no edit was made. extract_function needs complete statements; extract_variable and extract_constant need one complete expression; inline needs a symbol or call on those lines. Check the lines, or make the change by hand with edit_file.", action, lang, where, action)
	}
	apply := a.Apply == nil || *a.Apply
	changes, err := applyGuarded(ctx, t.m, env, *chosen.Edit, apply)
	if err != nil {
		return tools.Errorf("%s was not applied, and no file was changed: %v", action, err)
	}
	return editResult(t.m, chosen.Title+" ("+where+")", changes, apply)
}
