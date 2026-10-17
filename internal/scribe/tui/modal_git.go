package tui

// Git menu (Ctrl+G, /git), status list, and the commit modal (/commit).
// Long work runs through asyncModal; see modal_async.go.

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(
		&command{Name: "commit", Args: "[message]", Desc: "Commit changes (suggests a message)", Group: "Git",
			Run: func(m *Model, args string) tea.Cmd {
				if args == "" {
					return m.openCommitModal()
				}
				return m.commitWithGuard(args)
			}},
	)
}

// porcelainCmd lists every changed path with its two-letter status code.
const porcelainCmd = "git status --porcelain=v1 --untracked-files=all"

// gitEntry is one line of `git status --porcelain`.
type gitEntry struct {
	X, Y byte // index and worktree status letters
	Path string
}

func (e gitEntry) untracked() bool { return e.X == '?' }
func (e gitEntry) staged() bool    { return e.X != ' ' && e.X != '?' }
func (e gitEntry) unstaged() bool  { return e.Y != ' ' && e.Y != '?' }

// code is the letter shown for the entry in a list.
func (e gitEntry) code() string {
	switch {
	case e.untracked():
		return "??"
	case e.X != ' ':
		return string(e.X)
	}
	return string(e.Y)
}

// parsePorcelain reads porcelain v1 output. It keeps leading spaces, which
// are part of the status codes, so the output must not be trimmed first.
func parsePorcelain(out string) []gitEntry {
	var es []gitEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		p := line[3:]
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		es = append(es, gitEntry{X: line[0], Y: line[1], Path: strings.Trim(p, "\"")})
	}
	return es
}

// statusColor colours a status letter. ?? uses the info colour.
func statusColor(th theme.Theme, c byte) lipgloss.TerminalColor {
	switch c {
	case 'A':
		return th.Success
	case 'M':
		return th.Warning
	case 'D', 'U':
		return th.Error
	case 'R', 'C', '?':
		return th.Info
	}
	return th.TextSecondary
}

// entryColor colours an entry by its primary code.
func entryColor(th theme.Theme, e gitEntry) lipgloss.TerminalColor {
	if e.untracked() {
		return th.Info
	}
	if e.X != ' ' {
		return statusColor(th, e.X)
	}
	return statusColor(th, e.Y)
}

// statusItems groups status entries for a Picker. Each path is a row whose
// value is the path, so Enter can open its diff.
func statusItems(th theme.Theme, es []gitEntry) []PickItem {
	var staged, unstaged, untracked []PickItem
	for _, e := range es {
		if e.untracked() {
			untracked = append(untracked, PickItem{Value: e.Path, Label: "?? " + e.Path, Color: th.Info})
			continue
		}
		if e.staged() {
			staged = append(staged, PickItem{Value: e.Path, Label: string(e.X) + "  " + e.Path, Color: statusColor(th, e.X)})
		}
		if e.unstaged() {
			unstaged = append(unstaged, PickItem{Value: e.Path, Label: string(e.Y) + "  " + e.Path, Color: statusColor(th, e.Y)})
		}
	}
	var out []PickItem
	for _, g := range []struct {
		name  string
		items []PickItem
	}{{"Staged", staged}, {"Unstaged", unstaged}, {"Untracked", untracked}} {
		for _, it := range g.items {
			it.Group = fmt.Sprintf("%s (%d)", g.name, len(g.items))
			out = append(out, it)
		}
	}
	return out
}

// secretFiles returns the paths that look like credentials.
func secretFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		base := path.Base(p)
		if strings.HasPrefix(base, ".env") || strings.HasPrefix(base, "id_rsa") ||
			strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
			out = append(out, p)
		}
	}
	return out
}

// readStatus runs porcelain status off the loop and hands back the entries
// or the reason they could not be read.
func readStatus(be Backend) (entries []gitEntry, reason string) {
	out, code, err := shellRun(be, porcelainCmd, 30*time.Second)
	switch {
	case err != nil:
		return nil, err.Error()
	case code != 0:
		return nil, outputLine(out)
	}
	return parsePorcelain(out), ""
}

