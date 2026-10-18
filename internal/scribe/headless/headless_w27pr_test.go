package headless

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// --plugin is repeatable, takes the = form too, and --verbose-presets is a
// bare switch. Everything else passes through in order.
func TestTakePresetFlags(t *testing.T) {
	pf, err := takePresetFlags([]string{"-p", "hi", "--plugin", "a", "--plugin=b", "--verbose-presets", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(pf.plugins, want) {
		t.Fatalf("plugins = %v, want %v", pf.plugins, want)
	}
	if !pf.verbose {
		t.Fatal("--verbose-presets should be set")
	}
	if want := []string{"-p", "hi", "--json"}; !reflect.DeepEqual(pf.rest, want) {
		t.Fatalf("rest = %v, want %v", pf.rest, want)
	}
}

func TestTakePresetFlagsRefusesMissingValue(t *testing.T) {
	if _, err := takePresetFlags([]string{"--plugin"}); err == nil {
		t.Fatal("--plugin with no name should be an error")
	}
	if _, err := takePresetFlags([]string{"--plugin", "--json"}); err == nil {
		t.Fatal("--plugin must not take the next flag as its name")
	}
	if _, err := takePresetFlags([]string{"--verbose-presets=yes"}); err == nil {
		t.Fatal("--verbose-presets takes no value")
	}
}

// An unknown --plugin stops the run before any model is contacted, with exit 1
// and a message that names the preset.
func TestRunUnknownPluginFailsBeforeAnyModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	t.Setenv("SCRIBE_PRESETS", "")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-p", "hi", "--plugin", "missing", "--cwd", t.TempDir()},
		strings.NewReader(""), &stdout, &stderr, nil)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `no preset "missing"`) {
		t.Fatalf("stderr should name the missing preset: %q", stderr.String())
	}
}

// The --verbose-presets report names the keys a preset changes and never the
// values, and a run-only report leaves the config files as they were.
func TestPrintPresetChangesReportsKeysOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	writePreset(t, filepath.Join(home, "presets", "fast.json"), `{"mode":"auto","search_url":"https://secret.example/x"}`)
	cfg := filepath.Join(home, "config.json")
	writePreset(t, cfg, `{"mode":"default"}`)

	var stderr bytes.Buffer
	printPresetChanges(t.TempDir(), []string{"fast"}, &stderr)
	out := stderr.String()
	if !strings.Contains(out, "preset fast: changes mode, search_url") {
		t.Fatalf("report = %q", out)
	}
	if strings.Contains(out, "secret.example") {
		t.Fatalf("the report must not print values: %q", out)
	}
	if b, _ := os.ReadFile(cfg); string(b) != `{"mode":"default"}` {
		t.Fatalf("config changed: %s", b)
	}
}

func writePreset(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
