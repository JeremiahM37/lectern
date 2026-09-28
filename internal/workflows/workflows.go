// Package workflows stages project workflows and plugin skills onto targets.
//
// Workflows are plugin contributions: the bundled ones (Spec Kit, Maestro,
// Delegated build) come from the binary's own plugins in internal/pluginpkg,
// installed plugins add more through internal/plugins. The source files are
// copied to the target only when an operator enables a workflow. The target copy is versioned and
// immutable: a later enable verifies existing bytes instead of replacing them.
package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/helpers"
	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

type Definition struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	UpstreamURL string   `json:"upstream_url"`
	Commands    []string `json:"commands"`
	// Kind is "workflow", or "skill" for a plugin skill: a workflow with no
	// commands, enabled the same way.
	Kind string `json:"kind,omitempty"`
	// PluginID is the plugin that contributes it; Entry is the skill
	// directory it becomes in the project.
	PluginID string `json:"plugin_id,omitempty"`
	Entry    string `json:"entry,omitempty"`
	// Bundled marks the binary's own workflows, whose ids and attachment
	// records predate plugins and are kept exactly.
	Bundled bool `json:"bundled,omitempty"`
}

type File struct {
	Path []string
	Data []byte
	Mode uint32
}

// Source is a workflow with the files it stages.
type Source struct {
	Definition
	Files []File
}

// FromPlugin lists a plugin's workflows and skills as sources. A bundled
// plugin's workflows keep their bare ids (spec-kit, maestro, delegate) so
// existing project attachments still match; any other plugin's are
// namespaced "<plugin id>:<id>".
func FromPlugin(pkg *pluginpkg.Package) ([]Source, error) {
	m := pkg.Manifest
	bundled := m.Bundled()
	id := func(local string) string {
		if bundled {
			return local
		}
		return m.ID + ":" + local
	}
	var out []Source
	add := func(d Definition, dir string) error {
		files, err := filesUnder(pkg.Files, dir)
		if err != nil {
			return fmt.Errorf("%s %s: %w", d.Kind, d.ID, err)
		}
		out = append(out, Source{Definition: d, Files: files})
		return nil
	}
	for _, w := range m.Contributes.Workflows {
		d := Definition{ID: id(w.ID), Name: w.Name, Description: w.Description, Version: w.Version,
			UpstreamURL: w.UpstreamURL, Commands: append([]string{}, w.Commands...), Kind: "workflow",
			PluginID: m.ID, Entry: m.EntryFor(w.ID, w.Entry), Bundled: bundled}
		if d.Version == "" {
			d.Version = m.Version
		}
		if d.UpstreamURL == "" {
			d.UpstreamURL = m.Homepage
		}
		if err := add(d, w.Path); err != nil {
			return nil, err
		}
	}
	for _, s := range m.Contributes.Skills {
		name := s.Name
		if name == "" {
			name = s.ID
		}
		d := Definition{ID: id(s.ID), Name: name, Description: s.Description, Version: m.Version,
			UpstreamURL: m.Homepage, Commands: []string{}, Kind: "skill", PluginID: m.ID,
			Entry: m.EntryFor(s.ID, s.Entry), Bundled: bundled}
		if err := add(d, s.Path); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func filesUnder(all []pluginpkg.File, dir string) ([]File, error) {
	var out []File
	for _, f := range pluginpkg.SubFiles(all, dir) {
		out = append(out, File{Path: strings.Split(f.Path, "/"), Data: f.Data, Mode: f.Mode})
	}
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i].Path, "/") < strings.Join(out[j].Path, "/") })
	if len(out) == 0 {
		return nil, fmt.Errorf("no files under %s", dir)
	}
	for _, f := range out {
		if strings.Join(f.Path, "/") == "SKILL.md" {
			return out, nil
		}
	}
	return nil, fmt.Errorf("missing SKILL.md")
}