// ---------------------------------------------------------------- git menu

// gitMenu is the Ctrl+G menu. Each action closes it, except the confirms
// that ask first.
type gitMenu struct {
	m    *Model
	be   Backend
	th   theme.Theme
	st   GitStatus
	acts []gitAction
	cur  int
}

// gitAction is one row of the git menu.
type gitAction struct {
	key, label, desc, icon string
	run                    func(g *gitMenu) (tea.Cmd, bool) // reports whether the menu closes
}

// openGitMenu is Ctrl+G and /git.
func (m *Model) openGitMenu() {
	t := m.cur()
	g := &gitMenu{m: m, be: t.be, th: m.th, st: t.be.Git()}
	g.acts = []gitAction{
		{"w", "Status", "changed files", "git", (*gitMenu).actStatus},
		{"d", "Diff", "working tree changes", "diff_add", (*gitMenu).actDiff},
		{"c", "Commit", "stage all and commit", "check", (*gitMenu).actCommit},
		{"p", "Push", "send commits to remote", "arrow_right", (*gitMenu).actPush},
		{"u", "Pull", "fetch and merge the remote", "arrow", (*gitMenu).actPull},
		{"s", "Stash", "stash tracked changes", "file", (*gitMenu).actStash},
		{"o", "Stash pop", "restore latest stash", "rewind", (*gitMenu).actPop},
		{"l", "Log", "last 30 commits", "clock", (*gitMenu).actLog},
		{"f", "Force push", "--force-with-lease", "warning", (*gitMenu).actForcePush},
		{"r", "Reset hard", "discard uncommitted work", "error", (*gitMenu).actResetHard},
		{"g", "Lazygit", "open lazygit here", "terminal", (*gitMenu).actLazygit},
	}
	m.pushModal(g)
}

func (g *gitMenu) branch() string {
	if g.st.Branch == "" {
		return "(no branch)"
	}
	return g.st.Branch
}

func (g *gitMenu) actStatus() (tea.Cmd, bool) {
	m, be, th := g.m, g.be, g.th
	p := NewPicker(th, "Git status", "git")
	p.Hint = "↑↓ move · Enter open diff · / filter · Esc close"
	p.Loading, p.Spin = true, "◌"
	p.Blurb = g.counts()
	p.Empty = "Working tree clean."
	p.OnSelect = func(it PickItem) tea.Cmd { return m.openWorkingDiff(it.Value) }
	m.pushModal(p)
	return asyncModal(p, func() func() (tea.Cmd, bool) {
		es, reason := readStatus(be)
		items := statusItems(th, es)
		return func() (tea.Cmd, bool) {
			p.Loading = false
			if reason != "" {
				p.Empty = "Could not read status: " + reason
			}
			p.SetItems(items)
			return nil, false
		}
	}), true
}

func (g *gitMenu) actDiff() (tea.Cmd, bool) { return g.m.openWorkingDiff(""), true }

func (g *gitMenu) actCommit() (tea.Cmd, bool) { return g.m.openCommitModal(), true }

func (g *gitMenu) actStash() (tea.Cmd, bool) {
	m, be := g.m, g.be
	m.pushModal(NewConfirm(g.th, "Stash changes?",
		"Stashes tracked changes and resets the working tree to HEAD. Untracked files stay where they are.", false,
		func() tea.Cmd {
			m.closeModal(g)
			m.setFlash("info", "Stashing…")
			return shellFlash(be, "git stash push", "Changes stashed.", "Stash failed")
		}))
	return nil, false
}

func (g *gitMenu) actPop() (tea.Cmd, bool) {
	m, be := g.m, g.be
	m.pushModal(NewConfirm(g.th, "Pop the latest stash?",
		"Applies the most recent stash to the working tree and drops it from the stash list.", false,
		func() tea.Cmd {
			m.closeModal(g)
			m.setFlash("info", "Popping the stash…")
			return shellFlash(be, "git stash pop", "Stash popped.", "Stash pop failed")
		}))
	return nil, false
}

