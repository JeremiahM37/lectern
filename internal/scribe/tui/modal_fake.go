package tui

import (
	"errors"
	"strings"
)

// Skill catalogue for the fake backend, so the skills browser has data in
// tests and in the demo.

// fakeRegistry is the registry the fake backend offers the Search tab.
var fakeRegistry = []RegistrySkill{
	{ID: "acme/review-skill", Source: "https://github.com/acme/review-skill.git", Installs: 1234},
	{ID: "acme/changelog", Source: "https://github.com/acme/changelog.git", Installs: 987},
	{ID: "infra/k8s-debug", Source: "https://github.com/infra/k8s-debug.git", Installs: 412},
}

// PopularSkills implements SkillRegistry.
func (f *FakeBackend) PopularSkills() ([]RegistrySkill, error) {
	return append([]RegistrySkill(nil), fakeRegistry...), nil
}

// SearchSkills implements SkillRegistry, matching on the id.
func (f *FakeBackend) SearchSkills(query string) ([]RegistrySkill, error) {
	var out []RegistrySkill
	q := strings.ToLower(query)
	for _, r := range fakeRegistry {
		if strings.Contains(strings.ToLower(r.ID), q) {
			out = append(out, r)
		}
	}
	return out, nil
}

// RemoveSkill implements SkillRemover for the fake catalogue.
func (f *FakeBackend) RemoveSkill(name string) error {
	for _, d := range f.SkillDetails() {
		if d.Name == name {
			return nil
		}
	}
	return errors.New("no skill named " + name)
}

// SkillDetails implements SkillCatalog.
func (f *FakeBackend) SkillDetails() []SkillDetail {
	return []SkillDetail{
		{Name: "release-notes", Description: "Write release notes from merged PRs and tags.", Scope: "project", Dir: ".scribe/skills/release-notes"},
		{Name: "db-migrations", Description: "Plan and apply schema migrations safely.", Scope: "project", Dir: ".scribe/skills/db-migrations"},
		{Name: "k8s-debug", Description: "Diagnose failing pods, crash loops and probes.", Scope: "global", Dir: "~/.claude/skills/k8s-debug", Problem: "missing description in frontmatter"},
	}
}

// SkillBody implements SkillCatalog.
func (f *FakeBackend) SkillBody(name string) (string, error) {
	return "# Skill: " + name + "\n\nFollow the steps in this skill when the task matches its description.\n\n1. Read the relevant files first.\n2. Make the smallest change that works.\n3. Run the project's tests.", nil
}
