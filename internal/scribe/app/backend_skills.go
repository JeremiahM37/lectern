package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/skills"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// activeSkillsFile holds the names of the skills loaded into context for a
// project. It lives in .scribe/, which Scribe keeps out of git.
const activeSkillsFile = "active-skills.json"

type activeSkillsState struct {
	Active []string `json:"active"`
}

// SkillDetails implements tui.SkillCatalog.
func (b *scribeBackend) SkillDetails() []tui.SkillDetail {
	a := b.current()
	if a.Skills == nil {
		return nil
	}
	out := make([]tui.SkillDetail, 0, len(a.Skills.Skills))
	for _, s := range a.Skills.Skills {
		out = append(out, tui.SkillDetail{Name: s.Name, Description: s.Description, Scope: s.Scope, Dir: s.Dir, Problem: s.Problem})
	}
	return out
}

// SkillBody implements tui.SkillCatalog. It returns the instructions the
// model is given for the skill, with the files in its directory listed.
func (b *scribeBackend) SkillBody(name string) (string, error) {
	a := b.current()
	if a.Skills == nil {
		return "", errors.New("no skills are loaded")
	}
	return a.Skills.Render(name)
}

// InstallSkill implements tui.Backend. src is a local directory or a git URL,
// optionally with "#subdir"; scope is "project" or "global". It returns the
// installed skill's name. The skill is visible to the next request.
func (b *scribeBackend) InstallSkill(src, scope string) (string, error) {
	a := b.current()
	sk, err := skills.Install(a.Workdir, src, scope)
	if err != nil {
		return "", err
	}
	if err := a.refreshSkills(); err != nil {
		return "", err
	}
	return sk.Name, nil
}

// ActiveSkills implements tui.Backend. It returns the names of the skills
// loaded into context for this project, sorted.
func (b *scribeBackend) ActiveSkills() []string {
	return readActiveSkills(b.current().Workdir)
}

// SetSkillActive implements tui.Backend. Turning a skill on adds its
// instructions to the system prompt of the next request. Turning it off
// removes them. The choice is saved for the project.
func (b *scribeBackend) SetSkillActive(name string, on bool) error {
	a := b.current()
	if on {
		a.mu.Lock()
		set := a.Skills
		a.mu.Unlock()
		if _, ok := findSkill(set, name); !ok {
			return fmt.Errorf("no skill named %q", name)
		}
	}
	names := readActiveSkills(a.Workdir)
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	switch {
	case on && !have[name]:
		names = append(names, name)
	case !on && have[name]:
		var kept []string
		for _, n := range names {
			if n != name {
				kept = append(kept, n)
			}
		}
		names = kept
	default:
		return nil
	}
	sort.Strings(names)
	return writeActiveSkills(a.Workdir, names)
}

// activeSkillsSection returns the system-prompt text for the skills loaded
// into context: each skill's instructions, as the skills tool would give
// them. It is empty when none is loaded. BuildSystem does not know about
// skills beyond the catalog, so this section is appended after it.
func (a *App) activeSkillsSection() string {
	names := readActiveSkills(a.Workdir)
	if len(names) == 0 {
		return ""
	}
	a.mu.Lock()
	set := a.Skills
	a.mu.Unlock()
	if set == nil {
		return ""
	}
	var b strings.Builder
	for _, n := range names {
		body, err := set.Render(n)
		if err != nil {
			continue // removed from disk since it was loaded
		}
		b.WriteString("\n")
		b.WriteString(body)
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n# Loaded skills\nThe user loaded these skills into context for this project. Follow them wherever they apply.\n" + b.String()
}

// refreshSkills rediscovers skills after an install. Discover reads the
// skill directories, so the new skill is found at once.
func (a *App) refreshSkills() error {
	set, err := skills.Discover(a.Workdir)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.Skills = set
	a.mu.Unlock()
	return nil
}

func findSkill(set *skills.Set, name string) (skills.Skill, bool) {
	if set == nil {
		return skills.Skill{}, false
	}
	return set.Find(name)
}

// readActiveSkills returns the saved list. A missing or unreadable file
// means no skills are loaded.
func readActiveSkills(root string) []string {
	b, err := os.ReadFile(activeSkillsPath(root))
	if err != nil {
		return nil
	}
	var st activeSkillsState
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return st.Active
}

// writeActiveSkills saves the list atomically.
func writeActiveSkills(root string, names []string) error {
	if err := os.MkdirAll(config.ProjectDir(root), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(activeSkillsState{Active: names}, "", "  ")
	if err != nil {
		return err
	}
	path := activeSkillsPath(root)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func activeSkillsPath(root string) string {
	return filepath.Join(config.ProjectDir(root), activeSkillsFile)
}