func (g *gitMenu) actLog() (tea.Cmd, bool) {
	m, be, th := g.m, g.be, g.th
	iv := NewInfoView(th, "Git log", "git", []string{"Reading the log…"})
	m.pushModal(iv)
	return asyncModal(iv, func() func() (tea.Cmd, bool) {
		out, code, err := shellRun(be, "git log --oneline --decorate -30", 30*time.Second)
		var lines []string
		switch {
		case err != nil || (code != 0 && strings.TrimSpace(out) == ""):
			lines = []string{"Could not read the log."}
		case strings.TrimSpace(out) == "":
			lines = []string{"No commits found."}
		default:
			lines = strings.Split(strings.TrimRight(out, "\n"), "\n")
		}
		return func() (tea.Cmd, bool) {
			iv.lines, iv.off = lines, 0
			return nil, false
		}
	}), true
}

// actPull closes the menu and pulls. It needs no confirm: a pull only adds
// commits, and a conflict stops it with git's own message.
func (g *gitMenu) actPull() (tea.Cmd, bool) {
	m, be := g.m, g.be
	m.setFlash("info", "Pulling…")
	return shellFlash(be, "git pull", "Pull complete.", "Pull failed"), true
}

// actForcePush asks first, then runs push --force-with-lease, which refuses
// when the remote has commits this clone has not seen.
func (g *gitMenu) actForcePush() (tea.Cmd, bool) {
	m, be := g.m, g.be
	m.pushModal(NewConfirm(g.th, "Force-push to remote?",
		"git push --force-with-lease will overwrite the remote branch. Other contributors' commits may be discarded.", true,
		func() tea.Cmd {
			m.closeModal(g)
			m.setFlash("info", "Force-pushing (with lease)…")
			return shellFlash(be, "git push --force-with-lease", "Force push complete.", "Force push failed")
		}).WithLabels("Force push", "Cancel"))
	return nil, false
}

// actResetHard asks first. Untracked files are kept, as git reset --hard does.
func (g *gitMenu) actResetHard() (tea.Cmd, bool) {
	m, be := g.m, g.be
	m.pushModal(NewConfirm(g.th, "Reset working tree?",
		"git reset --hard HEAD will discard all uncommitted changes. Untracked files are kept.", true,
		func() tea.Cmd {
			m.closeModal(g)
			m.setFlash("info", "Resetting working tree…")
			return shellFlash(be, "git reset --hard HEAD", "Reset complete.", "Reset failed")
		}).WithLabels("Reset", "Cancel"))
	return nil, false
}

// lazygitMissing is the message when lazygit cannot be started.
const lazygitMissing = "Failed to launch lazygit. Is it installed?"

// actLazygit hands the terminal to lazygit and takes it back when it exits.
func (g *gitMenu) actLazygit() (tea.Cmd, bool) {
	if _, err := exec.LookPath("lazygit"); err != nil {
		return flashCmd("err", lazygitMissing), true
	}
	cmd := tea.ExecProcess(exec.Command("lazygit"), func(err error) tea.Msg {
		if err != nil {
			return flashMsg{kind: "err", text: lazygitMissing}
		}
		return nil
	})
	return cmd, true
}

func (g *gitMenu) actPush() (tea.Cmd, bool) {
	m, be, th := g.m, g.be, g.th
	branch := g.branch()
	return asyncModal(g, func() func() (tea.Cmd, bool) {
		upstream, code, err := shellRun(be, "git rev-parse --abbrev-ref --symbolic-full-name @{u}", 15*time.Second)
		up := ""
		if err == nil && code == 0 {
			up = strings.TrimSpace(upstream)
		}
		return func() (tea.Cmd, bool) {
			title, msg := "Push to remote?", "This branch has no upstream yet, so git push will fail unless one is configured."
			if up != "" {
				title = "Push to " + up + "?"
				msg = fmt.Sprintf("Sends your local commits on %s to %s.", branch, up)
			}
			m.pushModal(NewConfirm(th, title, msg, true, func() tea.Cmd {
				m.closeModal(g)
				m.setFlash("info", "Pushing…")
				return shellFlash(be, "git push", "Push complete.", "Push failed")
			}))
			return nil, false
		}
	}), false
}

