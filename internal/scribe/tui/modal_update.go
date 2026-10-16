package tui

// The update notice (/update): asks the release list for the newest Lectern
// release and says whether this build is behind. It only checks when asked;
// nothing runs at startup. Installing is `lectern update`, which owns the
// download and the checksum.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(&command{Name: "update", Desc: "Check whether a newer Lectern release is out", Group: "System",
		Run: func(m *Model, _ string) tea.Cmd {
			c, ok := m.cur().be.(UpdateChecker)
			if !ok {
				return flashCmd("info", "This build cannot check for updates. Run lectern update to check.")
			}
			u := &updateNotice{th: m.th, be: c, loading: true}
			m.pushModal(u)
			return u.check()
		}})
}

// UpdateStatus is what an update check found. Latest is "" when the release
// list could not be read, and Err says why.
type UpdateStatus struct {
	Current string
	Latest  string
	Known   bool   // Current and Latest could be compared
	Newer   bool   // Latest is newer than Current
	Err     string // why the check failed
	// Notes is the changelog excerpt for Latest, one line per change, newest
	// first. Empty when the release source does not carry release notes.
	Notes []string
	// URL is the release page for Latest. Empty means the releases index.
	URL string
}

// maxNotes caps the changelog excerpt in the notice.
const maxNotes = 6

// UpdateChecker is optionally implemented by a Backend that can check the
// release list. It may block for a few seconds; the notice runs it in the
// background.
type UpdateChecker interface {
	CheckUpdate() UpdateStatus
}

// updateDone carries the result of one check back to the notice.
type updateDone struct{ st UpdateStatus }

type updateNotice struct {
	th      theme.Theme
	be      UpdateChecker
	st      UpdateStatus
	loading bool
}

func (u *updateNotice) check() tea.Cmd {
	be := u.be
	return func() tea.Msg { return updateDone{st: be.CheckUpdate()} }
}

// Title implements Modal.
func (u *updateNotice) Title() string { return "Updates" }

// Icon implements Iconed.
func (u *updateNotice) Icon() string { return "download" }

// Footer implements Footered.
func (u *updateNotice) Footer() string {
	if u.st.Newer {
		return "r check again · g GitHub · Esc close"
	}
	return "r check again · Esc close"
}

// Busy implements busyModal: keys wait while the release list is read.
func (u *updateNotice) Busy() bool { return u.loading }

// Size implements Sized.
func (u *updateNotice) Size(cols, rows int) (int, int) {
	return min(cols-4, 72), min(rows-4, 10)
}

// Update implements Modal.
func (u *updateNotice) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case updateDone:
		u.st, u.loading = msg.st, false
		return u, nil, false
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return u, nil, true
		case "r":
			if !u.loading {
				u.loading = true
				return u, u.check(), false
			}
		case "g":
			link := u.st.URL
			if link == "" {
				link = releasePage(u.st.Latest)
			}
			if err := openURL(link); err != nil {
				return u, flashCmd("info", "Open this in a browser: "+link), false
			}
			return u, flashCmd("ok", "Opened the release page"), false
		}
	}
	return u, nil, false
}

// View implements Modal.
func (u *updateNotice) View(w, h int) string {
	th := u.th
	var out []string
	if u.loading {
		out = append(out, fg(th.TextFaint).Render("  Checking the release list…"))
	} else {
		out = append(out, u.lines(th)...)
	}
	out = append(out, "", fg(th.TextFaint).Render(render.Truncate(
		"Lectern never checks in the background. Installing is lectern update, or your package manager.", w)))
	for i := range out {
		out[i] = render.Truncate(out[i], w)
	}
	return strings.Join(fitLines(out, w, h), "\n")
}

// lines is the message for the result of a check.
func (u *updateNotice) lines(th theme.Theme) []string {
	st := u.st
	cur := fg(th.TextPrimary).Render(st.Current)
	switch {
	case st.Err != "":
		return []string{
			"  You are running " + cur + ".",
			"",
			fg(th.Error).Render("  Could not read the release list: " + oneLine(st.Err)),
		}
	case st.Newer:
		out := []string{
			"  You are running " + cur + ".",
			"",
			fg(th.Warning).Render(fmt.Sprintf("  Release %s is out. Run lectern update to install it.", st.Latest)),
			fg(th.TextMuted).Render("  Release page: " + releasePageOf(st)),
		}
		if len(st.Notes) > 0 {
			out = append(out, "", fgb(th.TextPrimary).Render("  What's new"))
			for i, n := range st.Notes {
				if i == maxNotes {
					out = append(out, fg(th.TextFaint).Render(fmt.Sprintf("    … and %d more on GitHub", len(st.Notes)-maxNotes)))
					break
				}
				out = append(out, fg(th.TextSecondary).Render("    • "+oneLine(n)))
			}
		}
		return out
	case st.Known:
		return []string{
			"  You are running " + cur + ".",
			"",
			fg(th.Success).Render("  This is the newest release (" + st.Latest + ")."),
		}
	}
	return []string{
		"  You are running " + cur + ", which is not a release build.",
		"",
		fg(th.TextMuted).Render("  The newest release is " + st.Latest + ". This build cannot be compared with it."),
	}
}

// releasePageOf is the release link the notice shows for a newer release.
func releasePageOf(st UpdateStatus) string {
	if st.URL != "" {
		return st.URL
	}
	return releasePage(st.Latest)
}
