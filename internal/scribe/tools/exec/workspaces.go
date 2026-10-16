package exec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// maxWorkspacePackages caps the monorepo listing so a huge workspace does not
// flood the result.
const maxWorkspacePackages = 50

var (
	goWorkBlock   = regexp.MustCompile(`(?ms)^\s*use\s*\(\s*(.*?)\)`)
	goWorkLine    = regexp.MustCompile(`(?m)^\s*use\s+(\S+)\s*$`)
	cargoWorkBlk  = regexp.MustCompile(`(?ms)^\[workspace\]\s*(.*?)(^\[|\z)`)
	cargoMembers  = regexp.MustCompile(`(?s)members\s*=\s*\[(.*?)\]`)
	quotedString  = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)
	pnpmPackageLn = regexp.MustCompile(`(?m)^\s*-\s*['"]?([^'"\s#]+)['"]?\s*$`)
)

// workspacePackages lists the package directories of a monorepo rooted at dir,
// relative to dir. It reads the workspace declarations of go.work, Cargo.toml,
// pnpm-workspace.yaml and package.json, and expands each pattern with a glob.
// It returns nil when dir declares no workspace, so a single package costs
// only a few file reads.
func workspacePackages(dir string) []string {
	var patterns []string
	patterns = append(patterns, goWorkPatterns(dir)...)
	patterns = append(patterns, cargoPatterns(dir)...)
	patterns = append(patterns, pnpmPatterns(dir)...)
	patterns = append(patterns, npmPatterns(dir)...)
	seen := map[string]bool{}
	var out []string
	for _, p := range patterns {
		p = strings.TrimPrefix(strings.TrimSpace(p), "./")
		p = strings.TrimSuffix(p, "/")
		if p == "" || strings.HasPrefix(p, "!") || strings.HasPrefix(p, "/") {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(p)))
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil || !fi.IsDir() {
				continue
			}
			rel, err := filepath.Rel(dir, m)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	if len(out) > maxWorkspacePackages {
		out = out[:maxWorkspacePackages]
	}
	return out
}

func readOptional(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func goWorkPatterns(dir string) []string {
	src := readOptional(filepath.Join(dir, "go.work"))
	if src == "" {
		return nil
	}
	var out []string
	for _, m := range goWorkBlock.FindAllStringSubmatch(src, -1) {
		for _, ln := range strings.Split(m[1], "\n") {
			if f := strings.Fields(ln); len(f) == 1 {
				out = append(out, f[0])
			}
		}
	}
	for _, m := range goWorkLine.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	return out
}

func cargoPatterns(dir string) []string {
	src := readOptional(filepath.Join(dir, "Cargo.toml"))
	block := cargoWorkBlk.FindStringSubmatch(src)
	if block == nil {
		return nil
	}
	m := cargoMembers.FindStringSubmatch(block[1])
	if m == nil {
		return nil
	}
	var out []string
	for _, q := range quotedString.FindAllStringSubmatch(m[1], -1) {
		if q[1] != "" {
			out = append(out, q[1])
		} else {
			out = append(out, q[2])
		}
	}
	return out
}

func pnpmPatterns(dir string) []string {
	src := readOptional(filepath.Join(dir, "pnpm-workspace.yaml"))
	if src == "" {
		return nil
	}
	var out []string
	for _, m := range pnpmPackageLn.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	return out
}

func npmPatterns(dir string) []string {
	src := readOptional(filepath.Join(dir, "package.json"))
	if src == "" {
		return nil
	}
	var pkg struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if json.Unmarshal([]byte(src), &pkg) != nil || len(pkg.Workspaces) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(pkg.Workspaces, &list) == nil {
		return list
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(pkg.Workspaces, &obj) == nil {
		return obj.Packages
	}
	return nil
}