// counts describes the working tree in one line: branch position and dirt.
func (g *gitMenu) counts() string {
	var parts []string
	if g.st.Ahead > 0 || g.st.Behind > 0 {
		parts = append(parts, fmt.Sprintf("↑%d ↓%d", g.st.Ahead, g.st.Behind))
	}
	if g.st.Dirty {
		parts = append(parts, fmt.Sprintf("%d changed", len(g.st.Changed)))
	} else {
		parts = append(parts, "clean")
	}
	return strings.Join(parts, " · ")
}

// Update implements Modal.
func (g *gitMenu) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return g, nil, false
	}
	key := km.String()
	switch key {
	case "esc":
		return g, nil, true
	case "up", "k":
		g.cur = (g.cur - 1 + len(g.acts)) % len(g.acts)
	case "down", "j":
		g.cur = (g.cur + 1) % len(g.acts)
	case "enter":
		return g.run(g.cur)
	default:
		for i, a := range g.acts {
			if a.key == key {
				g.cur = i
				return g.run(i)
			}
		}
	}
	return g, nil, false
}

func (g *gitMenu) run(i int) (Modal, tea.Cmd, bool) {
	cmd, done := g.acts[i].run(g)
	return g, cmd, done
}

// Title implements Modal.
func (g *gitMenu) Title() string { return "Git" }

// Icon implements Iconed.
func (g *gitMenu) Icon() string { return "git" }

// Footer implements Footered.
func (g *gitMenu) Footer() string {
	return "↑↓ move · Enter · c p u s o l g f r · Esc"
}

// Size implements Sized.
func (g *gitMenu) Size(cols, rows int) (int, int) {
	return min(60, max(56, cols/2)), 16
}

// View implements Modal.
func (g *gitMenu) View(w, h int) string {
	th := g.th
	blurb := "Common git actions"
	if g.st.Branch == "" {
		blurb = "Not a git repository."
	}
	lines := []string{
		spaceBetween(fgb(th.TextPrimary).Render(icons.Icon("git")+" "+g.branch()), fg(th.TextMuted).Render(g.counts()), w),
		fg(th.TextMuted).Render(blurb),
		"",
	}
	for i, a := range g.acts {
		mark, lab := "  ", fg(th.TextPrimary)
		if i == g.cur {
			mark, lab = fgb(th.BrandSecondary).Render("› "), fgb(th.TextPrimary)
		}
		line := mark + fgb(th.Brand).Render(a.key) + "  " + lab.Render(fmt.Sprintf("%-12s", a.label)) +
			fg(th.TextDim).Render(a.desc)
		lines = append(lines, render.Truncate(line, w))
	}
	return strings.Join(fitLines(lines, w, h), "\n")
}

// openWorkingDiff opens the working tree diff, selecting path when given.
func (m *Model) openWorkingDiff(path string) tea.Cmd {
	be := m.cur().be
	v := newDiffView(m, "Working tree diff", path)
	m.pushModal(v)
	return asyncModal(v, func() func() (tea.Cmd, bool) {
		text := be.GitDiff()
		return func() (tea.Cmd, bool) {
			v.load(text, "No changes in the working tree.")
			return nil, false
		}
	})
}

// ---------------------------------------------------------------- commit

