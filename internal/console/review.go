package console

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type reviewFile struct {
	Path    string `json:"path"`
	Working bool   `json:"working"`
	Staged  bool   `json:"staged"`
}
type reviewRepository struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
type reviewData struct {
	Repositories       []reviewRepository `json:"repositories"`
	SelectedRepository int                `json:"selected_repository"`
	Branch             string             `json:"branch"`
	Scope              string             `json:"scope"`
	Path               string             `json:"path"`
	Patch              string             `json:"patch"`
	Files              []reviewFile       `json:"files"`
	Truncated          bool               `json:"truncated"`
}
type codeReview struct {
	base, scope, failure string
	repository           int
	data                 reviewData
	generation           int
	loading              bool
	viewport             viewport.Model
}
type reviewMsg struct {
	owner      *codeReview
	generation int
	data       reviewData
	err        error
}

func (m *dashboard) openReview() tea.Cmd {
	r := m.current()
	if r == nil {
		return nil
	}
	kind, rid := strings.TrimSuffix(sections[m.section], "s"), id(r)
	if kind == "task" {
		a, _ := r["attempt"].(map[string]any)
		if a == nil {
			m.notice = "No task attempt yet"
			return nil
		}
		kind = "attempt"
		rid = id(row(a))
	}
	if kind != "session" && kind != "project" && kind != "attempt" {
		return nil
	}
	m.review = &codeReview{base: "/term/" + kind + "/" + rid + "/changes", scope: "working", viewport: viewport.New(max(1, m.width-2), max(1, m.height-8))}
	return m.loadReview("")
}
func (m *dashboard) loadReview(path string) tea.Cmd {
	r := m.review
	r.generation++
	r.loading = true
	r.failure = ""
	generation := r.generation
	c := m.client
	endpoint := r.base + "?scope=" + r.scope + "&path=" + url.QueryEscape(path) + fmt.Sprintf("&repository=%d", r.repository)
	return func() tea.Msg {
		b, e := c.JSON("GET", endpoint, nil)
		var data reviewData
		if e == nil {
			e = json.Unmarshal(b, &data)
		}
		return reviewMsg{r, generation, data, e}
	}
}
func (m *dashboard) receiveReview(v reviewMsg) {
	r := m.review
	if r == nil || r != v.owner || r.generation != v.generation {
		return
	}
	r.loading = false
	if v.err != nil {
		r.failure = clean(v.err.Error())
		r.data = reviewData{}
		r.viewport.SetContent("Changes unavailable. Press r to retry.")
		return
	}
	r.data = v.data
	r.repository = v.data.SelectedRepository
	patch := clean(v.data.Patch)
	if v.data.Path == "" {
		patch = "No changes in this view. Press s to switch between working tree and staged changes."
	} else if patch == "" {
		patch = "No textual changes (rename or file permissions)."
	}
	var lines []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.ReplaceAll(line, "\t", "    ")
		line = ansi.Wrap(line, max(1, m.width-4), "")
		if strings.HasPrefix(line, "+") {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Render(line)
		} else if strings.HasPrefix(line, "-") {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(line)
		} else if strings.HasPrefix(line, "@@") {
			line = accent.Render(line)
		}
		lines = append(lines, line)
	}
	r.viewport.SetContent(strings.Join(lines, "\n"))
	r.viewport.GotoTop()
}
func (m *dashboard) reviewFiles() []reviewFile {
	var out []reviewFile
	for _, f := range m.review.data.Files {
		if m.review.scope == "working" && f.Working || m.review.scope == "staged" && f.Staged {
			out = append(out, f)
		}
	}
	return out
}
func (m *dashboard) updateReview(k tea.KeyMsg) tea.Cmd {
	r := m.review
	switch k.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc", "q":
		// q steps back here like Esc; it quits only from the top level.
		m.review = nil
		return m.showHome()
	case "?":
		m.review = nil
		m.reviewPaused = r
		m.openHelp()
		m.helpContext = "Review"
		return nil
	case "c":
		return m.commitForm()
	case "tab":
		if r.loading || len(r.data.Repositories) < 2 {
			return nil
		}
		index := 0
		for i, repo := range r.data.Repositories {
			if repo.ID == r.repository {
				index = i
			}
		}
		r.repository = r.data.Repositories[(index+1)%len(r.data.Repositories)].ID
		return m.loadReview("")
	case "s":
		if r.scope == "working" {
			r.scope = "staged"
		} else {
			r.scope = "working"
		}
		return m.loadReview("")
	case "r":
		return m.loadReview(r.data.Path)
	case "left", "[", "right", "]":
		if r.loading {
			return nil
		}
		files := m.reviewFiles()
		index := 0
		for i, f := range files {
			if f.Path == r.data.Path {
				index = i
			}
		}
		if k.String() == "left" || k.String() == "[" {
			index--
		} else {
			index++
		}
		if index >= 0 && index < len(files) {
			return m.loadReview(files[index].Path)
		}
	case "down", "j":
		r.viewport.LineDown(1)
	case "up", "k":
		r.viewport.LineUp(1)
	case "pgdown", "ctrl+d":
		r.viewport.HalfPageDown()
	case "pgup", "ctrl+u":
		r.viewport.HalfPageUp()
	case "home":
		r.viewport.GotoTop()
	case "end":
		r.viewport.GotoBottom()
	}
	return nil
}
func (m *dashboard) reviewView() string {
	r := m.review
	r.viewport.Width = max(1, m.width-2)
	r.viewport.Height = max(1, m.height-9)
	files := m.reviewFiles()
	index := 0
	for i, f := range files {
		if f.Path == r.data.Path {
			index = i + 1
		}
	}
	title := "Review changes · " + r.scope + " · " + clean(r.data.Branch)
	for _, repo := range r.data.Repositories {
		if repo.ID == r.repository {
			title = "Review · " + clean(repo.Name) + " · " + r.scope + " · " + clean(r.data.Branch)
		}
	}
	status := fmt.Sprintf("%d / %d files", index, len(files))
	if r.loading {
		status = "Loading changes…"
	} else if r.failure != "" {
		status = r.failure
	} else if r.data.Truncated {
		status += " · truncated at 512 KiB"
	}
	clip := func(s string) string { return ansi.Truncate(s, max(1, m.width-2), "…") }
	hints := []keyHint{{"←→", "file"}, {"c", "commit"}, {"s", "staged/working"}, {"r", "refresh"}, {"Esc", "back"}}
	if len(r.data.Repositories) > 1 {
		hints = append([]keyHint{{"Tab", "repository"}}, hints...)
	}
	footer := renderKeyBar(hints, m.width)
	return accent.Bold(true).Render(clip(title)) + "\n" + clip(status) + "\n" + clip(clean(r.data.Path)) + "\n\n" + r.viewport.View() + "\n\n" + footer + "\n" + clip(" "+m.notice)
}

