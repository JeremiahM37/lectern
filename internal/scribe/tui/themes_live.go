package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// themesCheckEvery is how often the user theme files are looked at for a
// change. A stat of a few files is cheap; a watcher would need a dependency.
const themesCheckEvery = 2 * time.Second

// themePaths are the places user themes are read from: the per-id directory
// and the legacy combined file, both under Scribe's home.
func themePaths() (dir, legacy string) {
	home := config.Home()
	return filepath.Join(home, "themes"), filepath.Join(home, "themes.json")
}

// loadUserThemes reads user themes once at startup, before the screen opens,
// so a broken file is reported on stderr instead of corrupting the UI.
func loadUserThemes() {
	dir, legacy := themePaths()
	if err := theme.LoadUserThemesFrom(dir, legacy); err != nil {
		fmt.Fprintln(os.Stderr, "themes:", err)
	}
}

// themeStamp is the newest modification time among the user theme files, or
// the zero time when there are none.
func themeStamp() time.Time {
	dir, legacy := themePaths()
	var newest time.Time
	if fi, err := os.Stat(legacy); err == nil {
		newest = fi.ModTime()
	}
	if fi, err := os.Stat(dir); err == nil && fi.ModTime().After(newest) {
		newest = fi.ModTime()
	}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	return newest
}

// checkThemes reloads the user themes when their files changed, the way a
// fs.watch would, and re-applies the active theme so an edit shows at once.
// It looks at most every themesCheckEvery.
func (m *Model) checkThemes(now time.Time) {
	if now.Sub(m.themesAt) < themesCheckEvery {
		return
	}
	m.themesAt = now
	stamp := themeStamp()
	if stamp.Equal(m.themesSeen) {
		return
	}
	first := m.themesSeen.IsZero()
	m.themesSeen = stamp
	if first {
		return // the startup load already read these files
	}
	loadUserThemes()
	if th, ok := theme.Lookup(m.themeName); ok {
		m.setTheme(m.themeName, m.styledTheme(th))
		m.setFlash("info", "Themes reloaded")
	}
}

// themeStyleKeys are the flat config keys of the theme style (config/theme_style.go).
var themeStyleKeys = []string{"theme_transparent", "theme_message_opacity", "theme_diff_opacity", "theme_border"}

// styleFromConfig reads the saved theme style: transparency, the message and
// diff background opacity, and the border strength. Unset values are the
// defaults (opaque, 100 percent, default border).
func styleFromConfig(c config.Config) theme.Style {
	s := theme.DefaultStyle()
	s.Transparent = config.Bool(c.ThemeTransparent, false)
	if c.ThemeMessageOpacity != nil {
		s.UserMessageOpacity = *c.ThemeMessageOpacity
	}
	if c.ThemeDiffOpacity != nil {
		s.DiffOpacity = *c.ThemeDiffOpacity
	}
	if c.ThemeBorder != "" {
		s.BorderStrength = c.ThemeBorder
	}
	return s
}

// startupStyle is th with the saved theme style applied. NewModel uses it so
// the first frame already shows the saved transparency and borders.
func startupStyle(th theme.Theme, be Backend) theme.Theme {
	return theme.Adjust(th, styleFromConfig(fields7(be)))
}

// styledTheme is th with the style saved for the active backend.
func (m *Model) styledTheme(th theme.Theme) theme.Theme {
	return theme.Adjust(th, styleFromConfig(fields7(m.cur().be)))
}

// applyThemeStyle re-applies the saved style to the active palette and every
// open transcript. The settings screen calls it after a style row changes.
func (m *Model) applyThemeStyle() {
	th, ok := theme.Lookup(m.themeName)
	if !ok {
		return
	}
	m.setTheme(m.themeName, m.styledTheme(th))
}

// isThemeStyleKey reports whether a flat key is one of the theme style keys.
func isThemeStyleKey(key string) bool {
	for _, k := range themeStyleKeys {
		if k == key {
			return true
		}
	}
	return false
}