// commitModal writes a message, shows what will be committed, and commits.
// lines[0] is the subject; lines[1:] are the body.
type commitModal struct {
	m          *Model
	be         Backend
	th         theme.Theme
	lines      []string
	row, col   int
	files      []gitEntry
	secrets    []string
	loading    bool
	busy       string // non-empty while a background job runs; keys are held
	suggesting bool
	err        string
	cols, rows int
	width      int // the frame width Size chose; the footer follows it
	// stageAll is the Ctrl+A toggle. On by default, which is what a commit
	// has always done (git add -A). Off commits only what is already staged,
	// unless nothing is staged, in which case everything is staged anyway.
	stageAll bool
}

// stageCommitter is implemented by a Backend that can choose whether a commit
// stages every change first. Without it the commit always stages everything.
type stageCommitter interface {
	GitCommitWith(message string, stageAll bool) error
}

// canToggleStage reports whether the stage-all choice matters: some change is
// unstaged or untracked. When everything is already staged the toggle is hidden.
func (c *commitModal) canToggleStage() bool {
	for _, e := range c.files {
		if e.untracked() || e.unstaged() {
			return true
		}
	}
	return false
}

// anyStaged reports whether something is already in the index.
func (c *commitModal) anyStaged() bool {
	for _, e := range c.files {
		if !e.untracked() && e.staged() {
			return true
		}
	}
	return false
}

// stagesAll is the rule the commit follows: stage all when the toggle is on
// or nothing is staged yet, otherwise commit the staged set alone.
func (c *commitModal) stagesAll() bool { return c.stageAll || !c.anyStaged() }

// openCommitModal opens the commit modal and reads the changed files.
func (m *Model) openCommitModal() tea.Cmd {
	c := &commitModal{m: m, be: m.cur().be, th: m.th, lines: []string{""}, loading: true, stageAll: true}
	m.pushModal(c)
	be := c.be
	return asyncModal(c, func() func() (tea.Cmd, bool) {
		es, reason := readStatus(be)
		return func() (tea.Cmd, bool) {
			c.loading = false
			c.files = es
			c.err = reason
			paths := make([]string, 0, len(es))
			for _, e := range es {
				paths = append(paths, e.Path)
			}
			c.secrets = secretFiles(paths)
			return nil, false
		}
	})
}

// commitWithGuard handles /commit <message>, asking first when a changed file
// looks like a credential.
func (m *Model) commitWithGuard(msg string) tea.Cmd {
	secrets := secretFiles(m.cur().be.Git().Changed)
	if len(secrets) == 0 {
		return m.gitCommit(msg)
	}
	m.pushModal(secretConfirm(m.th, secrets, func() tea.Cmd { return m.gitCommit(msg) }))
	return nil
}

// secretConfirm asks before committing files that look like credentials.
func secretConfirm(th theme.Theme, secrets []string, onYes func() tea.Cmd) *listConfirm {
	return newListConfirm(th, "Commit files that look like secrets?",
		"These files look like credentials. Committing stages them with everything else, and they stay in the git history.",
		secrets, true, onYes)
}

// composeMessage joins the subject and the trimmed body into a commit message.
func composeMessage(lines []string) string {
	subject := strings.TrimSpace(lines[0])
	var body []string
	for _, l := range lines[1:] {
		body = append(body, strings.TrimRight(l, " \t"))
	}
	text := strings.TrimSpace(strings.Join(body, "\n"))
	if text == "" {
		return subject
	}
	return subject + "\n\n" + text
}

// setMessage fills the editor from a suggested message.
func (c *commitModal) setMessage(text string) {
	subject, body, _ := strings.Cut(strings.TrimSpace(text), "\n")
	c.lines = append([]string{strings.TrimSpace(subject)}, strings.Split(strings.TrimSpace(body), "\n")...)
	c.row = 0
	c.col = len([]rune(c.lines[0]))
}

// suggest asks the model for a message, filling the editor when it answers.
func (c *commitModal) suggest() tea.Cmd {
	if c.loading {
		c.err = "Still reading the changed files."
		return nil
	}
	c.busy, c.suggesting, c.err = "Suggesting a message…", true, ""
	be := c.be
	return asyncModal(c, func() func() (tea.Cmd, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		sugg, err := be.SuggestCommitMessage(ctx)
		return func() (tea.Cmd, bool) {
			c.busy, c.suggesting = "", false
			if err != nil {
				c.err = "No suggestion: " + err.Error()
				return nil, false
			}
			c.setMessage(sugg)
			return nil, false
		}
	})
}

