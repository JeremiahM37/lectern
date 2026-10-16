package repomap

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// fileIndex answers "which repo file does this import name?" It is built
// once per graph rebuild from the set of indexed files.
type fileIndex struct {
	files    map[string]*fileInfo
	byBase   map[string][]string
	goDirs   map[string][]string
	goModule string
	tsBase   string
	tsPaths  []tsPath
}

type tsPath struct {
	prefix, suffix string
	star           bool
	targets        []string
}

var tsExts = []string{".ts", ".tsx", ".d.ts", ".js", ".jsx", ".mjs", ".cjs"}
var jsExts = map[string]bool{".js": true, ".jsx": true, ".mjs": true, ".cjs": true}

func newFileIndex(root string, files map[string]*fileInfo) *fileIndex {
	ix := &fileIndex{files: files, byBase: map[string][]string{}, goDirs: map[string][]string{}}
	for p := range files {
		ix.byBase[path.Base(p)] = append(ix.byBase[path.Base(p)], p)
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			d := path.Dir(p)
			ix.goDirs[d] = append(ix.goDirs[d], p)
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			if f := strings.Fields(ln); len(f) == 2 && f[0] == "module" {
				ix.goModule = strings.Trim(f[1], `"`)
				break
			}
		}
	}
	ix.loadTSConfig(root)
	return ix
}

func (ix *fileIndex) loadTSConfig(root string) {
	b, err := os.ReadFile(filepath.Join(root, "tsconfig.json"))
	if err != nil {
		return
	}
	// tsconfig is JSONC; drop whole-line comments, which covers the common case.
	var keep []string
	for _, ln := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(ln), "//") {
			keep = append(keep, ln)
		}
	}
	var cfg struct {
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal([]byte(strings.Join(keep, "\n")), &cfg) != nil {
		return
	}
	ix.tsBase = path.Clean(cfg.CompilerOptions.BaseURL)
	if ix.tsBase == "." {
		ix.tsBase = ""
	}
	for pat, targets := range cfg.CompilerOptions.Paths {
		p := tsPath{targets: targets}
		if i := strings.Index(pat, "*"); i >= 0 {
			p.star = true
			p.prefix, p.suffix = pat[:i], pat[i+1:]
		} else {
			p.prefix = pat
		}
		ix.tsPaths = append(ix.tsPaths, p)
	}
}

