package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The theme is stored as one "theme" object. Flat keys written by older
// versions are still read, and the next save writes the object and drops them.

func TestLegacyFlatThemeKeysAreRead(t *testing.T) {
	var c Config
	in := `{"theme":"proxysoul-main","theme_transparent":true,"theme_message_opacity":30,"theme_diff_opacity":70,"theme_border":"strong"}`
	if err := json.Unmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	if c.Theme != "proxysoul-main" || c.ThemeTransparent == nil || !*c.ThemeTransparent {
		t.Fatalf("name or transparency not read: %+v", c)
	}
	if c.ThemeMessageOpacity == nil || *c.ThemeMessageOpacity != 30 || c.ThemeDiffOpacity == nil || *c.ThemeDiffOpacity != 70 {
		t.Fatalf("opacities not read: %+v", c)
	}
	if c.ThemeBorder != "strong" {
		t.Fatalf("border = %q", c.ThemeBorder)
	}
}

func TestThemeObjectIsRead(t *testing.T) {
	var c Config
	in := `{"theme":{"name":"ember","transparent":true,"userMessageOpacity":70,"diffOpacity":0,"borderStrength":"op"}}`
	if err := json.Unmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	if c.Theme != "ember" || !*c.ThemeTransparent || *c.ThemeMessageOpacity != 70 ||
		*c.ThemeDiffOpacity != 0 || c.ThemeBorder != "op" {
		t.Fatalf("object not read: %+v", c)
	}
}

func TestThemeObjectWinsOverFlatKeys(t *testing.T) {
	var c Config
	in := `{"theme":{"transparent":false},"theme_transparent":true}`
	if err := json.Unmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	if c.ThemeTransparent == nil || *c.ThemeTransparent {
		t.Fatalf("object should win, got %v", c.ThemeTransparent)
	}
}

func TestOutOfRangeThemeValuesAreIgnored(t *testing.T) {
	var c Config
	in := `{"theme":{"userMessageOpacity":250,"borderStrength":"wide"},"theme_border":"bogus","theme_diff_opacity":-5}`
	if err := json.Unmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	if c.ThemeMessageOpacity != nil || c.ThemeDiffOpacity != nil || c.ThemeBorder != "" {
		t.Fatalf("invalid values should be ignored: %+v", c)
	}
}

func TestSaveWritesObjectAndDropsFlatKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{"model":"anthropic/x","theme":"proxysoul-main","theme_transparent":true,"theme_border":"strong","remote":{"keep":true}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Theme != "proxysoul-main" || c.ThemeBorder != "strong" {
		t.Fatalf("legacy values not migrated on read: %+v", c)
	}
	if err := save(path, c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	text := string(b)
	for _, gone := range []string{"theme_transparent", "theme_border"} {
		if strings.Contains(text, gone) {
			t.Fatalf("flat key %q survived the save:\n%s", gone, text)
		}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	var th themeObject
	if err := json.Unmarshal(obj["theme"], &th); err != nil {
		t.Fatalf("theme not an object: %s", obj["theme"])
	}
	if th.Name != "proxysoul-main" || th.Transparent == nil || !*th.Transparent || th.BorderStrength != "strong" {
		t.Fatalf("object wrong: %+v", th)
	}
	if _, ok := obj["remote"]; !ok {
		t.Fatal("unmodelled key was dropped")
	}
	again, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Theme != c.Theme || *again.ThemeTransparent != true || again.ThemeBorder != "strong" {
		t.Fatalf("round trip changed the theme: %+v", again)
	}
}

func TestNoThemeObjectWhenNothingIsSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := save(path, Config{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `"theme"`) {
		t.Fatalf("empty theme written: %s", b)
	}
}

func TestSetThemeStyleStillStoresIntoTheObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var c Config
	if _, err := c.SetField("theme_message_opacity", 70); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetField("theme_border", "default"); err != nil {
		t.Fatal(err)
	}
	c.Theme = "scribe"
	if err := save(path, c); err != nil {
		t.Fatal(err)
	}
	back, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.ThemeMessageOpacity == nil || *back.ThemeMessageOpacity != 70 || back.ThemeBorder != "" || back.Theme != "scribe" {
		t.Fatalf("settings did not round trip: %+v", back)
	}
}
