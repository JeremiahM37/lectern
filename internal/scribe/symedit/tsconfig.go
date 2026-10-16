package symedit

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// tsAliases is the path mapping of a project's tsconfig.json
// (compilerOptions.paths, relative to baseUrl or to the tsconfig's directory).
// It lets "@/utils/x" and "~lib/y" resolve to project files, so the move and
// rename tools can follow those imports and write the alias back. A nil
// *tsAliases maps nothing.
type tsAliases struct {
	workdir string
	base    string // project-relative directory the targets are relative to
	rules   []aliasRule
}

// aliasRule is one paths entry. A pattern holds at most one "*"; the text it
// matches fills the "*" of each target.
type aliasRule struct {
	prefix, suffix string
	wild           bool
	targets        []aliasTarget
}

type aliasTarget struct{ prefix, suffix string }

// loadTSAliases reads tsconfig.json at the project root, following one chain of
// "extends" (relative paths only). It returns nil when there is no usable
// mapping.
func loadTSAliases(workdir string) *tsAliases {
	cfgRel := "tsconfig.json"
	if _, err := os.Stat(filepath.Join(workdir, cfgRel)); err != nil {
		return nil
	}
	var cfg struct {
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := readTSConfig(workdir, cfgRel, &cfg); err != nil || len(cfg.CompilerOptions.Paths) == 0 {
		return nil
	}
	cfgDir := path.Dir(cfgRel)
	base := cfgDir
	if b := cfg.CompilerOptions.BaseURL; b != "" {
		base = path.Join(cfgDir, b)
	}
	a := &tsAliases{workdir: workdir, base: path.Clean(base)}
	for pat, targets := range cfg.CompilerOptions.Paths {
		r := aliasRule{}
		r.prefix, r.suffix, r.wild = splitStar(pat)
		for _, t := range targets {
			p, s, _ := splitStar(t)
			r.targets = append(r.targets, aliasTarget{prefix: path.Join(a.base, p), suffix: s})
			if strings.HasSuffix(p, "/") && !strings.HasSuffix(r.targets[len(r.targets)-1].prefix, "/") {
				r.targets[len(r.targets)-1].prefix += "/"
			}
		}
		if len(r.targets) > 0 {
			a.rules = append(a.rules, r)
		}
	}
	return a
}

func readTSConfig(workdir, rel string, into any) error {
	for depth := 0; depth < 5; depth++ {
		data, err := os.ReadFile(filepath.Join(workdir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		var probe struct {
			Extends string `json:"extends"`
		}
		if err := json.Unmarshal(stripJSONC(data), &probe); err != nil {
			return err
		}
		// The child's own options win, so decode the child last.
		if probe.Extends == "" || !strings.HasPrefix(probe.Extends, ".") {
			return json.Unmarshal(stripJSONC(data), into)
		}
		parent := path.Clean(path.Join(path.Dir(rel), probe.Extends))
		if !strings.HasSuffix(parent, ".json") {
			parent += ".json"
		}
		if err := readTSConfig(workdir, parent, into); err != nil {
			return err
		}
		return json.Unmarshal(stripJSONC(data), into)
	}
	return nil
}

// stripJSONC removes // and /* */ comments and trailing commas, which tsconfig
// files allow, so encoding/json can read them. Strings are left alone.
func stripJSONC(b []byte) []byte {
	var out []byte
	inStr := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			if c == '\\' && i+1 < len(b) {
				i++
				out = append(out, b[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return trailingComma.ReplaceAll(out, []byte("$1"))
}

var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

// splitStar splits a pattern at its "*" (if any).
func splitStar(p string) (prefix, suffix string, wild bool) {
	if i := strings.Index(p, "*"); i >= 0 {
		return p[:i], p[i+1:], true
	}
	return p, "", false
}

// match returns the module key an alias spec names, or "" when no rule
// covers spec and its target file exists.
func (a *tsAliases) match(spec string) string {
	for _, key := range a.candidates(spec) {
		if a.exists(key) {
			return key
		}
	}
	return ""
}

// candidates lists the module keys an alias spec could name, one per target
// of the first rule that covers it, before any file is checked.
func (a *tsAliases) candidates(spec string) []string {
	if a == nil || isRelSpec(spec) {
		return nil
	}
	for _, r := range a.rules {
		var fill string
		if r.wild {
			if len(spec) < len(r.prefix)+len(r.suffix) || !strings.HasPrefix(spec, r.prefix) || !strings.HasSuffix(spec, r.suffix) {
				continue
			}
			fill = spec[len(r.prefix) : len(spec)-len(r.suffix)]
		} else if spec != r.prefix {
			continue
		}
		var out []string
		for _, t := range r.targets {
			out = append(out, tsModKey(t.prefix+fill+t.suffix))
		}
		return out
	}
	return nil
}

// exists reports whether a module key names a project file.
func (a *tsAliases) exists(key string) bool {
	for _, e := range tsModExts {
		if _, err := os.Stat(filepath.Join(a.workdir, filepath.FromSlash(key+e))); err == nil {
			return true
		}
	}
	_, err := os.Stat(filepath.Join(a.workdir, filepath.FromSlash(key), "index.ts"))
	return err == nil
}

// resolve is the module key a specifier in importerRel names: relative
// specifiers as tsResolve does, alias specifiers through the tsconfig paths.
// It returns "" when neither applies.
func (a *tsAliases) resolve(importerRel, spec string) string {
	if isRelSpec(spec) {
		return tsResolve(importerRel, spec)
	}
	return a.match(spec)
}

// respec writes the specifier a file in importerDir uses to reach target. An
// alias that was written as an alias stays one when a rule still covers the
// target; anything else gets the relative form tsRespec writes.
func (a *tsAliases) respec(importerDir, orig, target string) string {
	if a != nil && !isRelSpec(orig) {
		if s := a.aliasFor(target); s != "" {
			return s
		}
	}
	return tsRespec(importerDir, orig, target)
}

// aliasFor spells target as an alias, or "" when no rule covers it.
func (a *tsAliases) aliasFor(target string) string {
	key := tsModKey(target)
	for _, r := range a.rules {
		for _, t := range r.targets {
			tp := strings.TrimSuffix(t.prefix, "/")
			if !r.wild {
				if key == tsModKey(tp+t.suffix) {
					return r.prefix
				}
				continue
			}
			if !strings.HasPrefix(key, tp+"/") && key != tp {
				continue
			}
			rest := strings.TrimPrefix(strings.TrimPrefix(key, tp), "/")
			if t.suffix != "" {
				continue // a target with its own suffix is not spelled back
			}
			spelled := r.prefix + rest + r.suffix
			for _, c := range a.candidates(spelled) {
				if c == key {
					return spelled
				}
			}
		}
	}
	return ""
}
