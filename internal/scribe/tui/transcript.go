package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// transcript is the chat view of one tab. Only this file knows the chat
// package's constructor and option names; everything else goes through it.
type transcript = *chat.View

// newTranscript makes an empty transcript with opt.
func newTranscript(opt chat.Options) transcript { return chat.New(opt) }

// rebuildTranscript returns a copy of old under new options (theme or view
// flags). The chat package keeps its state through Export and Restore.
func rebuildTranscript(old transcript, opt chat.Options, w, h int) transcript {
	nv := chat.New(opt)
	nv.Restore(old.Export())
	nv.SetSize(w, h)
	// The new view draws its own images; free the old view's in the terminal.
	if old != nil {
		old.ForgetImages()
	}
	return nv
}

// changedFiles lists created and modified paths, created first.
func changedFiles(t transcript) []string {
	created, modified := t.ChangedFiles()
	return append(append([]string{}, created...), modified...)
}

// replayEvents feeds a saved event list into a transcript. The chat view does
// not add user prompts or steering messages from events itself, so those are
// added here as they arrive.
func replayEvents(t transcript, evs []agent.Event) {
	for _, ev := range evs {
		switch ev.Kind {
		case agent.EvTurnStart:
			if ev.Prompt != "" {
				t.AddUser(ev.Prompt, 0, ev.At, false)
			}
		case agent.EvSteering:
			t.AddUser(ev.Text, 0, ev.At, true)
			continue
		}
		t.Apply(ev)
	}
}

// liveTime is the time to stamp a locally created row with.
func liveTime() time.Time { return time.Now() }

// chatFrameMin is the narrowest transcript that gets its border. Narrower
// than this the border would take the text's room for no gain.
const chatFrameMin = 40

// chatFramed reports whether the transcript is drawn inside a rounded border.
// The landing page has no transcript, so it has no border either.
func (m *Model) chatFramed() bool {
	return m.phase != phaseLanding && m.width >= chatFrameMin
}

// chatInset is how many cells and rows the border takes from the transcript:
// one on each side when framed, none otherwise.
func (m *Model) chatInset() (cols, rows int) {
	if m.chatFramed() {
		return 2, 2
	}
	return 0, 0
}

// chatBorderColor is the border colour for the transcript. Idle it is
// textSubtle; while a turn runs (or the context is being compacted) it
// pings out to brandSecondary and back, one cycle per 600 ms.
func (m *Model) chatBorderColor(t *tab) lipgloss.TerminalColor {
	if t.busy || t.compacting {
		return theme.Ping(m.th.TextSubtle, m.th.BrandSecondary, m.frame, 12)
	}
	return m.th.TextSubtle
}

// frameChat wraps the transcript lines in the rounded border. lines must be
// exactly w-2 cells wide and h-2 rows tall; the result is w by h.
func (m *Model) frameChat(t *tab, lines []string, w int) []string {
	body := make([]string, len(lines))
	copy(body, lines)
	return frame("", "", body, "", w, m.chatBorderColor(t), m.th)
}

// wheelLines is how many lines one wheel notch scrolls. A notch that follows
// another within 80 ms counts as a flick, and a longer run of them scrolls
// further, so a fast fling covers a page while a single notch stays at three
// lines.
func (m *Model) wheelLines(now time.Time) int {
	if now.Sub(m.wheelAt) < 80*time.Millisecond {
		m.wheelRun++
	} else {
		m.wheelRun = 0
	}
	m.wheelAt = now
	switch {
	case m.wheelRun >= 6:
		return 9
	case m.wheelRun >= 2:
		return 6
	}
	return 3
}
