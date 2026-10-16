package app

import (
	"errors"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

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
