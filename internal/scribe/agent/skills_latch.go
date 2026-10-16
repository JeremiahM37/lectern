package agent

import (
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// The loaded skills reach the model as a user/assistant pair in the history,
// latched like the mode banner: the pair is added when the set of loaded
// skills changes, not on every turn. The system prompt stays the same bytes
// whatever is loaded, so the provider's prompt cache is not broken by a load.
const (
	skillsOpenTag     = "<loaded_skills>"
	skillsClosedTag   = "<skills_unloaded>"
	skillsAck         = "Understood. I will apply the loaded skills wherever they match the task."
	skillsUnloadedMsg = "<skills_unloaded>\nThe skills loaded earlier were unloaded. They no longer apply, so the skill blocks earlier in this conversation are out of date.\n</skills_unloaded>"
	skillsUnloadedAck = "Understood. I will stop applying the unloaded skills."
)

// SetSkills sets the loaded-skills block the next turn latches. block is the
// full text of the user message (the app builds it from the loaded skills),
// or "" when no skill is loaded. Nothing is written to the history here; the
// next Run latches the change if there is one.
func (a *Agent) SetSkills(block string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.skills = block
}

// latchSkillsLocked appends the skills pair for a change since the history
// was last told about the skills. Call it with a.mu held. Whether there was a
// change is read from the history itself, so a resumed session, or one whose
// history was replaced, is told again only when it needs it.
func (a *Agent) latchSkillsLocked() {
	a.msgs = append(a.msgs, skillsNotices(a.msgs, a.skills)...)
}

// skillsNotices returns the messages to add so the history matches block:
// nothing when it already does, the loaded pair when skills were loaded or
// changed, and the unloaded pair when every skill was unloaded.
func skillsNotices(msgs []llm.Message, block string) []llm.Message {
	prev := latchedSkills(msgs)
	switch {
	case block == prev:
		return nil
	case block != "":
		return []llm.Message{llm.UserText(block), llm.AssistantText(skillsAck)}
	default:
		return []llm.Message{llm.UserText(skillsUnloadedMsg), llm.AssistantText(skillsUnloadedAck)}
	}
}

// latchedSkills returns the loaded block the history last latched, or "" when
// the last skills message was an unload or there was none. It looks back from
// the end and stops at the first skills message it finds.
func latchedSkills(msgs []llm.Message) string {
	for k := len(msgs) - 1; k >= 0; k-- {
		m := msgs[k]
		if m.Role != llm.RoleUser {
			continue
		}
		text := m.Text()
		switch {
		case strings.HasPrefix(text, skillsOpenTag):
			return text
		case strings.HasPrefix(text, skillsClosedTag):
			return ""
		}
	}
	return ""
}
