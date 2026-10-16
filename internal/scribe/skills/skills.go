// Package skills discovers, renders, installs and removes Scribe skills.
//
// A skill is a directory that contains SKILL.md. The file starts with YAML
// frontmatter (name, description, optional allowed-tools) followed by
// Markdown instructions. Skills are searched in these directories, first
// match by name wins:
//
//  1. <root>/.scribe/skills, <root>/.claude/skills, <root>/.agents/skills (project)
//  2. config.Home()/skills, ~/.claude/skills, ~/.agents/skills (global)
//
// Symlinked skill directories are followed, and copies that resolve to the
// same real directory are counted once.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"gopkg.in/yaml.v3"
)

// Scopes of a skill.
const (
	ScopeProject = "project"
	ScopeGlobal  = "global"
)

// MaxBodyChars caps how much of SKILL.md Load returns.
const MaxBodyChars = 16000

// maxListedFiles caps the supporting-file listing Load prints.
const maxListedFiles = 200

// Skill is one discovered skill.
type Skill struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// Dir is the skill directory (absolute, as found; may be a symlink).
	Dir string `json:"dir"`
	// Source is the discovery directory the skill came from.
	Source string `json:"source"`
	Scope  string `json:"scope"`
	// Problem is set when SKILL.md could not be parsed; the skill is still
	// listed, named after its directory.
	Problem string `json:"problem,omitempty"`
}

// Set is the result of one discovery pass.
type Set struct {
	Root   string
	Skills []Skill
}

type frontmatter struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	AllowedTools any    `yaml:"allowed-tools"`
}

// SkillFile is the name of the file that marks a skill directory.
const SkillFile = "SKILL.md"

// Discover scans every skill directory for root (an empty root skips the
// project directories) and returns the effective skills sorted by name.
func Discover(root string) (*Set, error) {
	set := &Set{Root: root}
	seenName := map[string]bool{}
	seenReal := map[string]bool{}
	for _, src := range sources(root) {
		entries, err := os.ReadDir(src.dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("skills: read %s: %w", src.dir, err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			dir := filepath.Join(src.dir, e.Name())
			info, err := os.Stat(dir) // follows symlinks
			if err != nil || !info.IsDir() {
				continue
			}
			mdPath := filepath.Join(dir, SkillFile)
			if fi, err := os.Stat(mdPath); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			real, err := filepath.EvalSymlinks(dir)
			if err != nil {
				real = dir
			}
			if seenReal[real] {
				continue
			}
			seenReal[real] = true
			sk, err := readSkill(dir, mdPath, e.Name())
			if err != nil {
				sk.Problem = err.Error()
			}
			sk.Source = src.label
			sk.Scope = src.scope
			if seenName[sk.Name] {
				continue // an earlier directory already provides this name
			}
			seenName[sk.Name] = true
			set.Skills = append(set.Skills, sk)
		}
	}
	sort.SliceStable(set.Skills, func(i, j int) bool { return set.Skills[i].Name < set.Skills[j].Name })
	return set, nil
}

type source struct {
	dir, label, scope string
}

func sources(root string) []source {
	var out []source
	if root != "" {
		out = append(out,
			source{filepath.Join(root, ".scribe", "skills"), ".scribe/skills", ScopeProject},
			source{filepath.Join(root, ".claude", "skills"), ".claude/skills", ScopeProject},
			source{filepath.Join(root, ".agents", "skills"), ".agents/skills", ScopeProject},
		)
	}
	out = append(out, source{filepath.Join(config.Home(), "skills"), "scribe home skills", ScopeGlobal})
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out,
			source{filepath.Join(home, ".claude", "skills"), "~/.claude/skills", ScopeGlobal},
			source{filepath.Join(home, ".agents", "skills"), "~/.agents/skills", ScopeGlobal},
		)
	}
	return out
}