const commitLabel = "Commit"

// commitForm asks for a message and commits every change in the reviewed
// workspace. The server refuses a session that works directly on its base
// branch; that refusal is explained with the next step rather than shown raw.
func (m *dashboard) commitForm() tea.Cmd {
	r := m.review
	parts := strings.Split(strings.TrimPrefix(r.base, "/term/"), "/")
	if len(parts) < 2 {
		return nil
	}
	kind, rid := parts[0], parts[1]
	var path string
	var body func(message string) map[string]any
	switch kind {
	case "session":
		path = "/sessions/" + rid + "/git/commit"
		body = func(message string) map[string]any {
			out := map[string]any{"message": message, "stage_all": true}
			if len(r.data.Repositories) > 1 {
				out["repo"] = fmt.Sprint(r.repository)
			}
			return out
		}
	case "attempt":
		task := m.current()
		if task == nil {
			return nil
		}
		path = "/tasks/" + id(task) + "/commit"
		body = func(message string) map[string]any { return map[string]any{"message": message} }
	default:
		m.notice = "Commit from a session in this project: select it on Sessions and press v, then c."
		return nil
	}
	m.reviewPaused = r
	m.review = nil
	fields := []field{{Key: "message", Label: "Commit message", Required: true}}
	onBase := kind == "session" && (r.data.Branch == "main" || r.data.Branch == "master")
	if onBase {
		// A session started without a worktree works straight on the
		// default branch. Offer a new branch first; committing onto the
		// default branch itself is the second choice and asks again.
		fields = append(fields,
			optionField("commit_to", "Commit to", "new", []choice{{"A new branch (recommended)", "new"}, {r.data.Branch + " itself", "base"}}, true),
			field{Key: "new_branch", Label: "New branch name", Value: "lectern/" + branchSlug(oneLine(name(m.current())))})
	}
	cmd := m.openForm("Commit changes", fields, func(values map[string]any) tea.Cmd {
		if m.busy {
			return nil
		}
		payload := body(str(values["message"]))
		notice := "Committed: " + oneLine(str(payload["message"]))
		if onBase {
			if values["commit_to"] == "base" {
				payload["allow_base_branch"] = true
				m.commitRetry = &commitRequest{path: path, payload: payload, notice: notice}
				// Named commitLabel so the result returns to the review.
				m.pending = &dashboardAction{Label: commitLabel, Method: "POST", Path: path, Body: payload,
					Warning: "The commit goes straight onto " + r.data.Branch + ", not onto a separate branch.", Confirm: "commit onto " + r.data.Branch, Notice: notice}
				m.form = nil
				return nil
			}
			payload["new_branch"] = str(values["new_branch"])
			notice += " — this folder is now on " + str(values["new_branch"])
		}
		return m.postCommit(commitRequest{path: path, payload: payload, notice: notice})
	})
	if m.form != nil {
		m.form.submitVerb = "commit"
		if onBase {
			m.form.help = "Committing on a new branch switches this folder, including your editor and other terminals, to that branch."
		}
		m.form.cancel = func() tea.Cmd {
			m.form = nil
			m.review, m.reviewPaused = m.reviewPaused, nil
			m.notice = "Commit cancelled"
			return nil
		}
	}
	return cmd
}

