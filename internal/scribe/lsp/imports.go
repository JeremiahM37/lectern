package lsp

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// missingNameRE matches the diagnostics servers give for a name that is used
// but not declared or imported. Only these lead to an import fix, so a file
// with no such error is left alone.
var missingNameRE = regexp.MustCompile(`(?i)undefined: |undeclared name|cannot find name|cannot find (value|type|struct|trait|function|macro)\b|is not defined`)

// importKinds are the source actions that add missing imports, most
// preferred first. A source action applies every missing import in one edit,
// so it is taken without a choice. gopls's organize-imports action also adds
// missing imports (goimports semantics) and removes unused ones;
// typescript-language-server has addMissingImports, which only adds.
var importKinds = []string{"source.addMissingImports", "source.organizeImports"}

// FixMissingImports asks the server to add the imports that abs is missing and
// applies the edit it returns, unless apply is false, which previews it. The
// module paths come from the server only; nothing here guesses one. It
// returns the title of the fix chosen and the file changes. The title is
// empty when the server reports no missing name in abs.
func (m *Manager) FixMissingImports(ctx context.Context, env *tools.Env, abs string, apply bool) (string, []FileChange, error) {
	all, err := m.Diagnostics(ctx, abs)
	if err != nil {
		return "", nil, err
	}
	var missing []FileDiagnostic
	for _, d := range all {
		if d.Severity == "error" && missingNameRE.MatchString(d.Message) {
			missing = append(missing, d)
		}
	}
	if len(missing) == 0 {
		return "", nil, nil
	}
	lines, err := fileLineCount(abs)
	if err != nil {
		return "", nil, err
	}
	actions, err := m.actionsWith(ctx, abs, 1, lines, append(append([]string{}, importKinds...), "quickfix"), missing)
	if err != nil {
		return "", nil, err
	}
	chosen, err := chooseImportFix(actions)
	if err != nil {
		return "", nil, err
	}
	changes, err := applyGuarded(ctx, m, env, *chosen.Edit, apply)
	if err != nil {
		return "", nil, err
	}
	return chosen.Title, changes, nil
}

// chooseImportFix picks the one fix to apply from the server's actions. A source
// action is used when it is the only one of its kind. A quick fix is used only
// when it is the single import candidate: when several modules could supply
// the name, picking one would be a guess, so the caller is told to choose.
func chooseImportFix(actions []CodeAction) (CodeAction, error) {
	var usable []CodeAction
	for _, a := range actions {
		if a.Edit != nil && a.Disabled == "" {
			usable = append(usable, a)
		}
	}
	for _, kind := range importKinds {
		var tier []CodeAction
		for _, a := range usable {
			if hasKind(a.Kind, kind) {
				tier = append(tier, a)
			}
		}
		switch len(tier) {
		case 0:
			continue
		case 1:
			return tier[0], nil
		default:
			return CodeAction{}, fmt.Errorf("the server offers %d fixes of kind %s (%s); choose one with code_action", len(tier), kind, titlesOf(tier))
		}
	}
	var quick []CodeAction
	for _, a := range usable {
		if hasKind(a.Kind, "quickfix") && isImportTitle(a.Title) {
			quick = append(quick, a)
		}
	}
	switch len(quick) {
	case 0:
		return CodeAction{}, fmt.Errorf("the language server offers no import fix for the missing names")
	case 1:
		return quick[0], nil
	}
	return CodeAction{}, fmt.Errorf("the server offers %d import candidates (%s) and does not say which one is meant; choose one with code_action", len(quick), titlesOf(quick))
}

// hasKind reports whether kind is want or one of its sub-kinds. An empty kind
// matches nothing; kindWanted would accept it, which is too loose here.
func hasKind(kind, want string) bool {
	return kind != "" && (kind == want || strings.HasPrefix(kind, want+"."))
}

// isImportTitle reports whether a quick fix adds an import. Fixes that remove
// one are excluded.
func isImportTitle(title string) bool {
	t := strings.ToLower(title)
	return strings.Contains(t, "import") && !strings.Contains(t, "remove") && !strings.Contains(t, "unused")
}

func titlesOf(actions []CodeAction) string {
	titles := make([]string, 0, len(actions))
	for _, a := range actions {
		titles = append(titles, a.Title)
	}
	return strings.Join(titles, "; ")
}

// fileLineCount is the number of lines Actions can address in abs. A final
// newline ends the last line rather than starting another.
func fileLineCount(abs string) (int, error) {
	b, err := os.ReadFile(abs)
	if err != nil {
		return 0, err
	}
	text := string(b)
	n := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		n++
	}
	if n < 1 {
		n = 1
	}
	return n, nil
}
