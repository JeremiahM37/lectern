// Package workflows describes the optional, bundled project workflows.
//
// The source files are compiled into Lectern and copied to the target only
// when an operator enables a workflow. The target copy is versioned and
// immutable: a later enable verifies existing bytes instead of replacing them.
package workflows

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/shellq"
)

// bundled contains the pinned wrapper and upstream support files. The all:
// prefix is intentional: upstream repositories can contain files beginning
// with '.' or '_', and those files are part of the pinned source as well.
//
//go:embed all:bundled
var bundled embed.FS

type Definition struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	UpstreamURL string   `json:"upstream_url"`
	Commands    []string `json:"commands"`
}

type File struct {
	Path []string
	Data []byte
	Mode uint32
}

// These are deliberately a closed list. New installations start disabled and
// cannot be enabled by inventing an arbitrary source path or workflow id.
var definitions = map[string]Definition{
	"spec-kit": {
		ID:          "spec-kit",
		Name:        "Spec Kit",
		Description: "Specification-first development with constitution, planning, tasks, and implementation workflows.",
		Version:     "d848fb4e18f44640ad6b42e60a280551ee90cdce",
		UpstreamURL: "https://github.com/github/spec-kit",
		Commands: []string{
			"lectern-spec-kit constitution",
			"lectern-spec-kit specify",
			"lectern-spec-kit clarify",
			"lectern-spec-kit plan",
			"lectern-spec-kit tasks",
			"lectern-spec-kit analyze",
			"lectern-spec-kit checklist",
			"lectern-spec-kit implement",
			"lectern-spec-kit converge",
		},
	},
	"maestro": {
		ID:          "maestro",
		Name:        "Maestro",
		Description: "Curated agent workflow guidance for diagnosing, fortifying, refining, reflecting, and teaching Maestro.",
		Version:     "00f9115d446a8ba26b8f18f6ed306bc4a21807c3",
		UpstreamURL: "https://github.com/sharpdeveye/maestro",
		Commands: []string{
			"lectern-maestro diagnose",
			"lectern-maestro fortify",
			"lectern-maestro refine",
			"lectern-maestro reflect",
			"lectern-maestro agent-workflow",
			"lectern-maestro teach-maestro",
		},
	},
}

func DefinitionFor(id string) (Definition, bool) {
	d, ok := definitions[id]
	if !ok {
		return Definition{}, false
	}
	d.Commands = append([]string(nil), d.Commands...)
	return d, true
}

func Definitions() []Definition {
	out := make([]Definition, 0, len(definitions))
	for _, d := range definitions {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	for i := range out {
		out[i].Commands = append([]string(nil), out[i].Commands...)
	}
	return out
}

func Files(id string) ([]File, error) {
	if _, ok := DefinitionFor(id); !ok {
		return nil, fmt.Errorf("unknown workflow %q", id)
	}
	root := filepath.ToSlash(filepath.Join("bundled", id))
	var out []File
	err := fs.WalkDir(bundled, root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("bundled workflow file %q is not regular", path)
		}
		b, err := fs.ReadFile(bundled, path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("bundled workflow path escapes source: %q", path)
		}
		filePath := strings.Split(filepath.ToSlash(rel), "/")
		mode := uint32(0o644)
		// Embedded files do not reliably retain executable bits across all Go
		// toolchains. Workflow helpers are invoked as programs by users, so make
		// the executable intent explicit for the script formats we ship.
		if strings.HasSuffix(path, ".py") || strings.HasSuffix(path, ".sh") {
			mode = 0o755
		}
		out = append(out, File{Path: filePath, Data: b, Mode: mode})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i].Path, "/") < strings.Join(out[j].Path, "/") })
	if len(out) == 0 {
		return nil, fmt.Errorf("workflow %q has no bundled files", id)
	}
	seenSkill := false
	for _, f := range out {
		if strings.Join(f.Path, "/") == "SKILL.md" {
			seenSkill = true
		}
	}
	if !seenSkill {
		return nil, fmt.Errorf("workflow %q is missing bundled SKILL.md", id)
	}
	return out, nil
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

// Stage copies a bundled source to a target-local immutable version path. It
// uses only Executor.Run, so SSH targets execute the same checks remotely and
// bytes never land in the Lectern control-plane filesystem.
func Stage(ctx context.Context, ex executor.Executor, id string) (string, string, error) {
	d, ok := DefinitionFor(id)
	if !ok {
		return "", "", fmt.Errorf("unknown workflow %q", id)
	}
	files, err := Files(id)
	if err != nil {
		return "", "", err
	}
	homeResult, err := ex.Run(ctx, `printf '%s' "$HOME"`, executor.RunOpts{Timeout: 20})
	if err != nil {
		return "", "", err
	}
	if !homeResult.OK() {
		return "", "", fmt.Errorf("target home lookup failed: %s", strings.TrimSpace(homeResult.Stderr))
	}
	home := strings.TrimSpace(homeResult.Stdout)
	if filepath.IsAbs(home) == false || home == "." || strings.ContainsAny(home, "\r\n") {
		return "", "", fmt.Errorf("target returned an unsafe home path")
	}
	digest := SourceDigest(files)
	root := filepath.Join(home, ".lectern", "workflows", id, d.Version+"-"+digest[:16])
	for _, f := range files {
		if err := stageFile(ctx, ex, root, f.Path, f.Data, f.Mode); err != nil {
			return "", "", fmt.Errorf("stage workflow %s/%s: %w", id, strings.Join(f.Path, "/"), err)
		}
	}
	return root, SourceDigest(files), nil
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
	cmd := "python3 -c " + shellq.Quote(stageScript) + " " + shellq.Quote(string(b))
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil {
		return err
	}
	if !r.OK() {
		return fmt.Errorf("target command failed: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}
