package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// gitRec is a FakeBackend that records the git, checkpoint and job calls
// the modals make, and serves the data they read.
type gitRec struct {
	*FakeBackend
	status   string // porcelain output; "" uses the fake's default
	changed  []string
	diff     string
	commitEr error
	commits  []string
	pushes   []string
	restored []int
	undone   int
	killed   []string
	cps      []CheckpointInfo
	jobs     []Job
}

func (r *gitRec) Shell(ctx context.Context, command string) (string, int, error) {
	if r.status != "" && strings.HasPrefix(command, "git status --porcelain") {
		return r.status, 0, nil
	}
	if strings.HasPrefix(command, "git push") {
		r.pushes = append(r.pushes, command)
	}
	return r.FakeBackend.Shell(ctx, command)
}

func (r *gitRec) Git() GitStatus {
	return GitStatus{Branch: "feature/parser", Dirty: true, Ahead: 1, Changed: r.changed}
}

func (r *gitRec) GitDiff() string { return r.diff }

func (r *gitRec) GitCommit(message string) error {
	if r.commitEr != nil {
		return r.commitEr
	}
	r.commits = append(r.commits, message)
	return nil
}

func (r *gitRec) SuggestCommitMessage(ctx context.Context) (string, error) {
	return "parser: skip empty list items\n\n- keep the parser moving", nil
}

func (r *gitRec) Checkpoints() []CheckpointInfo {
	return append([]CheckpointInfo(nil), r.cps...)
}

func (r *gitRec) Undo() (CheckpointInfo, error) {
	for i := len(r.cps) - 1; i >= 0; i-- {
		if !r.cps[i].Undone {
			r.cps[i].Undone = true
			r.undone++
			return r.cps[i], nil
		}
	}
	return CheckpointInfo{}, gitErrNone
}

func (r *gitRec) Redo() (CheckpointInfo, error) {
	for i := range r.cps {
		if r.cps[i].Undone {
			r.cps[i].Undone = false
			return r.cps[i], nil
		}
	}
	return CheckpointInfo{}, gitErrNone
}

func (r *gitRec) RestoreTo(turn int) error {
	r.restored = append(r.restored, turn)
	return nil
}

func (r *gitRec) TurnDiff(turn int) string {
	return "--- a/x.go\n+++ b/x.go\n@@ -1 +1,2 @@\n-old\n+new\n+more\n"
}

func (r *gitRec) Jobs() []Job { return append([]Job(nil), r.jobs...) }

func (r *gitRec) KillJob(id string) error {
	r.killed = append(r.killed, id)
	return nil
}

var gitErrNone = gitErrText("nothing to do")

type gitErrText string

func (e gitErrText) Error() string { return string(e) }

// newGitModel builds a Model around a gitRec at w x h.
func newGitModel(t *testing.T, w, h int) (*Model, *gitRec) {
	t.Helper()
	rb := &gitRec{FakeBackend: NewFakeBackend(), changed: []string{"internal/parser/parse.go"}}
	rb.Delay = 0
	m := NewModel(rb, func() (Backend, error) {
		n := NewFakeBackend()
		n.Delay = 0
		return n, nil
	}, Options{Width: w, Height: h, Theme: "scribe"})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	settle(m)
	m.phase = phaseChat // the chat screen, as after the first input
	return m, rb
}

// pump runs a command and feeds its message back, until nothing is left.
func gitPump(m *Model, cmd tea.Cmd) {
	for i := 0; cmd != nil && i < 10; i++ {
		msg := cmd()
		_, cmd = m.Update(msg)
	}
}

// gitKey sends a key and runs whatever command it returned.
func gitKey(m *Model, name string) {
	gitPump(m, send(m, name))
}

// checkGitWidths fails when any line of the screen is not exactly w cells wide.
func checkGitWidths(t *testing.T, m *Model, w int) {
	t.Helper()
	for i, line := range strings.Split(m.View(), "\n") {
		if got := ansi.StringWidth(line); got != w {
			t.Fatalf("line %d is %d cells, want %d: %q", i, got, w, ansi.Strip(line))
		}
	}
}

func TestGitMenuNavigation(t *testing.T) {
	m, _ := newGitModel(t, 80, 24)
	send(m, "ctrl+g")
	g, ok := m.topModal().(*gitMenu)
	if !ok {
		t.Fatalf("ctrl+g should open the git menu, top is %T", m.topModal())
	}
	gitKey(m, "up")
	if g.cur != len(g.acts)-1 {
		t.Fatalf("up from the first row should wrap to the last, cur=%d", g.cur)
	}
	gitKey(m, "down")
	gitKey(m, "j")
	if g.cur != 1 {
		t.Fatalf("j should move down, cur=%d", g.cur)
	}
	gitKey(m, "k")
	if g.cur != 0 {
		t.Fatalf("k should move up, cur=%d", g.cur)
	}
	gitKey(m, "esc")
	if m.topModal() != nil {
		t.Fatalf("esc should close the git menu")
	}
}

