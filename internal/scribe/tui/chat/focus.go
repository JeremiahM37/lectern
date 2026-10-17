package chat

import (
	"path/filepath"
	"strings"
)

// SetSessionHeader sets the line shown above the transcript of a foreign
// session (one saved from another project or client). Restore sets its own
// header for a session that was compacted; call this after Restore. An empty
// text hides the header.
func (v *View) SetSessionHeader(text string) {
	v.header = text
	v.gen++
}

// ForeignSessionHeader is the header text for a session saved in project
// savedIn when it is opened in project here. It is "" when the two are the
// same folder, which is the normal case and gets no header. An unknown
// folder on either side gets none either: saying "from another project" when
// it is only unknown would be wrong.
func ForeignSessionHeader(savedIn, here string) string {
	if savedIn == "" || here == "" {
		return ""
	}
	if filepath.Clean(savedIn) == filepath.Clean(here) {
		return ""
	}
	return "from another project: " + savedIn
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
		return answerRaw(rec), true
	}
	return rec.Text, true
}

// answerRaw is the final answer's text as the model wrote it, line breaks and
// markdown kept, for copying. (answerText normalises it for comparisons.)
func answerRaw(rec *Record) string {
	last := lastModelTools(rec)
	var parts []string
	for i, s := range rec.Segments {
		if s.Kind == "text" && i > last {
			if t := strings.TrimSpace(s.Text); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}