// tryCommit validates the message, then commits or asks about secret files.
func (c *commitModal) tryCommit() tea.Cmd {
	if c.loading {
		c.err = "Still reading the changed files."
		return nil
	}
	subject := strings.TrimSpace(c.lines[0])
	if subject == "" {
		c.row, c.col = 0, 0
		c.err = "Commit message cannot be empty."
		return nil
	}
	if len(c.files) == 0 {
		c.err = "No changes to commit."
		return nil
	}
	msg := composeMessage(c.lines)
	if len(c.secrets) > 0 {
		c.m.pushModal(secretConfirm(c.th, c.secrets, func() tea.Cmd { return c.commit(msg, subject) }))
		return nil
	}
	return c.commit(msg, subject)
}

// commit runs GitCommit in the background. The modal closes on success and
// shows the error inline on failure.
func (c *commitModal) commit(msg, subject string) tea.Cmd {
	c.busy, c.err = "Committing…", ""
	be, m := c.be, c.m
	stagesAll := c.stagesAll()
	return asyncModal(c, func() func() (tea.Cmd, bool) {
		var err error
		if sc, ok := be.(stageCommitter); ok {
			err = sc.GitCommitWith(msg, stagesAll)
		} else {
			err = be.GitCommit(msg)
		}
		hash := ""
		if err == nil {
			hash = headShort(be)
		}
		return func() (tea.Cmd, bool) {
			if err != nil {
				c.busy = ""
				c.err = "Commit failed: " + err.Error()
				return nil, false
			}
			text := "Committed: " + subject
			if hash != "" {
				text = "Committed " + hash + ": " + subject
			}
			m.setFlash("ok", text)
			return nil, true
		}
	})
}

// headShort is the abbreviated hash of HEAD, or "" when git does not give one.
// It runs off the UI goroutine, after a commit, so the flash can name it.
func headShort(be Backend) string {
	out, code, err := shellRun(be, "git rev-parse --short HEAD", 2*time.Second)
	if err != nil || code != 0 {
		return ""
	}
	h := strings.TrimSpace(out)
	if len(h) < 4 || len(h) > 12 {
		return ""
	}
	for _, r := range h {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return ""
		}
	}
	return h
}

// Update implements Modal.
func (c *commitModal) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return c, nil, false
	}
	key := km.String()
	if c.busy != "" {
		// Esc abandons a suggestion; its result is then dropped. Nothing
		// else can be interrupted while the commit is running.
		if key == "esc" && c.suggesting {
			return c, nil, true
		}
		return c, nil, false
	}
	switch key {
	case "esc":
		return c, nil, true
	case "ctrl+g":
		return c, c.suggest(), false
	case "ctrl+s":
		return c, c.tryCommit(), false
	case "enter":
		if c.row == 0 {
			c.toBody()
		} else {
			c.newline()
		}
	case "ctrl+a":
		if c.canToggleStage() {
			c.stageAll = !c.stageAll
		}
	case "tab":
		c.toBody()
	case "shift+tab":
		c.row, c.col = 0, min(c.col, len([]rune(c.lines[0])))
	case "backspace":
		c.backspace()
	case "left":
		if c.col > 0 {
			c.col--
		} else if c.row > 0 {
			c.row--
			c.col = len([]rune(c.lines[c.row]))
		}
	case "right":
		if c.col < len([]rune(c.lines[c.row])) {
			c.col++
		} else if c.row < len(c.lines)-1 {
			c.row++
			c.col = 0
		}
	case "up":
		if c.row > 0 {
			c.row--
			c.col = min(c.col, len([]rune(c.lines[c.row])))
		}
	case "down":
		if c.row < len(c.lines)-1 {
			c.row++
			c.col = min(c.col, len([]rune(c.lines[c.row])))
		}
	case "home":
		c.col = 0
	case "end":
		c.col = len([]rune(c.lines[c.row]))
	default:
		if s := printable(km); s != "" {
			c.insert(s)
		}
	}
	return c, nil, false
}

