package tui

import (
	"context"
	"strings"
)

// showTurnError puts an agent or turn error on screen as a dismissable alert.
// Spec §7.7 reserves the alert for errors that need the user to dismiss them,
// and a failed turn is one: the flash line is too brief to read and the chat
// entry can scroll away. The error log entry is written separately and stays.
//
// Only the visible tab gets an alert. An error from a background tab is in
// the error log and flags its tab; popping it over the screen the user is
// looking at would be the wrong tab's problem on their screen.
func (m *Model) showTurnError(t *tab, text string) {
	if t != m.cur() || isStoppedText(text) {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" || m.errorAlertShowing(text) {
		return
	}
	body := text + "\n\nThe error is also in /errors."
	m.pushModal(NewAlert(m.th, "Turn failed", body, "error"))
}

// isStoppedText reports whether an error is the user stopping the turn. A stop
// is not a failure, so it gets no alert. The agent reports it as the plain
// context error text, because it carries no other marker.
func isStoppedText(text string) bool {
	return strings.TrimSpace(text) == context.Canceled.Error()
}

// errorAlertShowing reports whether an error alert with this body is already
// on the stack. One failure can arrive as more than one event, and the same
// error should not stack up as several dialogs.
func (m *Model) errorAlertShowing(text string) bool {
	for _, md := range m.modals {
		a, ok := md.(*Alert)
		if !ok || a.level != "error" {
			continue
		}
		if strings.HasPrefix(a.message, text) {
			return true
		}
	}
	return false
}
