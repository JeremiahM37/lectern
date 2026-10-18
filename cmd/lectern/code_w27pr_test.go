package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// presetFixture makes a Scribe home with two presets and a project config, and
// returns the home and the project folder.
func presetFixture(t *testing.T) (home, cwd string) {
	t.Helper()
	home = t.TempDir()
	cwd = t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	t.Setenv("SCRIBE_PRESETS", "")
	writeCmdFile(t, filepath.Join(home, "config.json"), `{"model":"openai/gpt-6","mode":"default"}`)
	writeCmdFile(t, filepath.Join(cwd, ".scribe", "config.json"), `{"theme":"light"}`)
	writeCmdFile(t, filepath.Join(home, "presets", "fast.json"), `{"mode":"auto","theme":"dark"}`)
	writeCmdFile(t, filepath.Join(home, "presets", "lab.json"), `{"max_tokens":4000}`)
	return home, cwd
}

func writeCmdFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshotFiles maps every file under dir to its contents.
func snapshotFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		out[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// --plugin is repeatable, both forms reach the TUI as one preset list in order,
// and the run writes no config file.
func TestCodePluginFlagAppliesForOneRunOnly(t *testing.T) {
	home, cwd := presetFixture(t)
	rec := withTUI(t)
	before := snapshotFiles(t, home)
	beforeProject := snapshotFiles(t, cwd)

	var errOut bytes.Buffer
	code := codeTUI([]string{"--plugin", "fast", "--plugin=lab", "--cwd", cwd}, &errOut)
	if code != 0 {
		t.Fatalf("exit %d; stderr %q", code, errOut.String())
	}
	if !rec.ran {
		t.Fatal("the TUI should have opened")
	}
	if rec.opts.Preset != "fast,lab" {
		t.Fatalf("opts.Preset = %q, want fast,lab", rec.opts.Preset)
	}
	if !reflect.DeepEqual(snapshotFiles(t, home), before) || !reflect.DeepEqual(snapshotFiles(t, cwd), beforeProject) {
		t.Fatal("a --plugin run must not write any config file")
	}
}

// SCRIBE_PRESETS applies at start, before --plugin and --preset. A name in it
// that does not resolve is a warning, and the run still opens.
func TestCodeScribePresetsEnvAppliesAtStart(t *testing.T) {
	_, cwd := presetFixture(t)
	t.Setenv("SCRIBE_PRESETS", "fast,gone")
	rec := withTUI(t)

	var errOut bytes.Buffer
	if code := codeTUI([]string{"--plugin", "lab", "--cwd", cwd}, &errOut); code != 0 {
		t.Fatalf("exit %d; stderr %q", code, errOut.String())
	}
	if rec.opts.Preset != "fast,lab" {
		t.Fatalf("opts.Preset = %q, want fast,lab", rec.opts.Preset)
	}
	if !strings.Contains(errOut.String(), "SCRIBE_PRESETS") || !strings.Contains(errOut.String(), `"gone"`) {
		t.Fatalf("a stale environment name should be reported: %q", errOut.String())
	}
}

// Without the env var or any flag, no preset is applied.
func TestCodeNoPresetsWithoutEnvOrFlag(t *testing.T) {
	_, cwd := presetFixture(t)
	rec := withTUI(t)
	if code := codeTUI([]string{"--cwd", cwd}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if rec.opts.Preset != "" {
		t.Fatalf("opts.Preset = %q, want empty", rec.opts.Preset)
	}
}

// --verbose-presets reports what each preset changes, by key, before the TUI.
func TestCodeVerbosePresetsReportsChanges(t *testing.T) {
	_, cwd := presetFixture(t)
	withTUI(t)
	var errOut bytes.Buffer
	if code := codeTUI([]string{"--plugin", "fast", "--verbose-presets", "--cwd", cwd}, &errOut); code != 0 {
		t.Fatalf("exit %d; stderr %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "preset fast: changes mode, theme") {
		t.Fatalf("verbose report missing: %q", errOut.String())
	}
}

// Without --verbose-presets the TUI run is silent about presets.
func TestCodeQuietPresetsByDefault(t *testing.T) {
	_, cwd := presetFixture(t)
	withTUI(t)
	var errOut bytes.Buffer
	if code := codeTUI([]string{"--plugin", "fast", "--cwd", cwd}, &errOut); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(errOut.String(), "changes") {
		t.Fatalf("no report expected without --verbose-presets: %q", errOut.String())
	}
}

// A --plugin name that does not exist stops the run with exit 2 before any
// session opens.
func TestCodePluginUnknownNameRefused(t *testing.T) {
	_, cwd := presetFixture(t)
	rec := withTUI(t)
	var errOut bytes.Buffer
	if code := codeTUI([]string{"--plugin", "nope", "--cwd", cwd}, &errOut); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if rec.ran || !strings.Contains(errOut.String(), `no preset "nope"`) {
		t.Fatalf("ran=%v stderr=%q", rec.ran, errOut.String())
	}
}

func TestCodePluginNeedsValue(t *testing.T) {
	var errOut bytes.Buffer
	if code := codeTUI([]string{"--plugin"}, &errOut); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

// --presets lists the presets for the folder, with their scope, and exits 0
// without starting a session.
func TestCodePresetsListsForFolder(t *testing.T) {
	_, cwd := presetFixture(t)
	rec := withTUI(t)
	var out, errOut bytes.Buffer
	code := codeCommand([]string{"--presets", "--cwd", cwd}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d; stderr %q", code, errOut.String())
	}
	if rec.ran {
		t.Fatal("--presets must not open a session")
	}
	for _, want := range []string{"fast", "lab", "global"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("listing should include %q: %q", want, out.String())
		}
	}
}

// With no presets defined, the listing says how to add one.
func TestCodePresetsEmptyListingSaysHowToAdd(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := codeCommand([]string{"--presets", "--cwd", t.TempDir()}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "No presets.") {
		t.Fatalf("empty listing = %q", out.String())
	}
}

// Headless runs take --plugin too, and it reaches the app the same way.
func TestCodePluginReachesHeadless(t *testing.T) {
	_, cwd := presetFixture(t)
	var out, errOut bytes.Buffer
	code := codeCommand([]string{"-p", "hi", "--plugin", "nope", "--cwd", cwd}, strings.NewReader(""), &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), `no preset "nope"`) {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
}

// lectern help code names the three preset switches and SCRIBE_PRESETS.
func TestHelpCodeNamesPresetFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if code, handled := helpCommand([]string{"help", "code"}, &out, &errOut); !handled || code != 0 {
		t.Fatalf("help code: handled=%v code=%d", handled, code)
	}
	for _, want := range []string{"--plugin NAME", "--presets", "--verbose-presets"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help code should name %s", want)
		}
	}
}