// toBody moves the cursor to the start of the body, creating it if needed.
func (c *commitModal) toBody() {
	if len(c.lines) < 2 {
		c.lines = append(c.lines, "")
	}
	c.row, c.col = 1, 0
}

func (c *commitModal) insert(s string) {
	r := []rune(c.lines[c.row])
	ins := []rune(s)
	out := make([]rune, 0, len(r)+len(ins))
	out = append(out, r[:c.col]...)
	out = append(out, ins...)
	out = append(out, r[c.col:]...)
	c.lines[c.row] = string(out)
	c.col += len(ins)
	c.err = ""
}

func (c *commitModal) newline() {
	r := []rune(c.lines[c.row])
	head, tail := string(r[:c.col]), string(r[c.col:])
	c.lines[c.row] = head
	rest := append([]string{tail}, c.lines[c.row+1:]...)
	c.lines = append(c.lines[:c.row+1], rest...)
	c.row++
	c.col = 0
}

func (c *commitModal) backspace() {
	switch {
	case c.col > 0:
		r := []rune(c.lines[c.row])
		c.lines[c.row] = string(append(r[:c.col-1], r[c.col:]...))
		c.col--
	case c.row > 0:
		prev := []rune(c.lines[c.row-1])
		c.col = len(prev)
		c.lines[c.row-1] = string(prev) + c.lines[c.row]
		c.lines = append(c.lines[:c.row], c.lines[c.row+1:]...)
		c.row--
	}
	c.err = ""
}

// Title implements Modal.
func (c *commitModal) Title() string { return "Git commit" }

// Icon implements Iconed.
func (c *commitModal) Icon() string { return "git" }

// Footer implements Footered.
func (c *commitModal) Footer() string {
	// A wide frame shows every hint. A narrow one keeps the commands and Esc
	// and drops the newline and body hints (Enter and Tab are the usual keys),
	// so the cancel hint is never cut off.
	if c.width >= 96 {
		parts := []string{"Enter new line", "Tab body", "Ctrl+S commit", "Ctrl+G suggest", "Esc cancel"}
		if c.canToggleStage() {
			parts = append(parts[:2], append([]string{"Ctrl+A stage all"}, parts[2:]...)...)
		}
		return strings.Join(parts, " · ")
	}
	parts := []string{"Ctrl+S commit", "Ctrl+G suggest", "Esc cancel"}
	if c.canToggleStage() {
		parts = append(parts[:1], append([]string{"Ctrl+A stage"}, parts[1:]...)...)
	}
	return strings.Join(parts, " · ")
}

// Size implements Sized.
func (c *commitModal) Size(cols, rows int) (int, int) {
	c.cols, c.rows = cols, rows
	c.width = min(96, max(56, cols*8/10))
	return c.width, min(rows-4, 22)
}

// summary is the first line: what the commit will contain.
func (c *commitModal) summary() (string, bool) {
	th := c.th
	switch {
	case c.loading:
		return fg(th.TextMuted).Render(spinner() + " Reading the changes…"), false
	case len(c.files) == 0:
		return fg(th.TextMuted).Render("No changes to commit."), false
	}
	// Counts of non-zero kinds only. A file can be both staged and modified.
	var staged, modified, untracked int
	for _, e := range c.files {
		switch {
		case e.untracked():
			untracked++
		default:
			if e.staged() {
				staged++
			}
			if e.unstaged() {
				modified++
			}
		}
	}
	var parts []string
	if staged > 0 {
		parts = append(parts, fg(th.Success).Render(fmt.Sprintf("● %d staged", staged)))
	}
	if modified > 0 {
		parts = append(parts, fg(th.Warning).Render(fmt.Sprintf("● %d modified", modified)))
	}
	if untracked > 0 {
		parts = append(parts, fg(th.Error).Render(fmt.Sprintf("● %d untracked", untracked)))
	}
	parts = append(parts, fg(th.TextMuted).Render("git add -A stages them all"))
	return strings.Join(parts, "  "), true
}

