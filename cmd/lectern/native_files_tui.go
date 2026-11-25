package main

// The file pane's interface (native_files.go). It follows the file sidebars
// people already know — nvim-tree, neo-tree, yazi, lf, broot — and fits a
// pane anywhere from 24 columns to a whole window:
//
//   - a tree, folders first, with git's state on each row and on each folder
//     (the most important change inside it), and a mark on everything the
//     agent touched since the pane opened;
//   - Tab switches to just the changed files; d on one shows its diff;
//   - / finds a file anywhere in the workspace as you type (the web's Go to
//     file index), Enter reveals it in the tree;
//   - from 80 columns up (an overlay, or a window of its own) a preview of
//     the selected file or diff sits beside the list;
//   - optional columns go first when narrow; names are cut in the middle;
//   - it never takes focus by itself, and refreshes live from the server's
//     file watch (inotify on the target), falling back to polling.

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// filesHost is where the pane runs.
type filesHost struct {
	tmux  bool     // a pane of the attachment's private tmux server
	popup bool     // that pane is an overlay, not a sidebar
	self  []string // KIND ID BASE, for the pane's own popups
}

const (
	viewTree = iota
	viewChanges
)

const filesPreviewMinWidth = 80

var (
	fsDir      = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	fsDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	fsFaint    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	fsTitle    = lipgloss.NewStyle().Bold(true)
	fsTouched  = lipgloss.NewStyle().Foreground(lipgloss.Color("177"))
	fsSelFocus = lipgloss.NewStyle().Background(lipgloss.Color("238")).Bold(true)
	fsSelBlur  = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	fsKey      = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	fsMessage  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	fsAdd      = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	fsDel      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	fsHunk     = lipgloss.NewStyle().Foreground(lipgloss.Color("80"))
	fsLive     = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	fsPoll     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

// gitMarks are the letter and colour for each git state, most important first.
var gitMarks = map[string]struct {
	letter string
	color  string
	rank   int
}{
	"conflict":  {"!", "211", 6},
	"deleted":   {"D", "203", 5},
	"modified":  {"M", "214", 4},
	"renamed":   {"R", "214", 3},
	"added":     {"A", "114", 2},
	"untracked": {"U", "114", 1},
	"ignored":   {"", "243", 0},
}

type fileRow struct {
	entry fileEntry
	depth int
	note  string // a placeholder row ("loading…", an error)
}

type (
	filesListMsg struct {
		dir     string
		entries []fileEntry
		err     error
	}
	filesGitMsg struct {
		status map[string]string
		err    error
	}
	filesWatchMsg struct {
		gen int
		ans watchAnswer
		err error
	}
	filesPollMsg  struct{ gen int }
	filesIndexMsg struct {
		files []string
		err   error
	}
	filesPreviewMsg struct {
		key   string
		lines []string
	}
	filesPreviewTick struct{ seq int }
	filesDoneMsg     struct {
		err    error
		reload bool
		quit   bool
	}
	filesSayMsg struct{ text string }
)

type filesModel struct {
	api  *filesAPI
	host filesHost

	width, height int
	focused       bool

	listings map[string][]fileEntry
	failed   map[string]string
	expanded map[string]bool
	status   map[string]string
	dirState map[string]string
	touched  map[string]bool
	opened   int64

	view        int
	showIgnored bool
	rows        []fileRow
	cursor      int
	offset      int
	want        string // the path to keep selected across a rebuild
	revealing   string // a found file to select once its folder is listed

	finding    bool
	input      textinput.Model
	index      []string
	indexStale bool
	indexErr   string
	results    []string
	fcursor    int
	foffset    int

	previewKey   string // the preview being fetched
	previewCache map[string][]string
	previewSeq   int

	watchGen  int
	token     string
	watchMode string
	repo      bool

	message string
	until   time.Time
	help    bool

	lastClick     time.Time
	lastClickPath string
}

func runFilesPane(api *filesAPI, host filesHost) error {
	input := textinput.New()
	input.Prompt = "/ "
	input.Placeholder = "find a file"
	m := &filesModel{
		api: api, host: host, focused: true,
		listings: map[string][]fileEntry{}, failed: map[string]string{},
		expanded: map[string]bool{".": true}, status: map[string]string{},
		dirState: map[string]string{}, touched: map[string]bool{},
		previewCache: map[string][]string{},
		opened:       filesSince(), input: input,
	}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	return err
}

// filesSince is when this attachment first opened its file pane, so the
// "changed since" marks survive closing and reopening it.
func filesSince() int64 {
	now := time.Now().UnixMilli()
	dir := os.Getenv(linkDirEnv)
	if dir == "" {
		return now
	}
	file := path.Join(dir, "files-since")
	if data, err := os.ReadFile(file); err == nil {
		if since, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil && since > 0 {
			return since
		}
	}
	_ = os.WriteFile(file, []byte(strconv.FormatInt(now, 10)), 0o600)
	return now
}

func (m *filesModel) Init() tea.Cmd {
	return tea.Batch(m.listCmd("."), m.gitCmd(), m.watchCmd())
}

// ---- commands

func (m *filesModel) listCmd(dir string) tea.Cmd {
	api := m.api
	return func() tea.Msg {
		entries, err := api.list(context.Background(), dir)
		return filesListMsg{dir: dir, entries: entries, err: err}
	}
}

func (m *filesModel) gitCmd() tea.Cmd {
	api := m.api
	return func() tea.Msg {
		status, err := api.gitStatus(context.Background())
		return filesGitMsg{status: status, err: err}
	}
}

func (m *filesModel) watchDirs() []string {
	dirs := []string{"."}
	for dir, open := range m.expanded {
		if open && dir != "." {
			if _, ok := m.listings[dir]; ok {
				dirs = append(dirs, dir)
			}
		}
	}
	sort.Strings(dirs)
	return dirs[:min(len(dirs), 64)]
}

func (m *filesModel) watchCmd() tea.Cmd {
	m.watchGen++
	gen, api, dirs, token := m.watchGen, m.api, m.watchDirs(), m.token
	return func() tea.Msg {
		ans, err := api.watch(context.Background(), dirs, token)
		return filesWatchMsg{gen: gen, ans: ans, err: err}
	}
}

// refresh lists every open folder again and re-reads git's state.
func (m *filesModel) refresh() tea.Cmd {
	cmds := []tea.Cmd{m.gitCmd()}
	for dir, open := range m.expanded {
		if open {
			cmds = append(cmds, m.listCmd(dir))
		}
	}
	// The find index is read again when next needed; an open finder keeps
	// showing the old one until the new one arrives.
	if m.finding {
		cmds = append(cmds, m.indexCmd())
	} else {
		m.indexStale = true
	}
	// A changed file has a new mtime and so a new key; a diff may change
	// without one, so diffs are read again.
	for key := range m.previewCache {
		if strings.HasPrefix(key, "diff:") {
			delete(m.previewCache, key)
		}
	}
	m.previewKey = ""
	return tea.Batch(append(cmds, m.schedulePreview())...)
}

func (m *filesModel) indexCmd() tea.Cmd {
	api := m.api
	return func() tea.Msg {
		files, err := api.index(context.Background())
		return filesIndexMsg{files: files, err: err}
	}
}

func (m *filesModel) say(text string) {
	m.message, m.until = text, time.Now().Add(4*time.Second)
}

// ---- update

func (m *filesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clamp()
		return m, m.schedulePreview()
	case tea.FocusMsg:
		m.focused = true
	case tea.BlurMsg:
		m.focused = false
	case filesListMsg:
		if msg.err != nil {
			m.failed[msg.dir] = msg.err.Error()
			if msg.dir == "." {
				m.say(msg.err.Error())
			}
		} else {
			delete(m.failed, msg.dir)
			m.listings[msg.dir] = msg.entries
			for _, e := range msg.entries {
				if e.Mtime > m.opened {
					m.touched[e.Path] = true
				}
			}
		}
		m.rebuild()
		return m, m.schedulePreview()
	case filesGitMsg:
		if msg.err == nil {
			m.repo = true
			m.status = msg.status
			m.dirState = folderStates(msg.status)
			m.rebuild()
		}
		return m, m.schedulePreview()
	case filesWatchMsg:
		if msg.gen != m.watchGen {
			return m, nil
		}
		if msg.err != nil {
			m.watchMode = "poll"
			gen := m.watchGen
			return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return filesPollMsg{gen: gen} })
		}
		m.watchMode, m.token = msg.ans.Mode, msg.ans.Token
		if msg.ans.Changed {
			return m, tea.Batch(m.refresh(), m.watchCmd())
		}
		return m, m.watchCmd()
	case filesPollMsg:
		if msg.gen != m.watchGen {
			return m, nil
		}
		// Poll once, then try the watch again: a busy server frees up.
		return m, tea.Batch(m.refresh(), m.watchCmd())
	case filesIndexMsg:
		if msg.err != nil {
			m.indexErr = msg.err.Error()
		} else {
			m.index, m.indexErr = msg.files, ""
		}
		keep := m.fcursor
		m.match()
		m.fcursor = min(keep, max(0, len(m.results)-1))
		return m, nil
	case filesPreviewTick:
		if msg.seq == m.previewSeq {
			if key := m.previewWanted(); key != "" && key != m.previewKey {
				if _, ok := m.previewCache[key]; !ok {
					m.previewKey = key
					return m, m.previewCmd(key)
				}
			}
		}
		return m, nil
	case filesPreviewMsg:
		m.previewCache[msg.key] = msg.lines
		if msg.key == m.previewKey {
			m.previewKey = ""
		}
		return m, nil
	case filesDoneMsg:
		if msg.err != nil {
			m.say(msg.err.Error())
		}
		if msg.quit {
			return m, tea.Quit
		}
		if msg.reload {
			return m, m.refresh()
		}
		return m, m.schedulePreview()
	case filesSayMsg:
		m.say(msg.text)
		return m, nil
	case tea.MouseMsg:
		return m, m.mouse(msg)
	case tea.KeyMsg:
		var cmd tea.Cmd
		if m.finding {
			cmd = m.findKey(msg)
		} else {
			cmd = m.key(msg)
		}
		return m, tea.Batch(cmd, m.schedulePreview())
	}
	return m, nil
}

