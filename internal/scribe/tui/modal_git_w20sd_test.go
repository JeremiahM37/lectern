package tui

import (
	"slices"
	"testing"
)

// The destructive git actions all open through one gate. The two that discard
// work or rewrite history default to Cancel, so Enter does nothing.
func TestDangerousGitActionsDefaultToCancel(t *testing.T) {
	for _, tc := range []struct {
		key, title, run, command string
	}{
		{"f", "Force-push to remote?", "Force push", "git push --force-with-lease"},
		{"r", "Reset working tree?", "Reset", "git reset --hard HEAD"},
	} {
		m, rb := newGitModel(t, 80, 24)
		gitKey(m, "ctrl+g")
		gitKey(m, tc.key)
		cf, ok := m.topModal().(*Confirm)
		if !ok || !cf.Danger() {
			t.Fatalf("%s: want a danger confirm, top=%T", tc.key, m.topModal())
		}
		if cf.Title() != tc.title || cf.yesLbl != tc.run || cf.noLbl != "Cancel" {
			t.Fatalf("%s: confirm %q [%s | %s], want %q [%s | Cancel]",
				tc.key, cf.Title(), cf.yesLbl, cf.noLbl, tc.title, tc.run)
		}
		if cf.yes {
			t.Fatalf("%s: Cancel must be the default answer", tc.key)
		}
		gitKey(m, "enter")
		if slices.Contains(rb.runs, tc.command) {
			t.Fatalf("%s: Enter on the default answer ran %q", tc.key, tc.command)
		}
	}
}

// Stash and pop can be undone, so they stay a plain confirm with the verb as
// the button and Cancel as the other. Declining runs nothing.
func TestStashAndPopShareTheGate(t *testing.T) {
	for _, tc := range []struct {
		key, title, run, command, flash string
	}{
		{"s", "Stash changes?", "Stash", "git stash push", "Changes stashed."},
		{"o", "Pop the latest stash?", "Pop", "git stash pop", "Stash popped."},
	} {
		m, rb := newGitModel(t, 80, 24)
		gitKey(m, "ctrl+g")
		gitKey(m, tc.key)
		cf, ok := m.topModal().(*Confirm)
		if !ok || cf.Danger() {
			t.Fatalf("%s: want a plain confirm, top=%T", tc.key, m.topModal())
		}
		if cf.Title() != tc.title || cf.yesLbl != tc.run || cf.noLbl != "Cancel" {
			t.Fatalf("%s: confirm %q [%s | %s]", tc.key, cf.Title(), cf.yesLbl, cf.noLbl)
		}
		gitKey(m, "n")
		if slices.Contains(rb.runs, tc.command) {
			t.Fatalf("%s: declining ran %q", tc.key, tc.command)
		}
		gitKey(m, tc.key)
		gitKey(m, "y")
		if !slices.Contains(rb.runs, tc.command) || m.flash != tc.flash {
			t.Fatalf("%s: runs=%v flash=%q", tc.key, rb.runs, m.flash)
		}
	}
}

// The gate's set of dangerous actions is exactly force-push and reset.
// Stash, pop and push are not in it, and a change to that set should be a
// deliberate one.
func TestGitOpDangerSetIsForcePushAndReset(t *testing.T) {
	danger := map[string]bool{}
	for name, op := range map[string]gitOp{
		"stash": gitStash, "pop": gitPop, "force": gitForcePush, "reset": gitResetHard,
	} {
		if op.run == "" || op.title == "" || op.message == "" {
			t.Fatalf("%s is missing its title, message or button", name)
		}
		if op.danger {
			danger[name] = true
		}
	}
	if !danger["force"] || !danger["reset"] || danger["stash"] || danger["pop"] {
		t.Fatalf("danger set = %v, want force and reset only", danger)
	}
}

// The push confirm used to say "Yes, delete", the default for a danger
// confirm. It now says Push.
func TestPushConfirmSaysPushNotDelete(t *testing.T) {
	m, _ := newGitModel(t, 80, 24)
	gitKey(m, "ctrl+g")
	gitKey(m, "p")
	cf, ok := m.topModal().(*Confirm)
	if !ok {
		t.Fatalf("push should ask first, top=%T", m.topModal())
	}
	if cf.yesLbl != "Push" || cf.noLbl != "Cancel" {
		t.Fatalf("push buttons = [%s | %s], want [Push | Cancel]", cf.yesLbl, cf.noLbl)
	}
}
