package chat

// SetSessionHeader sets the line shown above the transcript of a foreign
// session (one saved from another project or client). Restore sets its own
// header for a session that was compacted; call this after Restore. An empty
// text hides the header.
func (v *View) SetSessionHeader(text string) {
	v.header = text
	v.gen++
}

// SessionHeader is the header currently shown, or "".
func (v *View) SessionHeader() string { return v.header }

// FocusedText is the plain text of the message at the bottom of the view,
// for Ctrl+Y. It is what a reader is looking at last: the newest message in
// live mode, and the message they scrolled to otherwise. ok is false when the
// transcript is empty.
func (v *View) FocusedText() (text string, ok bool) {
	if len(v.msgs) == 0 {
		return "", false
	}
	v.build()
	bottom := v.windowTop(len(v.lines)) + v.h - 1
	idx := 0
	for i, start := range v.recStart {
		if start <= bottom {
			idx = i
		}
	}
	rec := v.msgs[idx]
	if rec.Role == roleAssistant {
		return answerText(rec), true
	}
	return rec.Text, true
}