func (m *filesModel) key(msg tea.KeyMsg) tea.Cmd {
	if m.help {
		m.help = false
		return nil
	}
	m.message, m.revealing = "", ""
	switch msg.String() {
	case "q", "ctrl+c":
		return tea.Quit
	case "esc":
		if m.host.tmux && !m.host.popup {
			if pane, err := agentPane(); err == nil {
				_, _ = privateTmux("select-pane", "-t", pane)
			}
			return nil
		}
		return tea.Quit
	case "?":
		m.help = true
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "ctrl+u", "ctrl+b":
		m.move(-max(1, m.listHeight()-1))
	case "pgdown", "ctrl+d", "ctrl+f":
		m.move(max(1, m.listHeight()-1))
	case "home", "g":
		m.move(-len(m.rows))
	case "end", "G":
		m.move(len(m.rows))
	case "tab", "c":
		if m.view == viewTree {
			m.view = viewChanges
		} else {
			m.view = viewTree
		}
		m.cursor, m.offset = 0, 0
		m.rebuild()
	case ".":
		m.showIgnored = !m.showIgnored
		if m.showIgnored {
			m.say("Showing ignored and hidden files")
		} else {
			m.say("Hiding ignored files and .git")
		}
		m.rebuild()
	case "r":
		m.say("Refreshing…")
		return m.refresh()
	case "/":
		m.finding = true
		m.input.SetValue("")
		m.fcursor, m.foffset = 0, 0
		cmd := m.input.Focus()
		if m.index == nil || m.indexStale {
			m.indexStale = false
			m.match()
			return tea.Batch(cmd, m.indexCmd())
		}
		m.match()
		return cmd
	case "right", "l":
		if row, ok := m.selected(); ok {
			if row.entry.Directory {
				return m.expand(row.entry.Path, true)
			}
			return m.spawn("view", row.entry.Path)
		}
	case "left", "h":
		if row, ok := m.selected(); ok {
			if row.entry.Directory && m.expanded[row.entry.Path] {
				m.expanded[row.entry.Path] = false
				m.rebuild()
				return nil
			}
			if parent := path.Dir(row.entry.Path); parent != "." {
				m.want = parent
				m.rebuild()
			}
		}
	case "enter", " ":
		return m.activate()
	case "v", "o":
		if row, ok := m.selected(); ok && !row.entry.Directory {
			return m.spawn("view", row.entry.Path)
		}
	case "e":
		if row, ok := m.selected(); ok && !row.entry.Directory {
			return m.spawn("edit", row.entry.Path)
		}
	case "d":
		if row, ok := m.selected(); ok && !row.entry.Directory {
			return m.spawn("diff", row.entry.Path)
		}
	case "i", "a":
		if row, ok := m.selected(); ok {
			return m.insert(row.entry.Path)
		}
	case "y":
		if row, ok := m.selected(); ok {
			return m.copy(row.entry.Path, "Copied "+row.entry.Path)
		}
	case "w":
		if row, ok := m.selected(); ok {
			target := m.api.webURL(row.entry.Path)
			if open, ok := localOpener(); ok {
				go func() { _ = open(target) }()
				m.say("Opening the web viewer…")
				return nil
			}
			return m.copy(target, "Copied the web viewer's address")
		}
	}
	return nil
}

