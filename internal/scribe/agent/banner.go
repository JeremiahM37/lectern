package agent

import "github.com/JeremiahM37/lectern/v2/internal/scribe/llm"

// defaultModeNote tells the model that a mode with a banner has ended.
const defaultModeNote = `# Mode: default
You are back in default mode: edits and commands are allowed, with approval for anything destructive. The mode notes earlier in this conversation no longer apply.`

// modeNotice is the user message to latch into the history when a turn runs
// in mode after the conversation was last told about prev. It is "" when
// there is nothing new to say: the mode did not change, or both the old and
// the new mode are the default.
//
// The notice is a user message rather than part of the system prompt, so the
// system prompt is the same bytes in every mode and the provider's prompt
// cache is not broken by a mode switch. It is sent once per change, not on
// every turn, because it stays in the history.
func modeNotice(prev, mode string) string {
	if mode == prev {
		return ""
	}
	if b := ModeBanner(mode); b != "" {
		return b
	}
	if ModeBanner(prev) != "" {
		return defaultModeNote
	}
	return ""
}

// latchMode latches a mode change that happened during a turn into the
// history, so the next model call sees it.
func (a *Agent) latchMode() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if notice := modeNotice(a.latchedMode, a.mode); notice != "" {
		a.msgs = append(a.msgs, llm.UserText(notice))
	}
	a.latchedMode = a.mode
}
