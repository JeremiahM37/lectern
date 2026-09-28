package helpers

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// claude-trust and codex-trust are sessions' claudeTrust and codexTrust (and
// the legacy claudeTrust they replaced): they record that the operator trusts
// a directory, so the CLI does not ask on its first start there.
func init() {
	Register("claude-trust", claudeTrust)
	Register("codex-trust", codexTrust)
}

// claudeTrust sets projects[DIR].hasTrustDialogAccepted in ~/.claude.json
// ($CLAUDE_CONFIG_DIR/.claude.json), rewriting the file through a temp file
// and rename only when the flag is missing.
func claudeTrust(args []string, _ io.Reader, _, stderr io.Writer) int {
	fail := func(err any) int {
		fmt.Fprintln(stderr, "claude-trust:", err)
		return 1
	}
	path := pyExpanduser("~/.claude.json")
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		path = pyJoin(pyExpanduser(dir), ".claude.json")
	}
	if err := pyMakedirs(pyDirname(path), 0o700); err != nil {
		return fail(err)
	}
	var doc any = newPyDict()
	if raw, err := os.ReadFile(path); err == nil {
		if v, err := pyJSONLoadBytes(raw); err == nil {
			doc = v
		}
	}
	if len(args) < 1 {
		return fail("usage: claude-trust DIR")
	}
	root, ok := doc.(*pyDict)
	if !ok {
		return fail("the config is not a JSON object")
	}
	projects, present := root.get("projects")
	if !present {
		projects = newPyDict()
		root.set("projects", projects)
	}
	projectsDict, ok := projects.(*pyDict)
	if !ok {
		return fail("projects is not an object")
	}
	entry, present := projectsDict.get(args[0])
	if !present {
		entry = newPyDict()
		projectsDict.set(args[0], entry)
	}
	entryDict, ok := entry.(*pyDict)
	if !ok {
		return fail("the project entry is not an object")
	}
	if v, _ := entryDict.get("hasTrustDialogAccepted"); v == true {
		return 0
	}
	entryDict.set("hasTrustDialogAccepted", true)
	text, err := pyJSONDumps(doc, 2, true)
	if err != nil {
		return fail(err)
	}
	dir := pyDirname(path)
	if dir == "" {
		dir = "."
	}
	if err := pyReplaceFile(dir, path, []byte(text)); err != nil {
		return fail(err)
	}
	return 0
}

// codexTrust appends a [projects."DIR"] trust table to ~/.codex/config.toml
// ($CODEX_HOME/config.toml) under an exclusive lock, unless the parsed file
// already names DIR. The file is never rewritten, so the operator's comments
// and layout survive.
func codexTrust(args []string, _ io.Reader, _, stderr io.Writer) int {
	fail := func(err any) int {
		fmt.Fprintln(stderr, "codex-trust:", err)
		return 1
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		home = "~/.codex"
	}
	root := pyExpanduser(home)
	if err := pyMakedirs(root, 0o700); err != nil {
		return fail(err)
	}
	f, err := os.OpenFile(pyJoin(root, "config.toml"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fail(err)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return fail(err)
	}
	if !utf8.Valid(raw) {
		return fail("config.toml is not UTF-8")
	}
	if len(args) < 1 {
		return fail("usage: codex-trust DIR")
	}
	dir := args[0]
	if text := pyUniversalNewlines(string(raw)); pyStrip(text) != "" {
		doc, err := tomlLoads(text)
		if err != nil {
			return fail(err)
		}
		projects, ok := doc["projects"]
		if !ok {
			projects = map[string]any{}
		}
		switch p := projects.(type) {
		case map[string]any:
			if _, ok := p[dir]; ok {
				return 0
			}
		case []any:
			for _, item := range p {
				if s, ok := item.(string); ok && s == dir {
					return 0
				}
			}
		case string:
			if strings.Contains(p, dir) {
				return 0
			}
		default:
			return fail("projects is not a table") // `in` raises TypeError
		}
	}
	if !utf8.ValidString(dir) {
		return fail("the directory is not UTF-8")
	}
	quoted, err := pyJSONDumps(dir, -1, false)
	if err != nil {
		return fail(err)
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fail(err)
	}
	if _, err := f.WriteString("\n[projects." + quoted + "]\ntrust_level = \"trusted\"\n"); err != nil {
		return fail(err)
	}
	return 0
}