// View implements Modal.
func (c *commitModal) View(w, h int) string {
	th := c.th
	var out []string
	sum, _ := c.summary()
	out = append(out, render.Truncate(sum, w))

	secretRows := 0
	if len(c.secrets) > 0 {
		secretRows = 1
	}
	edH := clamp(len(c.lines), 3, 6)
	budget := h - 4 - secretRows - edH // summary, blank, label, status
	if budget > 0 && len(c.files) > 0 {
		show := c.files
		more := 0
		if len(show) > budget {
			more = len(show) - (budget - 1)
			show = show[:budget-1]
		}
		for _, e := range show {
			line := "  " + fgb(entryColor(th, e)).Render(fmt.Sprintf("%-2s", e.code())) + "  " + fg(th.TextSecondary).Render(e.Path)
			out = append(out, render.Truncate(line, w))
		}
		if more > 0 {
			out = append(out, fg(th.TextFaint).Render(fmt.Sprintf("  … and %d more", more)))
		}
	}
	if secretRows == 1 {
		out = append(out, render.Truncate(fg(th.Warning).Render("⚠ looks like a secret: "+strings.Join(c.secrets, ", ")+" · Ctrl+S asks first"), w))
	}
	if c.canToggleStage() {
		out = append(out, render.Truncate(c.stageLine(), w))
	}
	out = append(out, "", fg(th.TextMuted).Render("Message:"))

	start := 0
	if c.row >= edH {
		start = c.row - edH + 1
	}
	for i := start; i < start+edH; i++ {
		if i >= len(c.lines) {
			out = append(out, "")
			continue
		}
		out = append(out, render.Truncate(c.editorLine(i, w), w))
	}

	switch {
	case c.busy != "":
		out = append(out, fg(th.BrandSecondary).Render(spinner()+" "+c.busy))
	case c.err != "":
		out = append(out, render.Truncate(fg(th.Error).Render(c.err), w))
	default:
		out = append(out, "")
	}
	return strings.Join(fitLines(out, w, h), "\n")
}

// stageLine draws the stage-all toggle and what it means right now.
func (c *commitModal) stageLine() string {
	th := c.th
	box := "[ ]"
	if c.stageAll {
		box = "[x]"
	}
	line := fg(th.TextSecondary).Render(box+" Stage all changes") + fg(th.TextFaint).Render("  Ctrl+A")
	switch {
	case c.stagesAll() && !c.stageAll:
		line += fg(th.TextMuted).Render("  · nothing is staged yet, so everything is")
	case !c.stageAll:
		line += fg(th.TextMuted).Render("  · only the staged files are committed")
	}
	return line
}

// editorLine draws line i of the message with the cursor when it is focused.
func (c *commitModal) editorLine(i, w int) string {
	th := c.th
	text := c.lines[i]
	focused := i == c.row
	prefix := "  "
	base := fg(th.TextSecondary)
	if i == 0 {
		base = fg(th.TextPrimary)
	}
	if focused {
		prefix = fg(th.Brand).Render("› ")
	}
	if text == "" && i == 0 && len(c.lines) == 1 {
		if focused {
			return prefix + fg(th.Brand).Render("▌") + fg(th.TextDim).Render("subject: describe the change")
		}
		return prefix + fg(th.TextDim).Render("subject: describe the change")
	}
	if !focused {
		return prefix + base.Render(text)
	}
	r := []rune(text)
	col := min(c.col, len(r))
	return prefix + base.Render(string(r[:col])) + fg(th.Brand).Render("▌") + base.Render(string(r[col:]))
}
