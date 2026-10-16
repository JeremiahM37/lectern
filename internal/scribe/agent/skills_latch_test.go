package agent

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// The loaded-skills pair is latched when the set changes and not repeated
// while it stays the same, and an unload is latched too.
func TestSkillsPairLatchesOnChangeOnly(t *testing.T) {
	var msgs []llm.Message
	add := func(block string) int {
		n := len(skillsNotices(msgs, block))
		msgs = append(msgs, skillsNotices(msgs, block)...)
		return n
	}
	const one = "<loaded_skills>\nThe following 1 skill(s) are loaded: a.\n<skill name=\"a\">\nA\n</skill>\n</loaded_skills>"
	const two = "<loaded_skills>\nThe following 2 skill(s) are loaded: a, b.\n</loaded_skills>"

	if n := add(""); n != 0 {
		t.Fatalf("nothing loaded: latched %d messages", n)
	}
	if n := add(one); n != 2 {
		t.Fatalf("first load should latch a user/assistant pair, got %d", n)
	}
	if msgs[0].Role != llm.RoleUser || msgs[1].Role != llm.RoleAssistant {
		t.Fatalf("pair roles = %v, %v", msgs[0].Role, msgs[1].Role)
	}
	if n := add(one); n != 0 {
		t.Fatalf("unchanged set must not be re-sent, got %d", n)
	}
	if n := add(two); n != 2 {
		t.Fatalf("changed set should latch again, got %d", n)
	}
	if n := add(""); n != 2 || !strings.Contains(msgs[len(msgs)-2].Text(), "<skills_unloaded>") {
		t.Fatalf("unload should latch the unloaded pair, got %d", n)
	}
	if n := add(""); n != 0 {
		t.Fatalf("already unloaded: latched %d", n)
	}
}

// After a history reset (a resumed session, or a cleared plan), the set is
// latched again because the history no longer has it.
func TestSkillsPairIsLatchedAgainAfterAReset(t *testing.T) {
	const block = "<loaded_skills>\nx\n</loaded_skills>"
	msgs := append([]llm.Message{}, skillsNotices(nil, block)...)
	if n := len(skillsNotices(msgs, block)); n != 0 {
		t.Fatalf("resumed history already has the set: latched %d", n)
	}
	if n := len(skillsNotices([]llm.Message{llm.UserText("fresh")}, block)); n != 2 {
		t.Fatalf("a history with no skills message must latch the set, got %d", n)
	}
}
