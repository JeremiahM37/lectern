package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const (
	maxListEntries = 500
	maxGlobResults = 500
	maxGlobScan    = 100000
)

// alwaysSkip are never descended into, whatever .gitignore says.
func alwaysSkip(name string) bool {
	return name == ".git" || name == "node_modules"
}

// loadIgnore extends ig with absDir/.gitignore, if there is one. relBase is
// absDir relative to the workdir.
func loadIgnore(ig *Ignore, relBase, absDir string) *Ignore {
	data, err := os.ReadFile(filepath.Join(absDir, ".gitignore"))
	if err != nil {
		return ig
	}
	return ig.Extend(relBase, data)
}

// chainIgnore builds the rules that apply to dir: the .gitignore files from
// the workdir down to dir, inclusive. Dirs outside the workdir get no rules.
func chainIgnore(wd, dir string) *Ignore {
	rel, err := filepath.Rel(wd, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	ig := loadIgnore(nil, "", wd)
	if rel == "." {
		return ig
	}
	cur := ""
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if cur == "" {
			cur = part
		} else {
			cur += "/" + part
		}
		ig = loadIgnore(ig, cur, filepath.Join(wd, filepath.FromSlash(cur)))
	}
	return ig
}

// walkDir visits the entries of dir, directories before their contents, in
// name order. level is 1 for dir's children. A directory is descended into
// while level < maxLevel, or without limit when maxLevel is negative. visit
// returns false to stop. walkDir returns false when it was stopped.
// Symlinks are reported but not followed.
func walkDir(ctx context.Context, wd, dir string, ig *Ignore, level, maxLevel int,
	visit func(abs, rel string, fi fs.FileInfo) bool) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, de := range entries {
		if ctx.Err() != nil {
			return false
		}
		name := de.Name()
		if alwaysSkip(name) {
			continue
		}
		abs := filepath.Join(dir, name)
		rel := relSlash(wd, abs)
		isDir := de.IsDir()
		if ig.Ignored(rel, isDir) {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		if !visit(abs, rel, fi) {
			return false
		}
		if isDir && (maxLevel < 0 || level < maxLevel) {
			if !walkDir(ctx, wd, abs, loadIgnore(ig, rel, abs), level+1, maxLevel, visit) {
				return false
			}
		}
	}
	return true
}

// treeWriter renders list_dir output.
type treeWriter struct {
	ctx    context.Context
	wd     string
	sb     strings.Builder
	count  int
	limit  int // entries this writer may show; the caller shares one budget
	capped bool
}

// renderTree lists root to depth levels (1 = its children). Directories come
// first, then files, each sorted by name. Files show their size.
func renderTree(ctx context.Context, wd, root string, depth int) (string, int, bool) {
	return renderTreeLimit(ctx, wd, root, depth, maxListEntries)
}

// renderTreeLimit is renderTree with an entry budget of limit.
func renderTreeLimit(ctx context.Context, wd, root string, depth, limit int) (string, int, bool) {
	w := &treeWriter{ctx: ctx, wd: wd, limit: limit}
	w.sb.WriteString(relSlash(wd, root) + "/\n")
	w.dir(root, chainIgnore(wd, root), "  ", 1, depth)
	return strings.TrimRight(w.sb.String(), "\n"), w.count, w.capped
}

func (w *treeWriter) dir(abs string, ig *Ignore, indent string, level, depth int) {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return
	}
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	for _, e := range entries {
		if w.ctx.Err() != nil {
			return
		}
		name := e.Name()
		if alwaysSkip(name) {
			continue
		}
		isDir := e.IsDir()
		childAbs := filepath.Join(abs, name)
		rel := relSlash(w.wd, childAbs)
		if ig.Ignored(rel, isDir) {
			continue
		}
		if w.count >= w.limit {
			w.capped = true
			return
		}
		w.count++
		line := indent + name
		if isDir {
			line += "/"
		} else if fi, err := e.Info(); err == nil {
			line += "  " + humanSize(fi.Size())
		}
		w.sb.WriteString(line + "\n")
		if isDir && level < depth {
			w.dir(childAbs, loadIgnore(ig, rel, childAbs), indent+"  ", level+1, depth)
		}
	}
}

// NewListDir returns list_dir.
func NewListDir() tools.Tool {
	return &tool{
		spec: newSpec("list_dir",
			"List a directory: subdirectories first, then files with sizes. Respects .gitignore. Use depth to see nested levels, "+
				"and paths to list several directories in one call.",
			tools.Schema(map[string]any{
				"path":  tools.Str("Directory to list, relative to the project root. Default: the project root."),
				"paths": tools.Arr("Several directories to list in one call. Each is shown under its own header; the entry cap is shared.", map[string]any{"type": "string"}),
				"depth": tools.Int("How many levels to show, 1 to 5. Default 1."),
			})),
		readOnly: true,
		run: func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
			var a struct {
				Path  string      `json:"path"`
				Paths flexStrings `json:"paths"`
				Depth flexInt     `json:"depth"`
			}
			if err := tools.Decode(args, &a); err != nil {
				return failTagged(err)
			}
			depth := min(max(int(a.Depth), 1), maxListDepth)
			var dirs []string
			if a.Path != "" {
				dirs = append(dirs, a.Path)
			}
			dirs = append(dirs, a.Paths...)
			if len(dirs) == 0 {
				dirs = []string{"."}
			}
			if len(dirs) == 1 {
				return listOne(ctx, env, dirs[0], depth, maxListEntries)
			}

			// Several directories share one entry budget, spent in order.
			var sb strings.Builder
			budget, total, failures := maxListEntries, 0, 0
			for _, d := range dirs {
				sb.WriteString("── " + d + " ──\n")
				if budget <= 0 {
					sb.WriteString("(entry cap reached; list this directory on its own)\n\n")
					continue
				}
				res := listOne(ctx, env, d, depth, budget)
				if res.IsError {
					failures++
					sb.WriteString("error: " + res.Output + "\n\n")
					continue
				}
				n := entriesOf(res)
				budget -= n
				total += n
				sb.WriteString(res.Output + "\n\n")
			}
			return tools.Result{
				Output:  strings.TrimRight(sb.String(), "\n"),
				IsError: failures == len(dirs),
				Summary: fmt.Sprintf("%d entries in %d directories", total, len(dirs)-failures),
			}
		},
	}
}