// bundledSources are the workflows the binary's own plugins contribute.
func bundledSources() []Source {
	pkgs, err := pluginpkg.Bundled()
	if err != nil {
		return nil
	}
	var out []Source
	for _, pkg := range pkgs {
		srcs, err := FromPlugin(pkg)
		if err != nil {
			continue
		}
		out = append(out, srcs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func DefinitionFor(id string) (Definition, bool) {
	for _, s := range bundledSources() {
		if s.ID == id {
			d := s.Definition
			d.Commands = append([]string(nil), d.Commands...)
			return d, true
		}
	}
	return Definition{}, false
}

// Definitions lists the bundled workflows. Installed plugins add theirs
// through internal/plugins.
func Definitions() []Definition {
	var out []Definition
	for _, s := range bundledSources() {
		d := s.Definition
		d.Commands = append([]string(nil), d.Commands...)
		out = append(out, d)
	}
	return out
}

func Files(id string) ([]File, error) {
	for _, s := range bundledSources() {
		if s.ID == id {
			return s.Files, nil
		}
	}
	return nil, fmt.Errorf("unknown workflow %q", id)
}

func SourceDigest(files []File) string {
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(strings.Join(f.Path, "/")))
		h.Write([]byte{0})
		fmt.Fprintf(h, "%o\x00", f.Mode)
		h.Write(f.Data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Stage copies a bundled workflow to a target-local immutable version path.
func Stage(ctx context.Context, ex executor.Executor, id string) (string, string, error) {
	d, ok := DefinitionFor(id)
	if !ok {
		return "", "", fmt.Errorf("unknown workflow %q", id)
	}
	files, err := Files(id)
	if err != nil {
		return "", "", err
	}
	return StageSource(ctx, ex, Source{Definition: d, Files: files})
}

// StageSource copies a workflow or skill to a target-local immutable version
// path, ~/.lectern/workflows/<id>/<version>-<digest>. It uses only
// Executor.Run, so SSH targets execute the same checks remotely and bytes
// never land in the Lectern control-plane filesystem.
func StageSource(ctx context.Context, ex executor.Executor, src Source) (string, string, error) {
	home, err := targetHome(ctx, ex)
	if err != nil {
		return "", "", err
	}
	digest := SourceDigest(src.Files)
	root := filepath.Join(home, ".lectern", "workflows", src.ID, src.Version+"-"+digest[:16])
	if err := StageFiles(ctx, ex, root, src.Files); err != nil {
		return "", "", fmt.Errorf("stage workflow %s: %w", src.ID, err)
	}
	return root, digest, nil
}

// StageFiles writes files under root on the target, verifying rather than
// replacing anything already there.
func StageFiles(ctx context.Context, ex executor.Executor, root string, files []File) error {
	for _, f := range files {
		if err := stageFile(ctx, ex, root, f.Path, f.Data, f.Mode); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(f.Path, "/"), err)
		}
	}
	return nil
}

func targetHome(ctx context.Context, ex executor.Executor) (string, error) {
	homeResult, err := ex.Run(ctx, `printf '%s' "$HOME"`, executor.RunOpts{Timeout: 20})
	if err != nil {
		return "", err
	}
	if !homeResult.OK() {
		return "", fmt.Errorf("target home lookup failed: %s", strings.TrimSpace(homeResult.Stderr))
	}
	home := strings.TrimSpace(homeResult.Stdout)
	if !filepath.IsAbs(home) || home == "." || strings.ContainsAny(home, "\r\n") {
		return "", fmt.Errorf("target returned an unsafe home path")
	}
	return home, nil
}

// TargetHome is the target user's $HOME, checked to be a plain absolute path.
func TargetHome(ctx context.Context, ex executor.Executor) (string, error) {
	return targetHome(ctx, ex)
}

const stageScript = `
import base64,json,os,stat,tempfile
def ensure_dir(path):
    path=os.path.abspath(path)
    fd=os.open('/',os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    try:
        for part in path.strip('/').split('/'):
            if not part: continue
            try: nfd=os.open(part,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd)
            except FileNotFoundError:
                try: os.mkdir(part,0o755,dir_fd=fd)
                except FileExistsError: pass
                nfd=os.open(part,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd)
            os.close(fd); fd=nfd
        return fd
    except:
        os.close(fd); raise
def main(a):
    root=os.path.abspath(a['root']); parts=a['parts']; data=base64.b64decode(a['data'],validate=True)
    if not parts or any(not isinstance(x,str) or x in ('','.','..') or '/' in x or '\\' in x for x in parts): raise OSError('unsafe bundled path')
    parent=ensure_dir(os.path.join(root,*parts[:-1]))
    name=parts[-1]
    try: st=os.lstat(name,dir_fd=parent)
    except FileNotFoundError: st=None
    mode=int(a.get('mode',0o644))
    def same_existing():
        try: current=os.lstat(name,dir_fd=parent)
        except FileNotFoundError: return False
        if not stat.S_ISREG(current.st_mode) or stat.S_ISLNK(current.st_mode): raise OSError('immutable source path is not a regular file')
        fd=os.open(name,os.O_RDONLY|os.O_NOFOLLOW,dir_fd=parent)
        try: old=os.read(fd,current.st_size+1)
        finally: os.close(fd)
        if old!=data or stat.S_IMODE(current.st_mode)!=mode: raise OSError('immutable workflow source differs from pinned content')
        return True
    if st:
        same_existing()
        return {'existing':True}
    tmp='.lectern-stage-'+next(tempfile._get_candidate_names())
    fd=os.open(tmp,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,mode,dir_fd=parent)
    try:
        view=memoryview(data)
        while view:
            n=os.write(fd,view); view=view[n:]
        os.fsync(fd)
        # os.open honors the target umask; immutable verification uses the
        # pinned mode, so set it explicitly after creation.
        os.fchmod(fd,mode)
    finally: os.close(fd)
    try:
        # link(2) gives us no-clobber publication. rename would replace a
        # foreign file created after the initial lstat race check.
        os.link(tmp,name,src_dir_fd=parent,dst_dir_fd=parent,follow_symlinks=False)
    except FileExistsError:
        os.unlink(tmp,dir_fd=parent)
        same_existing()
        return {'existing':True}
    os.unlink(tmp,dir_fd=parent)
    return {'created':True}
print(json.dumps(main(json.loads(__import__('sys').argv[1])),separators=(',',':')))
`

func stageFile(ctx context.Context, ex executor.Executor, root string, parts []string, data []byte, mode uint32) error {
	b, _ := json.Marshal(map[string]any{"root": root, "parts": parts, "data": base64.StdEncoding.EncodeToString(data), "mode": mode})
	cmd := helpers.Command(ex, "workflow-stage", []string{string(b)}, "python3 -c "+shellq.Quote(stageScript)+" "+shellq.Quote(string(b)))
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil {
		return err
	}
	if !r.OK() {
		return fmt.Errorf("target command failed: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}
