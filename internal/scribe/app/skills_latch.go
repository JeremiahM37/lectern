package app

import (
	"fmt"
	"strings"
)

// loadedSkillsBlock is the text of the user message that latches the skills
// loaded into context for this project (agent.SetSkills). It is "" when none
// is loaded, which unlatches them. The format follows the skills tool: one
// <skill name="..."> block per skill, under a header saying how to apply them.
func (a *App) loadedSkillsBlock() string {
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
	var found []string
	var b strings.Builder
	for _, n := range names {
		body, err := set.Render(n)
		if err != nil {
			continue // removed from disk since it was loaded
		}
		found = append(found, n)
		fmt.Fprintf(&b, "<skill name=\"%s\">\n%s\n</skill>\n", n, strings.TrimSpace(body))
	}
	if len(found) == 0 {
		return ""
	}
	return fmt.Sprintf("<loaded_skills>\nThe following %d skill(s) are loaded: %s. The user loaded them into context for this project. Apply them when the task matches their domain.\n%s</loaded_skills>",
		len(found), strings.Join(found, ", "), b.String())
}
