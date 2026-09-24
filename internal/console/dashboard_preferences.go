package console

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Layout is local to this client/server pair. It never contains auth tokens,
// prompts, search queries, terminal output, or transient attention filters.
type dashboardPreferences struct {
	Version   int            `json:"version"`
	Grouping  map[string]int `json:"grouping"`
	Collapsed []string       `json:"collapsed,omitempty"`
	// RecentProjects is the newest-first list of projects the operator last
	// successfully opened a session or project shell in. It is additive: an
	// older Lectern ignores it, and a missing, empty, or stale value simply
	// falls back to the existing stable project order.
	RecentProjects []int64 `json:"recent_projects,omitempty"`
	// ScratchNewSession records that the last successful new session used no
	// project, so the form keeps offering Scratch instead of a remembered
	// project. A machine shell never sets it: a machine is not a project.
	ScratchNewSession bool `json:"scratch_new_session,omitempty"`
}

// maxRecentProjects bounds the remembered recency list. The remaining projects
// keep their existing order, so capping the list never hides a project.
const maxRecentProjects = 8

func dashboardPreferencePath(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("invalid server address")
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	digest := sha256.Sum256([]byte(u.String()))
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lectern", "console", hex.EncodeToString(digest[:])+".json"), nil
}

func readDashboardPreferences(path string) (dashboardPreferences, error) {
	var prefs dashboardPreferences
	f, err := os.Open(path)
	if err != nil {
		return prefs, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return prefs, err
	}
	if len(data) > 1<<20 {
		return prefs, errors.New("saved view is too large")
	}
	if err := json.Unmarshal(data, &prefs); err != nil {
		return prefs, err
	}
	if prefs.Version != 1 {
		return prefs, errors.New("unsupported saved view version")
	}
	return prefs, nil
}

func (m *dashboard) loadPreferences() {
	path, err := dashboardPreferencePath(m.client.Base)
	if err != nil {
		m.notice = "Dashboard view preferences unavailable: " + err.Error()
		return
	}
	m.preferencePath = path
	prefs, err := readDashboardPreferences(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		// Preserve malformed/newer state instead of overwriting it on the next
		// keypress. The dashboard itself remains fully usable.
		m.preferencePath = ""
		m.notice = "Could not load saved dashboard view; changes will not be saved: " + err.Error()
		return
	}
	for i, section := range sections {
		value := prefs.Grouping[section]
		max := 2
		if i == 0 {
			max = 3
		}
		if value >= 0 && value <= max {
			m.groupingBySection[section] = value
		}
	}
	m.grouping = m.groupingBySection[sections[m.section]]
	m.collapsed = map[string]bool{}
	for i, group := range prefs.Collapsed {
		if i >= 1024 {
			break
		}
		if len(group) > 0 && len(group) <= 240 {
			m.collapsed[group] = true
		}
	}
	m.recentProjects = normalizeRecentProjects(prefs.RecentProjects)
	m.scratchNewSession = prefs.ScratchNewSession
}

func (m *dashboard) savePreferences() {
	m.groupingBySection[sections[m.section]] = m.grouping
	if m.preferencePath == "" {
		return
	}
	prefs := dashboardPreferences{Version: 1, Grouping: m.groupingBySection, RecentProjects: m.recentProjects, ScratchNewSession: m.scratchNewSession}
	for path, collapsed := range m.collapsed {
		if collapsed && len(path) > 0 && len(path) <= 240 {
			prefs.Collapsed = append(prefs.Collapsed, path)
		}
	}
	sort.Strings(prefs.Collapsed)
	if len(prefs.Collapsed) > 1024 {
		prefs.Collapsed = prefs.Collapsed[:1024]
	}
	if err := writeDashboardPreferences(m.preferencePath, prefs); err != nil {
		m.notice = "Could not save dashboard view: " + err.Error()
	}
}

func writeDashboardPreferences(path string, prefs dashboardPreferences) error {
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".view-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace saved view: %w", err)
	}
	return nil
}

// rowProjectID reads the project_id recorded on a session or shell row. Rows
// arrive from JSON, so the value can be a float64, a string, or absent (a
// scratch or machine row carries no project).
func rowProjectID(r row) int64 { return intID(r["project_id"]) }

// rowIntID reads a row's own numeric id. The projects list identifies each
// project by "id", which is a different field from a session's project_id.
func rowIntID(r row) int64 { return intID(r["id"]) }

func intID(value any) int64 {
	switch v := value.(type) {
	case float64:
		if v > 0 {
			return int64(v)
		}
	case int64:
		if v > 0 {
			return v
		}
	case int:
		if v > 0 {
			return int64(v)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return n
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// normalizeRecentProjects drops unusable and duplicate IDs and caps the list,
// so a hand-edited or older preferences file cannot unbound the picker.
func normalizeRecentProjects(ids []int64) []int64 {
	out := make([]int64, 0, maxRecentProjects)
	seen := map[int64]bool{}
	for _, projectID := range ids {
		if projectID <= 0 || seen[projectID] {
			continue
		}
		seen[projectID] = true
		out = append(out, projectID)
		if len(out) == maxRecentProjects {
			break
		}
	}
	return out
}

// orderProjects lists projects most-recently-used first. Projects that are not
// recent keep their existing order, so the picker is unchanged when recency is
// empty and stale or deleted IDs are skipped without a gap.
func orderProjects(projects []row, recent []int64) []row {
	if len(projects) == 0 || len(recent) == 0 {
		return projects
	}
	byID := make(map[int64]row, len(projects))
	for _, p := range projects {
		if projectID := rowIntID(p); projectID > 0 {
			byID[projectID] = p
		}
	}
	out := make([]row, 0, len(projects))
	placed := make(map[int64]bool, len(projects))
	for _, projectID := range recent {
		if placed[projectID] {
			continue
		}
		p, ok := byID[projectID]
		if !ok {
			continue
		}
		placed[projectID] = true
		out = append(out, p)
	}
	for _, p := range projects {
		if projectID := rowIntID(p); projectID > 0 && placed[projectID] {
			continue
		}
		out = append(out, p)
	}
	return out
}

func sameInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// rememberProject promotes a successfully used project to the front of the
// recency list. Only a confirmed launch calls it, so a failed request never
// reorders the picker or changes the default.
func (m *dashboard) rememberProject(projectID int64) {
	if projectID <= 0 {
		return
	}
	recent := make([]int64, 0, maxRecentProjects)
	recent = append(recent, projectID)
	for _, id := range m.recentProjects {
		if id == projectID || len(recent) == maxRecentProjects {
			continue
		}
		recent = append(recent, id)
	}
	changed := m.scratchNewSession || !sameInt64s(recent, m.recentProjects)
	m.recentProjects = recent
	m.scratchNewSession = false
	if changed {
		m.savePreferences()
	}
}

// rememberScratchSession records an explicit Scratch choice for the ordinary
// new-session form. Shells never call it: picking a machine is not a project
// choice and must not overwrite the remembered project.
func (m *dashboard) rememberScratchSession() {
	if m.scratchNewSession {
		return
	}
	m.scratchNewSession = true
	m.savePreferences()
}

// defaultProjectChoice is the project the new-session form preselects: the most
// recent successful choice, unless the last choice was Scratch or the project
// no longer exists. A stale or deleted ID degrades to the blank Scratch option.
func (m *dashboard) defaultProjectChoice() string {
	if m.scratchNewSession {
		return ""
	}
	for _, projectID := range m.recentProjects {
		for _, p := range m.projects {
			if rowIntID(p) == projectID {
				return id(p)
			}
		}
	}
	return ""
}
