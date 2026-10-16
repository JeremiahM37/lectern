package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readKeys(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	obj, err := readObject(path)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

// A settings change must not delete the "remote" section, which Config does
// not model. Before this, every save dropped it.
func TestSaveGlobalKeepsRemoteSection(t *testing.T) {
	home, _ := fixture(t)
	writeFile(t, GlobalPath(), `{"model":"a","remote":{"telegram":{"enabled":true,"chat_ids":[5]}}}`)
	c, err := LoadFile(GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	c.Model = "b"
	if err := SaveGlobal(c); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRemote()
	if err != nil {
		t.Fatal(err)
	}
	if r.Telegram == nil || !r.Telegram.Enabled || len(r.Telegram.ChatIDs) != 1 || r.Telegram.ChatIDs[0] != 5 {
		t.Fatalf("remote section lost after save: %+v", r.Telegram)
	}
	got, _ := LoadFile(filepath.Join(home, "config.json"))
	if got.Model != "b" {
		t.Fatalf("model not saved: %q", got.Model)
	}
}

func TestSaveProjectKeepsUnknownKeys(t *testing.T) {
	_, root := fixture(t)
	writeFile(t, ProjectPath(root), `{"theme":"x","team_note":"keep me"}`)
	c, _ := LoadFile(ProjectPath(root))
	c.Verbose = new(bool)
	if err := SaveProject(root, c); err != nil {
		t.Fatal(err)
	}
	keys := readKeys(t, ProjectPath(root))
	if _, ok := keys["team_note"]; !ok {
		t.Fatalf("hand-added key dropped: %v", keys)
	}
	if _, ok := keys["verbose"]; !ok {
		t.Fatal("new value not written")
	}
}

// A field that is cleared is removed from the file, not written as an empty value.
func TestSaveRemovesClearedKeys(t *testing.T) {
	home, _ := fixture(t)
	writeFile(t, GlobalPath(), `{"model":"a","theme":"t"}`)
	c, _ := LoadFile(GlobalPath())
	c.Model = ""
	if err := SaveGlobal(c); err != nil {
		t.Fatal(err)
	}
	keys := readKeys(t, filepath.Join(home, "config.json"))
	if _, ok := keys["model"]; ok {
		t.Fatalf("cleared model still in file: %v", keys)
	}
	if _, ok := keys["theme"]; !ok {
		t.Fatal("untouched theme was dropped")
	}
}

// A file that is not an object is an error, and its content is left alone.
func TestSaveRefusesNonObjectFile(t *testing.T) {
	_, root := fixture(t)
	writeFile(t, ProjectPath(root), `[1,2,3`)
	err := SaveProject(root, Config{Model: "x"})
	if err == nil || !strings.Contains(err.Error(), "not overwriting") {
		t.Fatalf("want refusal, got %v", err)
	}
	b, _ := os.ReadFile(ProjectPath(root))
	if string(b) != `[1,2,3` {
		t.Fatalf("file was overwritten: %q", b)
	}
}

func TestSaveLeavesNoTempFilesAndIs0600(t *testing.T) {
	home, _ := fixture(t)
	if err := SaveGlobal(Config{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("expected only config.json, got %v", entries)
	}
	info, _ := os.Stat(filepath.Join(home, "config.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
}

// A malformed config names its file, so the user knows what to fix.
func TestLoadErrorNamesTheFile(t *testing.T) {
	_, root := fixture(t)
	writeFile(t, ProjectPath(root), `{"model": }`)
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), ProjectPath(root)) {
		t.Fatalf("error should name %s, got %v", ProjectPath(root), err)
	}
}
