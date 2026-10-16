package tui

// /git subcommands (spec 02 section 2, Git family). Each one runs the same
// action as its row in the git menu, so the modal flows are shared. The
// three commands the menu does not have (init, branch, pull) run as user
// shell commands in the workdir, the same as "!git pull" would.

import (
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// gitBranchName is the subset of ref names /git branch accepts. It keeps the
// argument safe to hand to the shell.
var gitBranchName = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func init() {
	overrideCommands(&command{Name: "git", Desc: "Git menu: status, diff, commit, stash, log, push", Group: "Git",
		Run: func(m *Model, args string) tea.Cmd {
			if strings.TrimSpace(args) != "" {
				return cmdErrText("Unknown git command: " + args + ". Try /git status, diff, commit, stash, log, push, init, branch or pull")
			}
			m.openGitMenu()
			return nil
		}})

	registerCommands(
		&command{Name: "git status", Desc: "Changed files, staged and unstaged", Group: "Git",
			Run: func(m *Model, _ string) tea.Cmd { return m.gitMenuAction((*gitMenu).actStatus) }},
		&command{Name: "git diff", Desc: "Working tree diff", Group: "Git",
			Run: func(m *Model, _ string) tea.Cmd { return m.gitMenuAction((*gitMenu).actDiff) }},
		&command{Name: "git commit", Args: "[--staged] [message]", Desc: "Commit changes (suggests a message when none is given); --staged commits only what is staged", Group: "Git",
			Run: func(m *Model, args string) tea.Cmd {
				if msg, _ := parseCommitArgs(args); msg != "" {
					return m.commitCommand(args)
				}
				return m.gitMenuAction((*gitMenu).actCommit)
			}},
		&command{Name: "git stash", Desc: "Stash tracked changes", Group: "Git",
			Run: func(m *Model, _ string) tea.Cmd { return m.gitMenuAction((*gitMenu).actStash) }},
		&command{Name: "git stash pop", Desc: "Restore the latest stash", Group: "Git",
			Run: func(m *Model, _ string) tea.Cmd { return m.gitMenuAction((*gitMenu).actPop) }},
		&command{Name: "git log", Desc: "Last 30 commits", Group: "Git",
			Run: func(m *Model, _ string) tea.Cmd { return m.gitMenuAction((*gitMenu).actLog) }},
		&command{Name: "git push", Desc: "Push commits to the remote (asks first)", Group: "Git",
			Run: func(m *Model, _ string) tea.Cmd { return m.gitMenuAction((*gitMenu).actPush) }},
		&command{Name: "git init", Desc: "Run git init in the project", Group: "Git",
			Run: func(m *Model, _ string) tea.Cmd {
				return m.gitRepoCommand("git init", func(g GitRepoOps) (string, error) { return g.GitInit() })
			}},
		&command{Name: "git pull", Desc: "Run git pull in the project", Group: "Git",
			Run: func(m *Model, _ string) tea.Cmd {
				return m.gitRepoCommand("git pull", func(g GitRepoOps) (string, error) { return g.GitPull() })
			}},
		&command{Name: "git branch", Args: "[name]", Desc: "List branches, or create one named name", Group: "Git",
			Run: func(m *Model, args string) tea.Cmd {
				args = strings.TrimSpace(args)
				if args == "" {
					return m.runShell(m.cur(), "git branch")
				}
				if !gitBranchName.MatchString(args) {
					return cmdErrText("Branch names may only use letters, digits, '.', '_', '/' and '-'")
				}
				return m.gitRepoCommand("git branch "+args, func(g GitRepoOps) (string, error) {
					if err := g.GitBranch(args); err != nil {
						return "", err
					}
					return "Switched to a new branch " + args, nil
				})
			}},
	)
}

// gitMenuAction runs one git-menu action against the active tab. The menu
// itself is not opened; the action may push its own modal.
func (m *Model) gitMenuAction(act func(*gitMenu) (tea.Cmd, bool)) tea.Cmd {
	t := m.cur()
	cmd, _ := act(&gitMenu{m: m, be: t.be, th: m.th, st: t.be.Git()})
	return cmd
}