func TestGitMenuLogShowsCommits(t *testing.T) {
	m, _ := newGitModel(t, 80, 24)
	send(m, "ctrl+g")
	gitKey(m, "l")
	iv, ok := m.topModal().(*InfoView)
	if !ok {
		t.Fatalf("l should open the log, top is %T", m.topModal())
	}
	if !strings.Contains(strings.Join(iv.lines, "\n"), "parser: skip empty list items") {
		t.Fatalf("log should show the commits, got %q", iv.lines)
	}
}

func TestStatusGroupsEntries(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	rb.status = " M internal/parser/parse.go\nM  docs/parser.md\n?? notes/scratch.txt\nMM both.go\n"
	send(m, "ctrl+g")
	gitKey(m, "s")
	p, ok := m.topModal().(*Picker)
	if !ok {
		t.Fatalf("s should open the status list, top is %T", m.topModal())
	}
	groups := map[string]int{}
	for _, it := range p.Items {
		groups[it.Group]++
	}
	want := map[string]int{"Staged (2)": 2, "Unstaged (2)": 2, "Untracked (1)": 1}
	for g, n := range want {
		if groups[g] != n {
			t.Fatalf("group %q has %d rows, want %d (all: %v)", g, groups[g], n, groups)
		}
	}
	gitKey(m, "enter")
	if _, ok := m.topModal().(*diffView); !ok {
		t.Fatalf("enter on a status row should open its diff, top is %T", m.topModal())
	}
}

func TestCommitFlowComposesMessage(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	send(m, "ctrl+g")
	gitKey(m, "c")
	c, ok := m.topModal().(*commitModal)
	if !ok {
		t.Fatalf("c should open the commit modal, top is %T", m.topModal())
	}
	if len(c.files) != 4 {
		t.Fatalf("commit should list the 4 changed files, got %d", len(c.files))
	}
	typeText(m, "parser: skip empty items")
	send(m, "enter")
	typeText(m, "first line")
	gitKey(m, "ctrl+s")
	if len(rb.commits) != 1 {
		t.Fatalf("expected one GitCommit call, got %d", len(rb.commits))
	}
	if want := "parser: skip empty items\n\nfirst line"; rb.commits[0] != want {
		t.Fatalf("GitCommit got %q, want %q", rb.commits[0], want)
	}
	if m.topModal() != nil {
		t.Fatalf("a successful commit should close the modal")
	}
}

func TestCommitRejectsEmptySubject(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	send(m, "ctrl+g")
	gitKey(m, "c")
	gitKey(m, "ctrl+s")
	c := m.topModal().(*commitModal)
	if c.err != "Commit message cannot be empty." {
		t.Fatalf("empty subject should be refused inline, err=%q", c.err)
	}
	if len(rb.commits) != 0 {
		t.Fatalf("nothing should be committed")
	}
}

func TestCommitSuggestFillsMessage(t *testing.T) {
	m, _ := newGitModel(t, 80, 24)
	send(m, "ctrl+g")
	gitKey(m, "c")
	gitKey(m, "ctrl+g")
	c := m.topModal().(*commitModal)
	if c.lines[0] != "parser: skip empty list items" {
		t.Fatalf("suggestion should fill the subject, got %q", c.lines[0])
	}
	if !strings.Contains(strings.Join(c.lines, "\n"), "- keep the parser moving") {
		t.Fatalf("suggestion body should be filled, got %q", c.lines)
	}
}

func TestSecretGuardAsksBeforeCommit(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	rb.status = " M .env.local\n M main.go\n"
	rb.changed = []string{".env.local", "main.go"}
	send(m, "ctrl+g")
	gitKey(m, "c")
	typeText(m, "add config")
	gitKey(m, "ctrl+s")
	if _, ok := m.topModal().(*listConfirm); !ok {
		t.Fatalf("a .env file should trigger the secret confirm, top is %T", m.topModal())
	}
	gitKey(m, "n")
	if _, ok := m.topModal().(*commitModal); !ok || len(rb.commits) != 0 {
		t.Fatalf("declining should keep the modal open and commit nothing")
	}
	gitKey(m, "ctrl+s")
	gitKey(m, "y")
	if len(rb.commits) != 1 {
		t.Fatalf("confirming should commit, got %d commits", len(rb.commits))
	}
}

