package config

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateHome points HOME, LECTERN_HOME and SCRIBE_HOME into a temp dir, so
// nothing a test writes can reach the real ~/.lectern or ~/.scribe.
func isolateHome(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("LECTERN_HOME", "")
	t.Setenv("SCRIBE_HOME", filepath.Join(root, "scribe"))
	return root
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestEnsureGlobalCreatesFileWithPermissions(t *testing.T) {
	isolateHome(t)
	created, err := EnsureGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first run did not create the global config")
	}
	if got := modeOf(t, GlobalPath()); got != 0o600 {
		t.Fatalf("config file mode = %o, want 600", got)
	}
	if got := modeOf(t, Home()); got != 0o700 {
		t.Fatalf("config dir mode = %o, want 700", got)
	}
}

// The default file is only useful if it loads and says what the code's
// defaults are.
func TestEnsureGlobalContentLoadsWithDefaults(t *testing.T) {
	isolateHome(t)
	if _, err := EnsureGlobal(); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(GlobalPath())
	if err != nil {
		t.Fatalf("default file does not parse: %v", err)
	}
	if c.AgentFeatures == nil {
		t.Fatal("agent_features missing from default file")
	}
	for _, f := range agentFeatureDefaults {
		if got := c.AgentFeatures.Enabled(f.key); got != f.def {
			t.Errorf("agent_features.%s = %v, want default %v", f.key, got, f.def)
		}
	}
	if cmd, _ := c.Editor.CommandOrDefault(); cmd != DefaultEditorCommand {
		t.Errorf("editor command = %q, want %q", cmd, DefaultEditorCommand)
	}
	if got, want := ClampToolTimeout(c.ToolTimeout), ClampToolTimeout(nil); got != want {
		t.Errorf("tool timeout = %v, want %v", got, want)
	}
}

// Writing the defaults must not change what Scribe does. The file pins
// values equal to the code defaults, and a project file must still override
// them exactly as it does without the global file.
func TestEnsureGlobalDoesNotChangeEffectiveSettings(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	writeFile(t, ProjectPath(root), `{"agent_features":{"verify_edits":true,"tier_routing":false}}`)

	effective := func() (map[string]bool, float64) {
		t.Helper()
		c, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		feats := map[string]bool{}
		for _, f := range agentFeatureDefaults {
			feats[f.key] = c.AgentFeatures.Enabled(f.key)
		}
		return feats, ClampToolTimeout(c.ToolTimeout).Seconds()
	}

	beforeFeats, beforeTimeout := effective()
	if _, err := EnsureGlobal(); err != nil {
		t.Fatal(err)
	}
	afterFeats, afterTimeout := effective()

	for k, v := range beforeFeats {
		if afterFeats[k] != v {
			t.Errorf("agent_features.%s changed from %v to %v after first run", k, v, afterFeats[k])
		}
	}
	if afterTimeout != beforeTimeout {
		t.Errorf("tool timeout changed from %v to %v after first run", beforeTimeout, afterTimeout)
	}
}

// An existing file is the user's, even when it is empty or partial. Startup
// must leave it exactly as it was.
func TestEnsureGlobalNeverOverwritesExistingFile(t *testing.T) {
	cases := map[string]string{
		"with settings": `{"model":"ollama/qwen3.6:35b-a3b"}`,
		"empty":         "",
		"partial json":  `{"model":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeFile(t, GlobalPath(), body)
			created, err := EnsureGlobal()
			if err != nil {
				t.Fatal(err)
			}
			if created {
				t.Fatal("reported a create over an existing file")
			}
			got, err := os.ReadFile(GlobalPath())
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != body {
				t.Fatalf("existing file changed: %q", got)
			}
		})
	}
}

// createNew is the exclusive step. It must refuse a name that is already
// taken even when the caller has already checked, which is the race a
// concurrent second Scribe would hit.
func TestCreateNewRefusesExistingName(t *testing.T) {
	isolateHome(t)
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{"keep":true}`)
	created, err := createNew(path, []byte(`{"new":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("createNew replaced an existing file")
	}
	got, _ := os.ReadFile(path)
	if string(got) != `{"keep":true}` {
		t.Fatalf("existing file changed: %q", got)
	}
}

func TestEnsureGlobalIsIdempotent(t *testing.T) {
	isolateHome(t)
	if created, err := EnsureGlobal(); err != nil || !created {
		t.Fatalf("first call: created=%v err=%v", created, err)
	}
	first, _ := os.ReadFile(GlobalPath())
	created, err := EnsureGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second call created again")
	}
	second, _ := os.ReadFile(GlobalPath())
	if string(first) != string(second) {
		t.Fatal("second call changed the file")
	}
}

// A successful create leaves only config.json behind, no temporary files.
func TestEnsureGlobalLeavesNoTempFiles(t *testing.T) {
	isolateHome(t)
	if _, err := EnsureGlobal(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(Home())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Fatalf("unexpected file left in config dir: %s", e.Name())
		}
	}
}

// With no SCRIBE_HOME and no LECTERN_HOME the file goes under the user's
// home, at the path the spec gives for config.Home(). HOME is isolated, so
// this lands in the temp dir.
func TestEnsureGlobalUsesDefaultHomeLocation(t *testing.T) {
	root := isolateHome(t)
	t.Setenv("SCRIBE_HOME", "")
	want := filepath.Join(root, ".lectern", "scribe", "config.json")
	if GlobalPath() != want {
		t.Fatalf("GlobalPath = %q, want %q", GlobalPath(), want)
	}
	if created, err := EnsureGlobal(); err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

// A home that cannot hold a directory (here, a regular file sits where the
// directory should be) is an error the caller can warn about. It must not
// panic, and it must not leave a file behind.
func TestEnsureGlobalHomeIsAFileReturnsError(t *testing.T) {
	isolateHome(t)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	writeFile(t, blocker, "x")
	t.Setenv("SCRIBE_HOME", blocker)
	created, err := EnsureGlobal()
	if err == nil {
		t.Fatal("expected an error when the home is a file")
	}
	if created {
		t.Fatal("reported a create on failure")
	}
}

// Read-only directories are ignored by root, so this only runs as a normal
// user, which is how Scribe is run.
func TestEnsureGlobalReadOnlyDirReturnsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	isolateHome(t)
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	t.Setenv("SCRIBE_HOME", dir)

	created, err := EnsureGlobal()
	if err == nil {
		t.Fatal("expected an error in a read-only directory")
	}
	if created {
		t.Fatal("reported a create on failure")
	}
	if _, statErr := os.Stat(GlobalPath()); statErr == nil {
		t.Fatal("a config file appeared in a read-only directory")
	}
}