// readSkill parses SKILL.md. On a parse error it still returns a Skill named
// after dir, with the error.
func readSkill(dir, mdPath, dirName string) (Skill, error) {
	sk := Skill{Name: dirName, Dir: dir}
	src, err := os.ReadFile(mdPath)
	if err != nil {
		return sk, err
	}
	fm, _, err := ParseFrontmatter(string(src))
	if err != nil {
		return sk, fmt.Errorf("SKILL.md frontmatter: %w", err)
	}
	if n := strings.TrimSpace(fm.Name); n != "" && validName(n) == nil {
		sk.Name = n
	}
	sk.Description = oneLine(fm.Description)
	sk.AllowedTools = toolList(fm.AllowedTools)
	return sk, nil
}

// ParseFrontmatter splits a SKILL.md into its frontmatter and body. A file
// without a leading "---" line has empty frontmatter and the whole text as
// body. The frontmatter must be a YAML mapping.
func ParseFrontmatter(src string) (fm frontmatter, body string, err error) {
	s := strings.TrimPrefix(src, "\ufeff")
	lines := strings.SplitAfter(s, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r\n") != "---" {
		return fm, strings.TrimSpace(s), nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r\n") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return fm, "", errors.New("no closing --- line")
	}
	yamlText := strings.Join(lines[1:end], "")
	if strings.TrimSpace(yamlText) != "" {
		if err := yaml.Unmarshal([]byte(yamlText), &fm); err != nil {
			return frontmatter{}, "", err
		}
	}
	body = strings.TrimSpace(strings.Join(lines[end+1:], ""))
	return fm, body, nil
}

// toolList accepts "Read, Grep" or a YAML list.
func toolList(v any) []string {
	var raw []string
	switch t := v.(type) {
	case string:
		raw = strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				raw = append(raw, s)
			}
		}
	}
	var out []string
	for _, s := range raw {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// validName rejects names that could escape the skills directory.
func validName(n string) error {
	switch {
	case n == "" || n == "." || n == "..":
		return fmt.Errorf("invalid skill name %q", n)
	case strings.HasPrefix(n, "."):
		return fmt.Errorf("skill name %q must not start with a dot", n)
	case strings.ContainsAny(n, `/\`+"\x00"):
		return fmt.Errorf("skill name %q must not contain a path separator", n)
	}
	return nil
}

// Find returns the skill called name.
func (s *Set) Find(name string) (Skill, bool) {
	for _, sk := range s.Skills {
		if sk.Name == name {
			return sk, true
		}
	}
	return Skill{}, false
}

// Catalog renders one line per skill for the system prompt. It is empty when
// there are no skills.
func (s *Set) Catalog() string {
	if len(s.Skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Skills available (load one with the skills tool when its description fits the task):\n")
	for _, sk := range s.Skills {
		desc := sk.Description
		if desc == "" {
			desc = "(no description)"
		}
		if utf8.RuneCountInString(desc) > 200 {
			desc = trimRunes(desc, 200)
		}
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Render returns the skill for the model: its instructions (frontmatter
// removed, capped at MaxBodyChars) and the other files in its directory.
func (s *Set) Render(name string) (string, error) {
	sk, ok := s.Find(name)
	if !ok {
		return "", fmt.Errorf("no skill named %q", name)
	}
	src, err := os.ReadFile(filepath.Join(sk.Dir, SkillFile))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", SkillFile, err)
	}
	_, body, err := ParseFrontmatter(string(src))
	if err != nil {
		body = strings.TrimSpace(string(src))
	}
	if utf8.RuneCountInString(body) > MaxBodyChars {
		body = trimRunes(body, MaxBodyChars) + "\n[... truncated]"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Skill: %s\nDirectory: %s\n\n%s\n", sk.Name, sk.Dir, body)
	files := supportingFiles(sk.Dir)
	if len(files) > 0 {
		b.WriteString("\nOther files in this skill directory (read them with the read tool when needed):\n")
		for _, f := range files {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	return b.String(), nil
}

// supportingFiles lists files under dir except SKILL.md, relative and
// slash-separated, skipping hidden directories.
func supportingFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p == dir {
			return nil
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() == SkillFile {
			return nil
		}
		if len(out) >= maxListedFiles {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(dir, p)
		if err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func trimRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
