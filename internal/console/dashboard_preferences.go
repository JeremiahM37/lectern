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
	"strings"
)

// Layout is local to this client/server pair. It never contains auth tokens,
// prompts, search queries, terminal output, or transient attention filters.
type dashboardPreferences struct {
	Version   int            `json:"version"`
	Grouping  map[string]int `json:"grouping"`
	Collapsed []string       `json:"collapsed,omitempty"`
}

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
}

func (m *dashboard) savePreferences() {
	m.groupingBySection[sections[m.section]] = m.grouping
	if m.preferencePath == "" {
		return
	}
	prefs := dashboardPreferences{Version: 1, Grouping: m.groupingBySection}
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