// maxListDepth is the deepest list_dir may recurse.
const maxListDepth = 5

// listOne lists one directory with an entry budget of limit. The result
// carries the entry count in Meta["entries"] so a caller can spend the rest.
func listOne(ctx context.Context, env *tools.Env, p string, depth, limit int) tools.Result {
	t, err := resolve(ctx, env, "list_dir", p, accessSearch)
	if err != nil {
		return failTagged(err)
	}
	fi, err := statPath(t)
	if err != nil {
		return failTagged(err)
	}
	if !fi.IsDir() {
		return tools.Errorf("%s is not a directory. Use read to view a file.", t.rel)
	}
	out, n, capped := renderTreeLimit(ctx, env.Workdir, t.abs, depth, limit)
	if capped {
		out += fmt.Sprintf("\n(Output capped at %d entries. Narrow the path or reduce depth.)", limit)
	}
	return tools.Result{Output: out, Summary: fmt.Sprintf("%d entries", n), Meta: map[string]any{"entries": n}}
}

// entriesOf reads the entry count listOne recorded.
func entriesOf(res tools.Result) int {
	n, _ := res.Meta["entries"].(int)
	return n
}

// globMatch tests a path relative to the search base. A pattern with no
// slash matches the base name at any depth, as in .gitignore and rg.
func globMatch(re *regexp.Regexp, hasSlash bool, inBase string) bool {
	if hasSlash {
		return re.MatchString(inBase)
	}
	return re.MatchString(path.Base(inBase))
}

// globFilter is an optional name filter for grep.
type globFilter struct {
	re    *regexp.Regexp
	slash bool
}

func newGlobFilter(pat string) (globFilter, error) {
	if pat == "" {
		return globFilter{}, nil
	}
	re, err := globRegexp(strings.TrimPrefix(pat, "./"))
	if err != nil {
		return globFilter{}, fmt.Errorf("invalid glob %q: %v", pat, err)
	}
	return globFilter{re: re, slash: strings.Contains(pat, "/")}, nil
}

func (g globFilter) match(inBase string) bool {
	return g.re == nil || globMatch(g.re, g.slash, inBase)
}

// NewGlob returns glob.
func NewGlob() tools.Tool {
	return &tool{
		spec: newSpec("glob",
			"Find files by name pattern, newest first. Supports ** for any depth, e.g. **/*_test.go. Respects .gitignore.",
			tools.Schema(map[string]any{
				"pattern": tools.Str("Glob pattern, e.g. *.go or **/config/*.yaml."),
				"path":    tools.Str("Directory to search in, relative to the project root. Default: the project root."),
			}, "pattern")),
		readOnly: true,
		run: func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
			var a struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
			}
			if err := tools.Decode(args, &a); err != nil {
				return failTagged(err)
			}
			if a.Pattern == "" {
				return tools.Errorf("pattern is required.")
			}
			gf, err := newGlobFilter(a.Pattern)
			if err != nil {
				return failTagged(err)
			}
			p := a.Path
			if p == "" {
				p = "."
			}
			t, err := resolve(ctx, env, "glob", p, accessSearch)
			if err != nil {
				return failTagged(err)
			}
			fi, err := statPath(t)
			if err != nil {
				return failTagged(err)
			}
			if !fi.IsDir() {
				return tools.Errorf("%s is not a directory.", t.rel)
			}
			baseRel := relSlash(env.Workdir, t.abs)
			if baseRel == "." {
				baseRel = ""
			}
			type hit struct {
				rel string
				mod time.Time
			}
			var hits []hit
			scanned := 0
			walkDir(ctx, env.Workdir, t.abs, chainIgnore(env.Workdir, t.abs), 1, -1,
				func(abs, rel string, fi fs.FileInfo) bool {
					scanned++
					if scanned > maxGlobScan {
						return false
					}
					if fi.IsDir() {
						return true
					}
					inBase := rel
					if baseRel != "" {
						inBase = strings.TrimPrefix(rel, baseRel+"/")
					}
					if gf.match(inBase) {
						hits = append(hits, hit{rel, fi.ModTime()})
					}
					return true
				})
			if len(hits) == 0 {
				return tools.Result{Output: "No files found.", Summary: "0 files"}
			}
			sort.SliceStable(hits, func(i, j int) bool {
				if !hits[i].mod.Equal(hits[j].mod) {
					return hits[i].mod.After(hits[j].mod)
				}
				return hits[i].rel < hits[j].rel
			})
			shown := hits
			if len(shown) > maxGlobResults {
				shown = shown[:maxGlobResults]
			}
			lines := make([]string, len(shown))
			for i, h := range shown {
				lines[i] = h.rel
			}
			out := strings.Join(lines, "\n")
			if len(hits) > len(shown) {
				out += fmt.Sprintf("\n(Showing %d of %d matches. Narrow the pattern or path.)", len(shown), len(hits))
			}
			return tools.Result{Output: out, Summary: fmt.Sprintf("%d files", len(hits))}
		},
	}
}
