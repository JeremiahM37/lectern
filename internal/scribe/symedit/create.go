package symedit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
)

// createFile writes a new source file whose whole content is new_code. The
// file must not exist, must be a language ast_edit parses, and the content
// must pass the same validation an edit does. It is the one ast_edit path that
// creates a file; plain text files still go through write_file.
func createFile(ctx context.Context, env *tools.Env, a args, abs, rel string) tools.Result {
	if _, err := os.Stat(abs); err == nil {
		return tools.Errorf("%s already exists. create_file only makes new files: edit it with ast_edit, or write_file to replace it.", rel)
	}
	lang, ok := languages[strings.ToLower(filepath.Ext(abs))]
	if !ok {
		return tools.Errorf("%s: create_file needs a source file ast_edit parses. Use write_file for %q.", toolName, rel)
	}
	content := toLF(a.NewCode)
	if strings.TrimSpace(content) == "" {
		return tools.Errorf("new_code is required for create_file: the full content of %s.", rel)
	}
	ed := &editor{lang: lang, rel: rel, base: filepath.Base(abs)}
	defer ed.close()
	final, err := validate(ed, "", content)
	if err != nil {
		return tools.Errorf("%s: %v No file was created.", rel, err)
	}
	if apply := a.Apply == nil || *a.Apply; !apply {
		diff, added, _ := fs.UnifiedDiff(rel, "", final)
		return tools.Result{
			Output:  fmt.Sprintf("Preview, not written (apply is false): create %s. Pass apply: true, or leave it out, to write it.", rel),
			Summary: fmt.Sprintf("preview +%d in %s", added, rel),
			Diff:    diff,
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return tools.Errorf("create directories for %s: %v", rel, err)
	}
	if env.Files != nil {
		env.Files.BeforeWrite(abs)
	}
	if err := os.WriteFile(abs, []byte(final), 0o644); err != nil {
		return tools.Errorf("write %s: %v", rel, err)
	}
	if env.Files != nil {
		env.Files.MarkRead(abs)
	}
	diff, added, _ := fs.UnifiedDiff(rel, "", final)
	return tools.Result{
		Output:  fmt.Sprintf("Created %s (%d lines).", rel, strings.Count(final, "\n")+1),
		Summary: fmt.Sprintf("new file, +%d in %s", added, rel),
		Diff:    diff,
		Files:   []string{rel},
	}
}