func (m *filesModel) activate() tea.Cmd {
	row, ok := m.selected()
	if !ok {
		return nil
	}
	if row.entry.Directory {
		return m.expand(row.entry.Path, !m.expanded[row.entry.Path])
	}
	if m.view == viewChanges && m.status[row.entry.Path] != "untracked" {
		return m.spawn("diff", row.entry.Path)
	}
	return m.spawn("view", row.entry.Path)
}

func (m *filesModel) expand(dir string, open bool) tea.Cmd {
	if m.view == viewChanges {
		return nil
	}
	m.expanded[dir] = open
	m.want = dir
	m.rebuild()
	if open {
		// Watch the new folder too.
		return tea.Batch(m.listCmd(dir), m.watchCmd())
	}
	return nil
}

func (m *filesModel) move(delta int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor = max(0, min(len(m.rows)-1, m.cursor+delta))
	m.want = m.rows[m.cursor].entry.Path
	m.clamp()
}

func (m *filesModel) selected() (fileRow, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) || m.rows[m.cursor].note != "" {
		return fileRow{}, false
	}
	return m.rows[m.cursor], true
}

// ---- find

func (m *filesModel) findKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.finding = false
		m.input.Blur()
		return nil
	case "up", "ctrl+p", "ctrl+k":
		m.fcursor = max(0, m.fcursor-1)
		return nil
	case "down", "ctrl+n", "ctrl+j":
		m.fcursor = min(max(0, len(m.results)-1), m.fcursor+1)
		return nil
	case "enter":
		if m.fcursor < len(m.results) {
			m.finding = false
			m.input.Blur()
			return m.reveal(m.results[m.fcursor])
		}
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.match()
	return cmd
}

