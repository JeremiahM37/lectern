package config

// Flat keys for the theme style: transparency, the message and diff background
// opacity, and the border strength. They are the settings screen's names for
// the theme_* JSON fields in Config.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	themeTransparentKey = "theme_transparent"
	themeMessageKey     = "theme_message_opacity"
	themeDiffKey        = "theme_diff_opacity"
	themeBorderKey      = "theme_border"
)

// legacyThemeKeys are the flat keys older versions wrote for the theme style.
// They are read as a fallback and removed the next time the file is saved.
var legacyThemeKeys = []string{themeTransparentKey, themeMessageKey, themeDiffKey, themeBorderKey}

// themeObject is the "theme" object of the config file (spec 02 section 5.2):
// the theme name and its style. Unset fields are left out.
type themeObject struct {
	Name               string `json:"name,omitempty"`
	Transparent        *bool  `json:"transparent,omitempty"`
	UserMessageOpacity *int   `json:"userMessageOpacity,omitempty"`
	DiffOpacity        *int   `json:"diffOpacity,omitempty"`
	BorderStrength     string `json:"borderStrength,omitempty"`
}

// legacyThemeFlat holds the flat theme keys of the file, for migration.
type legacyThemeFlat struct {
	Transparent *bool  `json:"theme_transparent,omitempty"`
	MsgOpacity  *int   `json:"theme_message_opacity,omitempty"`
	DiffOpacity *int   `json:"theme_diff_opacity,omitempty"`
	Border      string `json:"theme_border,omitempty"`
}

// validOpacity reports whether n is a percentage from 0 to 100.
func validOpacity(n int) bool { return n >= 0 && n <= 100 }

// normBorder maps a border name to the stored form ("" is the default) and
// reports whether it is one of ThemeBorders.
func normBorder(s string) (string, bool) {
	s = strings.ToLower(s)
	if s == "default" {
		s = ""
	}
	if s == "" || oneOf(s, ThemeBorders[1:]) {
		return s, true
	}
	return "", false
}

// UnmarshalJSON reads the config file. The "theme" key may be the name string
// that older versions wrote or the theme object. The flat theme keys are read
// too, and the object wins where both set a value. Values that are out of
// range are ignored rather than failing the whole file.
func (c *Config) UnmarshalJSON(b []byte) error {
	type plain Config
	if err := json.Unmarshal(b, (*plain)(c)); err != nil {
		return err
	}
	var side struct {
		Theme json.RawMessage `json:"theme"`
		legacyThemeFlat
	}
	if err := json.Unmarshal(b, &side); err != nil {
		return err
	}
	f := side.legacyThemeFlat
	if f.Transparent != nil {
		c.ThemeTransparent = f.Transparent
	}
	if f.MsgOpacity != nil && validOpacity(*f.MsgOpacity) {
		c.ThemeMessageOpacity = f.MsgOpacity
	}
	if f.DiffOpacity != nil && validOpacity(*f.DiffOpacity) {
		c.ThemeDiffOpacity = f.DiffOpacity
	}
	if s, ok := normBorder(f.Border); ok && f.Border != "" {
		c.ThemeBorder = s
	}
	raw := bytes.TrimSpace(side.Theme)
	switch {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
	case raw[0] == '"':
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return err
		}
		c.Theme = name
	case raw[0] == '{':
		var o themeObject
		if err := json.Unmarshal(raw, &o); err != nil {
			return fmt.Errorf("theme: %w", err)
		}
		if o.Name != "" {
			c.Theme = o.Name
		}
		if o.Transparent != nil {
			c.ThemeTransparent = o.Transparent
		}
		if o.UserMessageOpacity != nil && validOpacity(*o.UserMessageOpacity) {
			c.ThemeMessageOpacity = o.UserMessageOpacity
		}
		if o.DiffOpacity != nil && validOpacity(*o.DiffOpacity) {
			c.ThemeDiffOpacity = o.DiffOpacity
		}
		if s, ok := normBorder(o.BorderStrength); ok && o.BorderStrength != "" {
			c.ThemeBorder = s
		}
	default:
		return fmt.Errorf("theme must be a name or an object")
	}
	return nil
}

// MarshalJSON writes the config with the theme as one "theme" object. Nothing
// is written for the theme when it has no name and no style.
func (c Config) MarshalJSON() ([]byte, error) {
	type plain Config
	b, err := json.Marshal(plain(c))
	if err != nil {
		return nil, err
	}
	obj := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, err
	}
	if t := c.themeObject(); t != nil {
		tb, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		obj["theme"] = tb
	}
	return json.Marshal(obj)
}

// themeObject is the theme as it is written, or nil when there is nothing to
// write.
func (c Config) themeObject() *themeObject {
	t := themeObject{Name: c.Theme, Transparent: c.ThemeTransparent,
		UserMessageOpacity: c.ThemeMessageOpacity, DiffOpacity: c.ThemeDiffOpacity,
		BorderStrength: c.ThemeBorder}
	if t.Name == "" && t.Transparent == nil && t.UserMessageOpacity == nil &&
		t.DiffOpacity == nil && t.BorderStrength == "" {
		return nil
	}
	return &t
}

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
		s, ok := normBorder(toString(v))
		if !ok {
			return fmt.Errorf("theme_border must be default, strong or op")
		}
		c.ThemeBorder = s
	}
	return nil
}
