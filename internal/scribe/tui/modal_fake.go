package tui

// Skill catalogue for the fake backend, so the skills browser has data in
// tests and in the demo.

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