func (m *filesModel) match() {
	m.results = fuzzyFiles(m.index, m.input.Value(), 300)
	m.fcursor, m.foffset = 0, 0
}

// reveal opens every folder above rel and selects it.
func (m *filesModel) reveal(rel string) tea.Cmd {
	m.view = viewTree
	var cmds []tea.Cmd
	for dir := path.Dir(rel); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if !m.expanded[dir] {
			m.expanded[dir] = true
		}
		if _, ok := m.listings[dir]; !ok {
			cmds = append(cmds, m.listCmd(dir))
		}
	}
	m.revealing = rel
	m.rebuild()
	return tea.Batch(append(cmds, m.watchCmd())...)
}

// fuzzyFiles ranks paths that contain query's letters in order: a run of
// consecutive letters, a match at the start of a name, and a short path
// score higher, as in Go to file.
func fuzzyFiles(files []string, query string, limit int) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		if len(files) > limit {
			return files[:limit]
		}
		return files
	}
	type scored struct {
		path  string
		score int
	}
	var out []scored
	for _, file := range files {
		if score, ok := fuzzyScore(strings.ToLower(file), query); ok {
			out = append(out, scored{file, score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return len(out[i].path) < len(out[j].path)
	})
	result := make([]string, 0, min(len(out), limit))
	for _, s := range out[:min(len(out), limit)] {
		result = append(result, s.path)
	}
	return result
}

func fuzzyScore(file, query string) (int, bool) {
	base := strings.LastIndex(file, "/") + 1
	if i := strings.Index(file[base:], query); i >= 0 {
		// The whole query inside the file name beats any scattered match.
		bonus := 0
		if i == 0 {
			bonus = 50
		}
		return 1000 + bonus - len(file), true
	}
	score, at, run := 0, 0, 0
	for _, q := range query {
		i := strings.IndexRune(file[at:], q)
		if i < 0 {
			return 0, false
		}
		pos := at + i
		if i == 0 && at > 0 {
			run++
			score += 5 * run
		} else {
			run = 0
		}
		if pos >= base {
			score += 3
		}
		if pos == 0 || file[pos-1] == '/' || file[pos-1] == '_' || file[pos-1] == '-' || file[pos-1] == '.' {
			score += 8
		}
		at = pos + utf8.RuneLen(q)
	}
	return score - len(file)/8, true
}

// ---- rows

func folderStates(status map[string]string) map[string]string {
	dirs := map[string]string{}
	for name, kind := range status {
		if kind == "ignored" {
			continue
		}
		for dir := path.Dir(strings.TrimSuffix(name, "/")); dir != "." && dir != "/"; dir = path.Dir(dir) {
			if gitMarks[kind].rank > gitMarks[dirs[dir]].rank {
				dirs[dir] = kind
			}
		}
	}
	return dirs
}

// stateOf is a path's git state: its own, an ignored folder above it, or
// (for a folder) the most important change inside it.
func (m *filesModel) stateOf(e fileEntry) string {
	if kind, ok := m.status[e.Path]; ok {
		return kind
	}
	if e.Directory {
		if kind, ok := m.status[e.Path+"/"]; ok {
			return kind
		}
		if kind, ok := m.dirState[e.Path]; ok {
			return kind
		}
	}
	for dir := path.Dir(e.Path); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if m.status[dir+"/"] == "ignored" {
			return "ignored"
		}
		if kind := m.status[dir+"/"]; kind == "untracked" {
			return kind
		}
	}
	return ""
}

func (m *filesModel) hidden(e fileEntry) bool {
	if m.showIgnored {
		return false
	}
	return e.Name == ".git" || m.stateOf(e) == "ignored"
}

func (m *filesModel) rebuild() {
	if m.want == "" && m.cursor < len(m.rows) && m.cursor >= 0 {
		m.want = m.rows[m.cursor].entry.Path
	}
	m.rows = m.rows[:0]
	if m.view == viewChanges {
		var names []string
		for name, kind := range m.status {
			if kind != "ignored" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			dir := strings.HasSuffix(name, "/")
			clean := strings.TrimSuffix(name, "/")
			m.rows = append(m.rows, fileRow{entry: fileEntry{Name: path.Base(clean), Path: clean, Directory: dir}})
		}
		if len(m.rows) == 0 {
			note := "No uncommitted changes"
			if !m.repo {
				note = "Not a git repository"
			}
			m.rows = append(m.rows, fileRow{note: note})
		}
	} else {
		m.flatten(".", 0)
	}
	for i, row := range m.rows {
		if row.note == "" && row.entry.Path == m.want {
			m.cursor = i
		}
	}
	for i, row := range m.rows {
		if row.note == "" && m.revealing != "" && row.entry.Path == m.revealing {
			m.cursor, m.revealing = i, ""
			// Show it with some context above.
			m.offset = max(0, i-m.listHeight()/3)
		}
	}
	m.want = ""
	m.clamp()
}