func TestSlashCommitGuardsSecrets(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	rb.changed = []string{"server.pem"}
	m.runCommand("/commit fix auth")
	if _, ok := m.topModal().(*listConfirm); !ok {
		t.Fatalf("/commit with a .pem file should ask, top is %T", m.topModal())
	}
	gitKey(m, "y")
	if len(rb.commits) != 1 || rb.commits[0] != "fix auth" {
		t.Fatalf("confirmed /commit should commit the message, got %q", rb.commits)
	}
}

func TestDiffViewerSwitchesFiles(t *testing.T) {
	m, rb := newGitModel(t, 140, 40)
	rb.diff = "diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n-old a\n+new a\n keep\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1,2 @@\n-x\n+y\n+z\n"
	gitPump(m, m.runCommand("/diff"))
	v, ok := m.topModal().(*diffView)
	if !ok {
		t.Fatalf("/diff should open the viewer, top is %T", m.topModal())
	}
	if len(v.files) != 2 || v.files[0].Path != "a.go" || v.files[1].Path != "b.go" {
		t.Fatalf("files parsed wrong: %+v", v.files)
	}
	if v.files[1].Adds != 2 || v.files[1].Dels != 1 {
		t.Fatalf("b.go counts wrong: +%d -%d", v.files[1].Adds, v.files[1].Dels)
	}
	gitKey(m, "n")
	if v.cur != 1 {
		t.Fatalf("n should go to the next file, cur=%d", v.cur)
	}
	gitKey(m, "p")
	gitKey(m, "p")
	if v.cur != 1 {
		t.Fatalf("p should wrap to the last file, cur=%d", v.cur)
	}
	gitKey(m, "s")
	if !v.sbs {
		t.Fatalf("s should switch to side-by-side when wide")
	}
}

func TestCheckpointRestoreCallsRestoreTo(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	now := time.Now()
	rb.cps = []CheckpointInfo{
		{Turn: 1, Prompt: "add parser", Files: []string{"a.go"}, Created: now},
		{Turn: 2, Prompt: "fix lexer", Files: []string{"b.go", "c.go"}, Created: now},
		{Turn: 3, Prompt: "docs", Files: []string{"d.md"}, Created: now},
	}
	m.runCommand("/checkpoints")
	b, ok := m.topModal().(*cpBrowser)
	if !ok {
		t.Fatalf("/checkpoints should open the browser, top is %T", m.topModal())
	}
	if b.cur != 2 {
		t.Fatalf("cursor should start on the latest turn, cur=%d", b.cur)
	}
	gitKey(m, "up")
	gitKey(m, "enter")
	if _, ok := m.topModal().(*listConfirm); !ok {
		t.Fatalf("enter should ask before restoring, top is %T", m.topModal())
	}
	gitKey(m, "y")
	if len(rb.restored) != 1 || rb.restored[0] != 2 {
		t.Fatalf("RestoreTo should be called with turn 2, got %v", rb.restored)
	}
}

func TestCommitModalDropsResultWhenClosed(t *testing.T) {
	m, _ := newGitModel(t, 80, 24)
	send(m, "ctrl+g")
	loadCmd := send(m, "c") // the file read starts; not run yet
	gitKey(m, "esc")        // the modal closes before the read finishes
	gitPump(m, loadCmd)     // its result arrives late and must be dropped
	if m.topModal() != nil {
		t.Fatalf("a late result must not reopen or leave a modal")
	}
}

func TestUndoConfirmListsFilesAndReportsInChat(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	now := time.Now()
	rb.cps = []CheckpointInfo{
		{Turn: 2, Prompt: "fix lexer", Files: []string{"b.go"}, Created: now},
		{Turn: 3, Prompt: "docs", Files: []string{"d.md", "e.md"}, Created: now},
	}
	send(m, "ctrl+z")
	lc, ok := m.topModal().(*listConfirm)
	if !ok {
		t.Fatalf("ctrl+z should ask first, top is %T", m.topModal())
	}
	if !strings.Contains(lc.Confirm.message, "d.md") || !strings.Contains(lc.Confirm.message, "e.md") {
		t.Fatalf("confirm should list the files that will be reverted: %q", lc.Confirm.message)
	}
	gitKey(m, "y")
	if rb.undone != 1 {
		t.Fatalf("expected one undo, got %d", rb.undone)
	}
	// system rows type themselves out; advance the chat clock past it
	m.Update(tickMsg(time.Now().Add(10 * time.Second)))
	if !strings.Contains(ansi.Strip(m.View()), "Undid turn 3: 2 files restored.") {
		t.Fatalf("the result should appear as a chat message; screen:\n%s", ansi.Strip(m.View()))
	}
}

