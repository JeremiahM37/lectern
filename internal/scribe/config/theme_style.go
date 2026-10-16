package config

// Flat keys for the theme style: transparency, the message and diff background
// opacity, and the border strength. They are the settings screen's names for
// the theme_* JSON fields in Config.

import (
	"fmt"
	"strings"
)

const (
	themeTransparentKey = "theme_transparent"
	themeMessageKey     = "theme_message_opacity"
	themeDiffKey        = "theme_diff_opacity"
	themeBorderKey      = "theme_border"
)

// ThemeOpacitySteps are the opacities the settings screen cycles through.
var ThemeOpacitySteps = []int{0, 30, 70, 100}

// ThemeBorders are the border strengths; the first is the default.
var ThemeBorders = []string{"default", "strong", "op"}

// setThemeStyle sets one theme style key. An opacity is a whole number from 0
// to 100; a border is one of ThemeBorders ("" and "default" both mean default).
func (c *Config) setThemeStyle(key string, v any) error {
	switch key {
	case themeTransparentKey:
		b, err := toBool(v)
		if err != nil {
			return err
		}
		c.ThemeTransparent = &b
	case themeMessageKey, themeDiffKey:
		n, err := toInt(v)
		if err != nil || n < 0 || n > 100 {
			return fmt.Errorf("%s must be a whole number from 0 to 100", key)
		}
		if key == themeMessageKey {
			c.ThemeMessageOpacity = &n
		} else {
			c.ThemeDiffOpacity = &n
		}
	case themeBorderKey:
		s := strings.ToLower(toString(v))
		if s == "default" {
			s = ""
		}
		if s != "" && !oneOf(s, ThemeBorders[1:]) {
			return fmt.Errorf("theme_border must be default, strong or op")
		}
		c.ThemeBorder = s
	}
	return nil
}