func (m *filesModel) flatten(dir string, depth int) {
	entries, ok := m.listings[dir]
	if !ok {
		note := "loading…"
		if failure := m.failed[dir]; failure != "" {
			note = failure
		}
		m.rows = append(m.rows, fileRow{depth: depth, note: note, entry: fileEntry{Path: dir + "/…"}})
		return
	}
	for _, e := range entries {
		if m.hidden(e) {
			continue
		}
		m.rows = append(m.rows, fileRow{entry: e, depth: depth})
		if e.Directory && m.expanded[e.Path] {
			m.flatten(e.Path, depth+1)
		}
	}
	if dir == "." && len(m.rows) == 0 {
		m.rows = append(m.rows, fileRow{note: "Empty folder"})
	}
}

func (m *filesModel) touchedDir(dir string) bool {
	for name := range m.touched {
		if strings.HasPrefix(name, dir+"/") {
			return true
		}
	}
	return false
}

// ---- layout

func (m *filesModel) previewShown() bool { return m.width >= filesPreviewMinWidth }

func (m *filesModel) listWidth() int {
	if !m.previewShown() {
		return m.width
	}
	return max(28, min(m.width*38/100, 56))
}

func (m *filesModel) listHeight() int { return max(1, m.height-3) }

func (m *filesModel) clamp() {
	n := len(m.rows)
	if m.cursor >= n {
		m.cursor = max(0, n-1)
	}
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(0, min(m.offset, max(0, n-h)))
}

// ---- preview

func (m *filesModel) previewWanted() string {
	if !m.previewShown() || m.finding {
		return ""
	}
	row, ok := m.selected()
	if !ok || row.entry.Directory {
		return ""
	}
	if m.view == viewChanges && m.status[row.entry.Path] != "untracked" {
		return "diff:" + row.entry.Path
	}
	return fmt.Sprintf("file:%s@%d", row.entry.Path, row.entry.Mtime)
}

func (m *filesModel) schedulePreview() tea.Cmd {
	m.previewSeq++
	seq := m.previewSeq
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return filesPreviewTick{seq: seq} })
}

func (m *filesModel) previewCmd(key string) tea.Cmd {
	api := m.api
	return func() tea.Msg {
		kind, rest, _ := strings.Cut(key, ":")
		if kind == "diff" {
			patch, err := api.diff(context.Background(), rest)
			if err != nil {
				return filesPreviewMsg{key: key, lines: []string{fsDim.Render(err.Error())}}
			}
			var lines []string
			for _, line := range strings.Split(patch, "\n") {
				lines = append(lines, diffLine(line))
				if len(lines) > 400 {
					break
				}
			}
			return filesPreviewMsg{key: key, lines: lines}
		}
		rel := rest[:strings.LastIndex(rest, "@")]
		data, _, err := api.read(context.Background(), rel, 128<<10)
		if err != nil {
			return filesPreviewMsg{key: key, lines: []string{fsDim.Render(err.Error())}}
		}
		if !textual(data) {
			return filesPreviewMsg{key: key, lines: []string{fsDim.Render("Not text — w opens it in the web viewer")}}
		}
		text := strings.ReplaceAll(string(data), "\t", "    ")
		raw := strings.Split(text, "\n")
		width := len(fmt.Sprint(min(len(raw), 400)))
		var lines []string
		for i, line := range raw[:min(len(raw), 400)] {
			lines = append(lines, fsFaint.Render(fmt.Sprintf("%*d ", width, i+1))+sanitizeLine(line))
		}
		return filesPreviewMsg{key: key, lines: lines}
	}
}

func diffLine(line string) string {
	line = sanitizeLine(line)
	switch {
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
		return fsDim.Render(line)
	case strings.HasPrefix(line, "@@"):
		return fsHunk.Render(line)
	case strings.HasPrefix(line, "+"):
		return fsAdd.Render(line)
	case strings.HasPrefix(line, "-"):
		return fsDel.Render(line)
	}
	return line
}

// sanitizeLine drops control characters, so a file cannot drive the terminal.
func sanitizeLine(line string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, line)
}

// ---- actions

