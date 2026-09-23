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
	case "q", "ctrl+c":
		return tea.Quit
	case "esc":
		m.review = nil
		m.showHome()
		return nil
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
	r.viewport.Height = max(1, m.height-8)
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
	footer := "←/→ file · s staged/working · r refresh · Esc back · q quit"
	if m.width < 65 {
		footer = "←/→ file · s scope · Esc back · q quit"
	}
	if len(r.data.Repositories) > 1 {
		footer = "Tab repo · " + footer
	}
	return accent.Bold(true).Render(clip(title)) + "\n" + clip(status) + "\n" + clip(clean(r.data.Path)) + "\n\n" + r.viewport.View() + "\n\n" + clip(footer)
}