func TestJobsOutputAndKill(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	rb.jobs = []Job{
		{ID: "j1", Command: "go test ./...", Status: "running", Started: time.Now().Add(-40 * time.Second), Tail: []string{"ok example/lexer"}},
		{ID: "j2", Command: "make", Status: "exited", ExitCode: 2},
	}
	m.runCommand("/jobs")
	if _, ok := m.topModal().(*jobsPanel); !ok {
		t.Fatalf("/jobs should open the panel, top is %T", m.topModal())
	}
	gitKey(m, "enter")
	iv, ok := m.topModal().(*InfoView)
	if !ok || !strings.Contains(strings.Join(iv.lines, "\n"), "ok example/lexer") {
		t.Fatalf("enter should show the job's output, top is %T", m.topModal())
	}
	gitKey(m, "esc")
	gitKey(m, "k")
	if _, ok := m.topModal().(*Confirm); !ok {
		t.Fatalf("k on a running job should ask first, top is %T", m.topModal())
	}
	gitKey(m, "y")
	if len(rb.killed) != 1 || rb.killed[0] != "j1" {
		t.Fatalf("expected j1 to be killed, got %v", rb.killed)
	}
}

func TestPushIsDangerConfirmThenRuns(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	send(m, "ctrl+g")
	gitKey(m, "p")
	cf, ok := m.topModal().(*Confirm)
	if !ok || !cf.Danger() {
		t.Fatalf("push should ask with a danger confirm, top is %T", m.topModal())
	}
	if cf.Title() != "Push to origin/feature/parser?" {
		t.Fatalf("confirm title = %q", cf.Title())
	}
	gitKey(m, "y")
	if len(rb.pushes) != 1 || rb.pushes[0] != "git push" {
		t.Fatalf("push should run git push, got %v", rb.pushes)
	}
	if m.flash != "Push complete." {
		t.Fatalf("flash = %q", m.flash)
	}
}

func TestEmptyStates(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	rb.cps = nil
	m.runCommand("/checkpoints")
	if !strings.Contains(ansi.Strip(m.View()), "No checkpoints yet") {
		t.Fatalf("empty checkpoint list should say so")
	}
	gitKey(m, "esc")
	m.runCommand("/jobs")
	if !strings.Contains(ansi.Strip(m.View()), "No background jobs") {
		t.Fatalf("empty job list should say so")
	}
}

func TestSplitDiffCheckpointStyle(t *testing.T) {
	files := splitDiff("--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-c\n+d\n")
	if len(files) != 2 || files[0].Path != "x" || files[1].Path != "y" {
		t.Fatalf("checkpoint diffs should split per file, got %+v", files)
	}
	if files[0].Adds != 1 || files[0].Dels != 1 {
		t.Fatalf("counts wrong: %+v", files[0])
	}
}

func TestEveryScreenFillsTerminalWidth(t *testing.T) {
	screens := map[string]func(m *Model){
		"git menu": func(m *Model) { send(m, "ctrl+g") },
		"commit": func(m *Model) {
			send(m, "ctrl+g")
			gitKey(m, "c")
			typeText(m, "subject that is quite long to check that it is cut at the edge")
		},
		"diff": func(m *Model) {
			m.cur().be.(*gitRec).diff = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-a very long line that will need wrapping because it keeps going and going\n+b\n"
			gitPump(m, m.runCommand("/diff"))
		},
		"diff side by side": func(m *Model) {
			m.cur().be.(*gitRec).diff = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n-a very long line that will need wrapping because it keeps going and going\n+b\n keep\n"
			gitPump(m, m.runCommand("/diff"))
			gitKey(m, "s")
		},
		"checkpoints": func(m *Model) {
			m.cur().be.(*gitRec).cps = []CheckpointInfo{{Turn: 1, Prompt: strings.Repeat("long prompt ", 12), Files: []string{"a.go"}, Created: time.Now()}}
			m.runCommand("/checkpoints")
		},
		"jobs": func(m *Model) { m.runCommand("/jobs") },
		"undo": func(m *Model) {
			m.cur().be.(*gitRec).cps = []CheckpointInfo{{Turn: 1, Files: []string{"a.go"}}}
			send(m, "ctrl+z")
		},
	}
	for _, size := range [][2]int{{80, 24}, {140, 40}} {
		for name, open := range screens {
			m, _ := newGitModel(t, size[0], size[1])
			open(m)
			if m.topModal() == nil {
				t.Fatalf("%s: no modal open", name)
			}
			t.Run(name, func(t *testing.T) { checkGitWidths(t, m, size[0]) })
		}
	}
}