// spawn opens a file in a popup over the window (from the sidebar) or in
// this pane (an overlay or a window of its own already has the room).
func (m *filesModel) spawn(verb, rel string) tea.Cmd {
	self, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return filesDoneMsg{err: err} }
	}
	argv := append([]string{self, terminalFilesFlag}, m.host.self...)
	argv = append(argv, verb, rel)
	reload := verb == "edit"
	if m.host.tmux && !m.host.popup {
		title := " " + rel + " "
		return func() tea.Msg {
			_, err := privateTmux("display-popup", "-E", "-w", "92%", "-h", "90%", "-T", title, "--", quoteWords(argv))
			return filesDoneMsg{err: err, reload: reload}
		}
	}
	return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg {
		return filesDoneMsg{err: err, reload: reload}
	})
}

// insert types the path into the agent's prompt without pressing Enter, and
// puts the cursor back there.
func (m *filesModel) insert(rel string) tea.Cmd {
	text := rel
	if strings.ContainsAny(text, " \t'\"\\$`!*?[]{}()<>|&;") {
		text = quoteWords([]string{rel})
	}
	text += " "
	if !m.host.tmux {
		return m.copy(text, "Copied "+rel+" — paste it into the agent's prompt")
	}
	if _, bad := firstInsertControl(text); bad {
		m.say("That name cannot be typed safely")
		return nil
	}
	pane, err := agentPane()
	if err == nil {
		_, err = privateTmux("send-keys", "-l", "-t", pane, "--", text)
	}
	if err != nil {
		m.say("Could not type into the agent: " + err.Error())
		return nil
	}
	if m.host.popup {
		return tea.Quit
	}
	_, _ = privateTmux("select-pane", "-t", pane)
	m.say("Typed " + rel + " into the prompt")
	return nil
}

// copy puts text on the clipboard of the terminal in front of the person
// (OSC 52; through tmux's set-clipboard when inside the private server).
func (m *filesModel) copy(text, done string) tea.Cmd {
	if m.host.tmux {
		if _, err := privateTmux("set-buffer", "-w", "--", text); err != nil {
			m.say("Could not copy: " + err.Error())
			return nil
		}
		m.say(done)
		return nil
	}
	fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
	m.say(done)
	return nil
}

// ---- mouse

func (m *filesModel) mouse(msg tea.MouseMsg) tea.Cmd {
	if m.help {
		if msg.Action == tea.MouseActionPress {
			m.help = false
		}
		return nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.offset = max(0, m.offset-3)
		m.cursor = min(m.cursor, m.offset+m.listHeight()-1)
		return m.schedulePreview()
	case tea.MouseButtonWheelDown:
		m.offset = max(0, min(m.offset+3, len(m.rows)-m.listHeight()))
		m.cursor = max(m.cursor, m.offset)
		return m.schedulePreview()
	}
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return nil
	}
	if msg.Y == 0 && !m.finding {
		// The header's view switch.
		if msg.X >= m.listWidth()-18 {
			m.view = 1 - m.view
			m.cursor, m.offset = 0, 0
			m.rebuild()
		}
		return nil
	}
	if m.finding || msg.X >= m.listWidth() {
		return nil
	}
	i := m.offset + msg.Y - 2
	if i < 0 || i >= len(m.rows) || m.rows[i].note != "" {
		return nil
	}
	m.cursor = i
	clicked := m.rows[i].entry.Path
	double := clicked == m.lastClickPath && time.Since(m.lastClick) < 400*time.Millisecond
	m.lastClick, m.lastClickPath = time.Now(), clicked
	if double || m.rows[i].entry.Directory {
		m.lastClickPath = ""
		return tea.Batch(m.activate(), m.schedulePreview())
	}
	return m.schedulePreview()
}

// ---- view

func (m *filesModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.help {
		return m.helpView()
	}
	lw := m.listWidth()
	left := m.listView(lw)
	if !m.previewShown() {
		return left
	}
	right := m.previewView(m.width - lw - 1)
	sep := fsFaint.Render(strings.Repeat("│\n", m.height-1) + "│")
	return lipgloss.JoinHorizontal(lipgloss.Top, left, sep, right)
}

func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := ansi.StringWidth(s)
	if w <= width {
		return s + strings.Repeat(" ", width-w)
	}
	return ansi.Truncate(s, width, "…")
}

// fitMiddle cuts a name in the middle so its start and extension stay.
func fitMiddle(s string, width int) string {
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if width <= 4 {
		return runewidth.Truncate(s, width, "…")
	}
	tail := (width - 1) / 3
	runes := []rune(s)
	end := string(runes[len(runes)-tail:])
	return runewidth.Truncate(s, width-1-runewidth.StringWidth(end), "") + "…" + end
}