// resolve returns the repo files an import refers to. Unresolvable imports
// (packages, std lib) return nil.
func (ix *fileIndex) resolve(from string, imp rawImport) []string {
	dir := path.Dir(from)
	switch imp.Kind {
	case "go":
		if ix.goModule != "" && (imp.Spec == ix.goModule || strings.HasPrefix(imp.Spec, ix.goModule+"/")) {
			d := strings.TrimPrefix(strings.TrimPrefix(imp.Spec, ix.goModule), "/")
			if d == "" {
				d = "."
			}
			return ix.goDirs[d]
		}
	case "js":
		if strings.HasPrefix(imp.Spec, ".") {
			return one(ix.tsFile(path.Join(dir, imp.Spec)))
		}
		for _, p := range ix.tsPaths {
			if !p.star {
				if imp.Spec != p.prefix {
					continue
				}
				for _, t := range p.targets {
					if f := ix.tsFile(path.Join(ix.tsBase, t)); f != "" {
						return []string{f}
					}
				}
				continue
			}
			if len(imp.Spec) >= len(p.prefix)+len(p.suffix) && strings.HasPrefix(imp.Spec, p.prefix) && strings.HasSuffix(imp.Spec, p.suffix) {
				mid := imp.Spec[len(p.prefix) : len(imp.Spec)-len(p.suffix)]
				for _, t := range p.targets {
					if f := ix.tsFile(path.Join(ix.tsBase, strings.Replace(t, "*", mid, 1))); f != "" {
						return []string{f}
					}
				}
			}
		}
		if ix.tsBase != "" {
			return one(ix.tsFile(path.Join(ix.tsBase, imp.Spec)))
		}
	case "py":
		return ix.resolvePy(from, imp)
	case "rs-mod":
		stem := strings.TrimSuffix(path.Base(from), ".rs")
		base := dir
		if stem != "mod" && stem != "lib" && stem != "main" {
			base = path.Join(dir, stem)
		}
		return firstExisting(ix, path.Join(base, imp.Spec+".rs"), path.Join(base, imp.Spec, "mod.rs"))
	case "rs-use":
		root := ""
		if strings.HasPrefix(from, "src/") {
			root = "src/"
		} else if i := strings.Index(from, "/src/"); i >= 0 {
			root = from[:i+5]
		}
		segs := strings.Split(strings.TrimPrefix(imp.Spec, "crate::"), "::")
		for n := len(segs); n >= 1; n-- {
			p := path.Join(append([]string{root}, segs[:n]...)...)
			if r := firstExisting(ix, p+".rs", path.Join(p, "mod.rs")); len(r) > 0 {
				return r
			}
		}
	case "jvm":
		spec := strings.TrimSuffix(imp.Spec, ".")
		for drop := 0; drop <= 1; drop++ {
			segs := strings.Split(spec, ".")
			if drop >= len(segs) || len(segs)-drop < 2 {
				break
			}
			base := strings.Join(segs[:len(segs)-drop], "/")
			var out []string
			out = append(out, ix.bySuffix(base+".java")...)
			out = append(out, ix.bySuffix(base+".kt")...)
			if len(out) > 0 {
				return out
			}
		}
	case "c":
		if f := ix.exists(path.Join(dir, imp.Spec)); f != "" {
			return []string{f}
		}
		return ix.bySuffix(imp.Spec)
	case "rb-rel":
		return firstExisting(ix, path.Join(dir, imp.Spec+".rb"))
	case "rb":
		return ix.bySuffix(imp.Spec + ".rb")
	case "php":
		segs := strings.Split(strings.ReplaceAll(imp.Spec, `\`, "/"), "/")
		if len(segs) < 2 {
			return nil
		}
		want := strings.Join(segs[len(segs)-2:], "/") + ".php"
		return ix.bySuffix(want)
	case "lua":
		base := strings.ReplaceAll(imp.Spec, ".", "/")
		return firstExisting(ix, base+".lua", path.Join(base, "init.lua"))
	case "sh":
		return firstExisting(ix, path.Join(dir, imp.Spec), path.Clean(imp.Spec))
	case "zig":
		return firstExisting(ix, path.Join(dir, imp.Spec))
	}
	return nil
}

func (ix *fileIndex) resolvePy(from string, imp rawImport) []string {
	spec := imp.Spec
	level := 0
	for level < len(spec) && spec[level] == '.' {
		level++
	}
	rest := spec[level:]
	base := "."
	if level > 0 {
		base = path.Dir(from)
		for i := 1; i < level; i++ {
			base = path.Dir(base)
		}
	}
	var parts []string
	for _, p := range strings.Split(rest, ".") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	mod := path.Join(append([]string{base}, parts...)...)
	var out []string
	if rest != "" || level == 0 {
		out = firstExisting(ix, mod+".py", path.Join(mod, "__init__.py"))
		if len(out) == 0 && level == 0 {
			out = firstExisting(ix, path.Join(path.Dir(from), mod+".py"), path.Join(path.Dir(from), mod, "__init__.py"))
		}
	}
	for _, n := range imp.Names {
		out = append(out, firstExisting(ix, path.Join(mod, n)+".py")...)
	}
	return out
}

// tsFile resolves an extensionless or .js-suffixed module path to a file.
func (ix *fileIndex) tsFile(base string) string {
	base = path.Clean(base)
	if ix.files[base] != nil {
		return base
	}
	var cands []string
	if ext := path.Ext(base); jsExts[ext] {
		stem := strings.TrimSuffix(base, ext)
		cands = append(cands, stem+".ts", stem+".tsx", stem+".d.ts")
	}
	for _, e := range tsExts {
		cands = append(cands, base+e)
	}
	for _, e := range tsExts {
		cands = append(cands, path.Join(base, "index"+e))
	}
	for _, c := range cands {
		if ix.files[c] != nil {
			return c
		}
	}
	return ""
}

func (ix *fileIndex) exists(p string) string {
	if ix.files[p] != nil {
		return p
	}
	return ""
}

func firstExisting(ix *fileIndex, cands ...string) []string {
	for _, c := range cands {
		if ix.files[c] != nil {
			return []string{c}
		}
	}
	return nil
}

// bySuffix returns files whose path ends in suffix on a path boundary.
func (ix *fileIndex) bySuffix(suffix string) []string {
	var out []string
	for _, f := range ix.byBase[path.Base(suffix)] {
		if f == suffix || strings.HasSuffix(f, "/"+suffix) {
			out = append(out, f)
		}
	}
	return out
}

func one(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
