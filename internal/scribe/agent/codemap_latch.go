package agent

import (
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// The code map reaches the model as a user/assistant pair in the history, the
// same way loaded skills do (skills_latch.go). The system prompt stays the
// same bytes for the whole session, and appending a message never changes the
// prefix a provider has cached, so a map change costs no cache miss.
//
// Providers that cache (PromptCaches) get the snapshot once: the app redraws
// the map only after a compaction, which empties the history, so the snapshot
// is latched again then. Other providers get the changed lines as one update
// pair each time the map is redrawn.
const (
	codeMapOpen        = "<soul_map>"
	codeMapClose       = "</soul_map>"
	codeMapUpdateOpen  = "<soul_map_update>"
	codeMapUpdateClose = "</soul_map_update>"
	codeMapIntro       = "The most central files and their key symbols, ranked by how much of the codebase depends on them. This is a snapshot: files may have changed since it was drawn, so read a file before you edit it. Later changes to the map arrive as <soul_map_update> messages."
	codeMapAck         = "Understood. I will use the code map as the snapshot of the codebase and read a file before I edit it."
	codeMapOffNote     = "<soul_map_update>\nThe code map was turned off. Do not rely on the code map shown earlier in this conversation.\n</soul_map_update>"
	codeMapOffAck      = "Understood. I will not rely on the earlier code map."
)

// codeMapLatch is what the history was last told about the code map.
type codeMapLatch struct {
	latched string // the text the history last carried; "" when none
}

// latchCodeMapLocked appends the messages that bring the history up to date
// with want, the map as it is now (Options.CodeMap, read before the lock is
// taken). An empty want means the map is off. Call it with a.mu held, before
// the history is copied into a request.
func (a *Agent) latchCodeMapLocked(want string) {
	a.msgs = append(a.msgs, a.codeMap.notices(a.msgs, want)...)
}

// notices returns the messages to add so the history carries want. Nothing is
// added when it already does. A history with no snapshot in it (a compaction
// or a reset emptied it) is latched again in full.
func (l *codeMapLatch) notices(msgs []llm.Message, want string) []llm.Message {
	if !historyHasCodeMap(msgs) {
		l.latched = ""
	}
	switch {
	case want == "" && l.latched == "":
		return nil
	case want == "":
		l.latched = ""
		return []llm.Message{llm.UserText(codeMapOffNote), llm.AssistantText(codeMapOffAck)}
	case l.latched == "":
		l.latched = want
		return []llm.Message{llm.UserText(codeMapSnapshot(want)), llm.AssistantText(codeMapAck)}
	case want == l.latched:
		return nil
	}
	delta := codeMapDelta(l.latched, want)
	l.latched = want
	if delta == "" {
		return nil
	}
	return []llm.Message{llm.UserText(codeMapUpdate(delta)), llm.AssistantText(codeMapAck)}
}

// historyHasCodeMap reports whether a user message in msgs carries a code map
// snapshot. It looks back from the end, since the latest one is what matters.
func historyHasCodeMap(msgs []llm.Message) bool {
	for k := len(msgs) - 1; k >= 0; k-- {
		if msgs[k].Role == llm.RoleUser && strings.HasPrefix(msgs[k].Text(), codeMapOpen) {
			return true
		}
	}
	return false
}

// codeMapSnapshot wraps a whole map for the history.
func codeMapSnapshot(text string) string {
	return codeMapOpen + "\n" + codeMapIntro + "\n\n" + text + codeMapClose
}

// codeMapUpdate wraps the changed lines for the history.
func codeMapUpdate(delta string) string {
	return codeMapUpdateOpen + "\nThe code map changed since it was last shown. \"+\" lines were added and \"-\" lines were removed; every other line is unchanged.\n" + delta + codeMapUpdateClose
}

// codeMapDelta lists the lines that differ between two renders: "- " for each
// line only prev has and "+ " for each line only next has. Lines are compared
// as multisets, so a moved symbol shows as one removal and one addition, and
// blank lines are ignored.
func codeMapDelta(prev, next string) string {
	var b strings.Builder
	for _, ln := range lineDiff(prev, next) {
		b.WriteString("- " + ln + "\n")
	}
	for _, ln := range lineDiff(next, prev) {
		b.WriteString("+ " + ln + "\n")
	}
	return b.String()
}

// lineDiff returns the non-blank lines of from that to does not have, counting
// repeats.
func lineDiff(from, to string) []string {
	have := map[string]int{}
	for _, ln := range strings.Split(to, "\n") {
		have[ln]++
	}
	var out []string
	for _, ln := range strings.Split(from, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if have[ln] > 0 {
			have[ln]--
			continue
		}
		out = append(out, ln)
	}
	return out
}