func (m *filesModel) header(width int) string {
	dot := fsFaint.Render("○")
	switch m.watchMode {
	case "inotify", "poll":
		if m.watchMode == "inotify" {
			dot = fsLive.Render("●")
		} else {
			dot = fsPoll.Render("●")
		}
	}
	if m.watchGen > 0 && m.watchMode == "" {
		dot = fsFaint.Render("○")
	}
	changed := 0
	for _, kind := range m.status {
		if kind != "ignored" {
			changed++
		}
	}
	tree, changes := "Tree", fmt.Sprintf("Changes %d", changed)
	if width < 34 {
		tree, changes = "Tree", fmt.Sprintf("Δ%d", changed)
	}
	if m.view == viewTree {
		tree, changes = fsTitle.Render(tree), fsDim.Render(changes)
	} else {
		tree, changes = fsDim.Render(tree), fsTitle.Render(changes)
	}
	tabs := tree + fsFaint.Render(" · ") + changes
	title := " " + dot + " " + fsTitle.Render("Files")
	gap := width - lipgloss.Width(title) - lipgloss.Width(tabs) - 1
	if gap < 1 {
		return fit(" "+dot+" "+tabs, width)
	}
	return title + strings.Repeat(" ", gap) + tabs + " "
}

func (m *filesModel) listView(width int) string {
	var b strings.Builder
	b.WriteString(m.header(width) + "\n")
	b.WriteString(fsFaint.Render(strings.Repeat("─", width)) + "\n")
	h := m.listHeight()
	if m.finding {
		b.WriteString(m.finderRows(width, h))
	} else {
		for i := m.offset; i < m.offset+h; i++ {
			if i < len(m.rows) {
				b.WriteString(m.rowView(m.rows[i], width, i == m.cursor))
			} else {
				b.WriteString(strings.Repeat(" ", width))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString(m.footer(width))
	return b.String()
}

func (m *filesModel) rowView(row fileRow, width int, selected bool) string {
	indentStep := 2
	if width < 32 {
		indentStep = 1
	}
	indent := strings.Repeat(" ", 1+row.depth*indentStep)
	if row.note != "" {
		return fit(indent+fsDim.Render(row.note), width)
	}
	e := row.entry
	kind := m.stateOf(e)
	mark := gitMarks[kind]
	icon := "  "
	if e.Directory {
		icon = "▸ "
		if m.expanded[e.Path] && m.view == viewTree {
			icon = "▾ "
		}
	}
	name := e.Name
	if m.view == viewChanges {
		name = e.Path
	}
	if e.Directory {
		name += "/"
	}
	right := ""
	if mark.letter != "" {
		right = mark.letter
	}
	touched := m.touched[e.Path] || (e.Directory && m.touchedDir(e.Path))
	if touched {
		right = "●" + right
	}
	if right != "" {
		right = " " + right + " "
	}
	room := width - runewidth.StringWidth(indent) - 2 - runewidth.StringWidth(right)
	shown := fitMiddle(name, max(1, room))
	pad := max(0, room-runewidth.StringWidth(shown))
	style := lipgloss.NewStyle()
	switch {
	case kind == "ignored":
		style = fsFaint
	case kind != "":
		style = lipgloss.NewStyle().Foreground(lipgloss.Color(mark.color))
	case e.Directory:
		style = fsDir
	}
	markText := ""
	if right != "" {
		markText = " "
		if touched {
			markText += fsTouched.Render("●")
		}
		if mark.letter != "" {
			markText += lipgloss.NewStyle().Foreground(lipgloss.Color(mark.color)).Bold(true).Render(mark.letter)
		}
		markText += " "
	}
	if selected {
		sel := fsSelBlur
		if m.focused {
			sel = fsSelFocus
		}
		// Keep the colours, change the background across the whole row.
		style = style.Inherit(sel)
		return sel.Render(indent+icon) + style.Render(shown+strings.Repeat(" ", pad)) + sel.Render(stripStyle(markText, right))
	}
	return indent + fsFaint.Render(icon) + style.Render(shown) + strings.Repeat(" ", pad) + markText
}

// stripStyle keeps a selected row's markers plain, so its background is unbroken.
func stripStyle(styled, plain string) string {
	if styled == "" {
		return ""
	}
	return plain
}

func (m *filesModel) finderRows(width, h int) string {
	var b strings.Builder
	m.input.Width = max(4, width-4)
	b.WriteString(fit(m.input.View(), width) + "\n")
	h--
	switch {
	case m.indexErr != "":
		b.WriteString(fit(" "+fsDim.Render(m.indexErr), width) + "\n")
		h--
	case m.index == nil:
		b.WriteString(fit(" "+fsDim.Render("listing files…"), width) + "\n")
		h--
	case len(m.results) == 0:
		b.WriteString(fit(" "+fsDim.Render("no match"), width) + "\n")
		h--
	}
	if m.fcursor < m.foffset {
		m.foffset = m.fcursor
	}
	if m.fcursor >= m.foffset+h {
		m.foffset = m.fcursor - h + 1
	}
	for i := m.foffset; i < m.foffset+h; i++ {
		if i >= len(m.results) || h <= 0 {
			b.WriteString(strings.Repeat(" ", width) + "\n")
			continue
		}
		file := m.results[i]
		dir, base := path.Split(file)
		room := width - 2
		baseShown := fitMiddle(base, room)
		dirShown := ""
		if left := room - runewidth.StringWidth(baseShown) - 1; left > 3 && dir != "" {
			dirShown = " " + fitMiddle(strings.TrimSuffix(dir, "/"), left)
		}
		line := " " + baseShown + fsDim.Render(dirShown)
		if i == m.fcursor {
			line = fsSelFocus.Render(fit(" "+baseShown+dirShown, width))
		} else {
			line = fit(line, width)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// footer is the message of the moment, or the keys that fit.
func (m *filesModel) footer(width int) string {
	if m.message != "" && time.Now().Before(m.until) {
		return fit(" "+fsMessage.Render(m.message), width)
	}
	var keys [][2]string
	if m.finding {
		keys = [][2]string{{"⏎", "reveal"}, {"esc", "back"}}
	} else {
		keys = [][2]string{{"⏎", "open"}, {"e", "edit"}, {"d", "diff"}, {"i", "to prompt"}, {"/", "find"}, {"?", "keys"}}
		if m.host.tmux && !m.host.popup {
			keys = append(keys[:len(keys)-1], [2]string{"esc", "agent"}, [2]string{"?", "keys"})
		}
	}
	line, used := " ", 1
	for i, k := range keys {
		part := fsKey.Render(k[0]) + " " + fsDim.Render(k[1])
		w := runewidth.StringWidth(k[0]) + 1 + runewidth.StringWidth(k[1]) + 2
		// "? keys" always shows, at the end.
		if used+w > width && i != len(keys)-1 {
			continue
		}
		if used+w-2 > width {
			break
		}
		line += part + "  "
		used += w
	}
	return fit(strings.TrimRight(line, " "), width)
}

func (m *filesModel) previewView(width int) string {
	h := m.height
	var title string
	var lines []string
	row, ok := m.selected()
	switch {
	case m.finding:
		title = "Find"
		lines = []string{fsDim.Render("Type part of a name; ⏎ shows it in the tree.")}
	case !ok:
		title = ""
	case row.entry.Directory:
		title = row.entry.Path + "/"
		n := len(m.listings[row.entry.Path])
		if _, loaded := m.listings[row.entry.Path]; loaded {
			lines = []string{fsDim.Render(fmt.Sprintf("%d items", n))}
		} else {
			lines = []string{fsDim.Render("⏎ opens the folder")}
		}
	default:
		title = row.entry.Path
		if m.view == viewChanges && m.status[row.entry.Path] != "untracked" {
			title += fsDim.Render("  changes")
		} else if row.entry.Size > 0 {
			title += fsDim.Render("  " + humanSize(row.entry.Size))
		}
		if cached, ok := m.previewCache[m.previewWanted()]; ok {
			lines = cached
		} else {
			lines = []string{fsDim.Render("…")}
		}
	}
	var b strings.Builder
	b.WriteString(fit(" "+fsTitle.Render(fitMiddle(title, max(1, width-2))), width) + "\n")
	b.WriteString(fsFaint.Render(strings.Repeat("─", width)) + "\n")
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(lines) {
			line = " " + lines[i]
		}
		b.WriteString(fitStyled(line, width))
		if i < h-3 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// fitStyled pads or cuts a line that carries colour codes.
func fitStyled(s string, width int) string {
	w := lipgloss.Width(s)
	if w <= width {
		return s + strings.Repeat(" ", width-w)
	}
	return ansi.Truncate(s, width, "")
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
}

func (m *filesModel) helpView() string {
	keys := [][2]string{
		{"↑↓ j k", "move"}, {"→ l", "open folder / view file"}, {"← h", "close folder / go up"},
		{"⏎", "open (diff in Changes)"}, {"v", "view"}, {"e", "edit in $EDITOR"}, {"d", "diff"},
		{"i", "type path into the agent's prompt"}, {"y", "copy path"}, {"w", "open in the web viewer"},
		{"/", "find a file"}, {"Tab", "tree ↔ changed files"}, {".", "show ignored files"},
		{"r", "refresh"}, {"esc", "back to the agent"}, {"q", "close"},
		{"Ctrl+] f", "open / focus / close"},
	}
	var b strings.Builder
	b.WriteString(" " + fsTitle.Render("File pane keys") + "\n\n")
	for _, k := range keys {
		b.WriteString(" " + fsKey.Render(fmt.Sprintf("%-9s", k[0])) + " " + fsDim.Render(k[1]) + "\n")
	}
	b.WriteString("\n " + fsTouched.Render("●") + fsDim.Render(" changed since you opened files here") + "\n")
	b.WriteString(" " + fsDim.Render("M A U D ! git state; any key closes"))
	lines := strings.Split(b.String(), "\n")
	for i := range lines {
		lines[i] = fitStyled(lines[i], m.width)
	}
	for len(lines) < m.height {
		lines = append(lines, strings.Repeat(" ", m.width))
	}
	return strings.Join(lines[:m.height], "\n")
}