// commitRequest is one commit as sent, kept so that a commit refused for a
// missing git name and email can be sent again once they are given.
type commitRequest struct {
	path, notice string
	payload      map[string]any
}

func (m *dashboard) postCommit(req commitRequest) tea.Cmd {
	m.busy = true
	m.commitRetry = &req
	c := m.client
	return func() tea.Msg {
		data, err := c.JSON("POST", req.path, req.payload)
		if err != nil {
			return resultMsg{label: commitLabel, err: commitError(err)}
		}
		var out struct {
			Failed string `json:"failed"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(data, &out) == nil && out.Failed != "" {
			return resultMsg{label: commitLabel, err: fmt.Errorf("%s", strings.TrimPrefix(out.Detail, "commit failed: "))}
		}
		return resultMsg{label: commitLabel, data: data, notice: req.notice}
	}
}

// needsGitIdentity reports a commit the server refused because git has no
// name and email on that machine yet.
func needsGitIdentity(err error) bool {
	he, ok := err.(*HTTPError)
	return ok && he.Code == "no_git_identity"
}

// gitIdentityForm asks for the name and email git puts on commits, saves
// them for every repository on that machine, and sends the commit again —
// instead of sending the person off to run git config.
func (m *dashboard) gitIdentityForm() tea.Cmd {
	req := m.commitRetry
	if req == nil {
		return nil
	}
	fields := []field{
		{Key: "name", Label: "Your name", Required: true},
		{Key: "email", Label: "Your email", Required: true},
	}
	cmd := m.openForm("Git needs your name and email", fields, func(values map[string]any) tea.Cmd {
		if m.busy {
			return nil
		}
		name, email := strings.TrimSpace(str(values["name"])), strings.TrimSpace(str(values["email"]))
		if !strings.Contains(email, "@") {
			m.notice = "Enter an email address, like you@example.com"
			return nil
		}
		payload := map[string]any{}
		for k, v := range req.payload {
			payload[k] = v
		}
		payload["identity"] = map[string]any{"name": name, "email": email, "scope": "global"}
		return m.postCommit(commitRequest{path: req.path, payload: payload, notice: req.notice})
	})
	if m.form != nil {
		m.form.submitVerb = "save and commit"
		m.form.help = "Git puts these on every commit. They are saved for all your repositories on this computer (git config --global), then the commit goes ahead."
		m.form.cancel = func() tea.Cmd {
			m.form = nil
			m.commitRetry = nil
			if m.reviewPaused != nil {
				m.review, m.reviewPaused = m.reviewPaused, nil
			}
			m.notice = "Commit cancelled"
			return nil
		}
	}
	m.notice = "Git needs your name and email before it can commit."
	return cmd
}

func commitError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "refusing to commit directly on") {
		return fmt.Errorf("Not committed: this server does not commit on a session's main branch. Attach (Enter) and commit in a shell beside the agent (Ctrl+] |), or start the session in a separate Git worktree (n → More options)")
	}
	return err
}

// branchSlug turns a session name into a branch name part.
func branchSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		out = "work"
	}
	return out
}
