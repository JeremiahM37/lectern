package tui

import "strings"

// gitFailurePrefixes are the flash lines a git operation reports when it fails
// (see modal_git.go and commands_menus.go). Each one leaves the user with a
// repository in a state they must look at, so the failure is an alert. These
// are matched by text because the git producers return plain flashMsg values
// without the alert flag.
var gitFailurePrefixes = []string{
	"Commit failed: ",
	"Stash failed: ",
	"Stash pop failed: ",
	"Pull failed: ",
	"Push failed: ",
	"Force push failed: ",
	"Reset failed: ",
}

// flashAlertTitle decides whether a flash line is a failure the user must act
// on, and so also opens a dismissable alert (spec 01 §7.3 and §7.7). It returns
// the alert title and true when it should.
//
// Flash-only: info and ok lines, usage hints ("Usage: ..."), and invalid
// choices from cmdHint. A usage hint or an invalid choice is a mistake in how a
// command was typed; the flash line is enough for it, and an alert would make a
// typo look like a crash.
func flashAlertTitle(msg flashMsg) (string, bool) {
	if msg.kind != "err" || strings.TrimSpace(msg.text) == "" {
		return "", false
	}
	if strings.HasPrefix(msg.text, "Usage:") {
		return "", false
	}
	// A producer that marks the failure (cmdErr: a command the backend
	// reported) is trusted; the text table covers the git failures that are
	// not marked.
	if msg.alert || hasAnyPrefix(msg.text, gitFailurePrefixes) {
		return "Command failed", true
	}
	return "", false
}

// raiseFlashAlert opens the dismissable alert for a failed flash line. A
// repeat of an alert already on the stack is not added again.
func (m *Model) raiseFlashAlert(title, text string) {
	text = strings.TrimSpace(text)
	if m.errorAlertShowing(text) {
		return
	}
	m.showAlert(title, text)
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
