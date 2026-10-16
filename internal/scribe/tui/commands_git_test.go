package tui

import "testing"

func TestGitSubcommandsResolveToTheirOwnCommand(t *testing.T) {
	for line, want := range map[string]struct{ name, args string }{
		"/git":                {"git", ""},
		"/git status":         {"git status", ""},
		"/git stash pop":      {"git stash pop", ""},
		"/git stash":          {"git stash", ""},
		"/git commit fix bug": {"git commit", "fix bug"},
		"/git branch feat/x":  {"git branch", "feat/x"},
		"/git bogus":          {"git", "bogus"},
	} {
		c, args := findCommand(line)
		if c == nil || c.Name != want.name || args != want.args {
			got := "<nil>"
			if c != nil {
				got = c.Name
			}
			t.Errorf("%q resolved to (%s, %q), want (%s, %q)", line, got, args, want.name, want.args)
		}
	}
}

func TestGitBranchRejectsUnsafeNames(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	for _, bad := range []string{"a;rm -rf x", "$(id)", "two words"} {
		fm := flashOf(t, m.runCommand("/git branch "+bad))
		if fm.kind != "err" {
			t.Errorf("branch name %q was accepted: %+v", bad, fm)
		}
	}
}

func TestGitUnknownSubcommandSaysWhatIsAccepted(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	fm := flashOf(t, m.runCommand("/git bogus"))
	if fm.kind != "err" {
		t.Fatalf("/git bogus gave %+v, want an error", fm)
	}
}

func TestGitWithNoArgsOpensTheMenu(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.runCommand("/git")
	if len(m.modals) == 0 || m.modals[len(m.modals)-1].Title() == "" {
		t.Fatal("/git should open the git menu")
	}
	if _, ok := m.modals[len(m.modals)-1].(*gitMenu); !ok {
		t.Fatalf("top modal is %T, want the git menu", m.modals[len(m.modals)-1])
	}
}
