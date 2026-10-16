package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemovePresetDeletesOnlyItsOwnScope(t *testing.T) {
	home, root := fixture(t)
	global := filepath.Join(home, "presets", "fast.json")
	project := filepath.Join(root, ".scribe", "presets", "fast.json")
	writeFile(t, global, `{"model":"a"}`)
	writeFile(t, project, `{"model":"b"}`)
	p, err := FindPreset(root, "fast")
	if err != nil || !p.Project {
		t.Fatalf("FindPreset = %+v, %v; want the project copy", p, err)
	}
	if err := RemovePreset(root, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(project); !os.IsNotExist(err) {
		t.Fatal("the project preset file should be gone")
	}
	if _, err := os.Stat(global); err != nil {
		t.Fatal("the global copy of the same name must stay")
	}
}

func TestRemovePresetRefusesAFileOutsideThePresetDir(t *testing.T) {
	home, root := fixture(t)
	elsewhere := filepath.Join(home, "other", "fast.json")
	writeFile(t, elsewhere, `{}`)
	err := RemovePreset(root, Preset{Name: "fast", Path: elsewhere})
	if err == nil {
		t.Fatal("RemovePreset must refuse a path outside the presets directory")
	}
	if _, statErr := os.Stat(elsewhere); statErr != nil {
		t.Fatal("a refused removal must not touch the file")
	}
}
