package tui

import (
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
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
