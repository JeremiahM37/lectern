package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// mapTestEnv points SCRIBE_HOME and HOME at temp dirs and resets the budget
// mirror, so one test cannot leak a budget into another.
func mapTestEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { setMapBudget(0) })
	root := t.TempDir()
	src := "package pkg\n\n// Add adds two numbers.\nfunc Add(a, b int) int { return a + b }\n"
	if err := os.WriteFile(filepath.Join(root, "pkg.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// mapBackend is a bare App with a built code map and the settings loaded from
// config, the way Open leaves them.
func mapBackend(t *testing.T, root string) (*scribeBackend, *App) {
	t.Helper()
	be, a := newTestBackend(t, root)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg = cfg
	m, err := repomap.Open(root, repomap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	a.ext.cmap = m
	return be, a
}

// The settings the modal saves are read back by a new process: same mode,
// limit, auto-regenerate and budget, with nothing kept in memory between them.
func TestMapPrefsSurviveARestart(t *testing.T) {
	root := mapTestEnv(t)
	be, _ := mapBackend(t, root)
	want := tui.MapPrefs{Mode: "llm", SummaryLimit: 200, AutoRegen: true, Budget: 4000}
	if err := be.SetMapPrefs(want); err != nil {
		t.Fatal(err)
	}
	saved := mustLoadGlobal(t)
	if saved.SemanticSummaries != "llm" || saved.SemanticSummaryLimit != 200 ||
		!config.Bool(saved.SemanticAutoRegen, false) || saved.RepoMapTokenBudget != 4000 {
		t.Fatalf("global config lost the settings: %+v", saved)
	}

	// A second process: fresh config load, fresh map, same startup path.
	setMapBudget(0)
	be2, a2 := mapBackend(t, root)
	applyMapConfig(a2, a2.ext.cmap)
	if got := be2.MapPrefs(); got != want {
		t.Fatalf("after restart MapPrefs = %+v, want %+v", got, want)
	}
	if a2.ext.cmap.SummaryMode() != "llm" || !a2.ext.cmap.AutoRegen() {
		t.Fatalf("map did not take the saved mode and auto-regenerate")
	}
	if mapBudget() != 4000 {
		t.Fatalf("budget after restart = %d, want 4000", mapBudget())
	}
}

// The on/off switch is saved to config and reported back, but the running map
// is left alone: the map is built at startup, so the change waits for a start.
func TestMapEnabledIsSavedForTheNextStart(t *testing.T) {
	root := mapTestEnv(t)
	be, _ := mapBackend(t, root)
	if d := be.MapDetail(); !d.Enabled || !d.Running {
		t.Fatalf("a fresh map should be enabled and running: %+v", d)
	}
	if err := be.SetMapEnabled(false); err != nil {
		t.Fatal(err)
	}
	if config.Bool(mustLoadGlobal(t).RepoMap, true) {
		t.Fatal("repo_map was not saved as false")
	}
	d := be.MapDetail()
	if d.Enabled || !d.Running {
		t.Fatalf("after turning it off: %+v, want Enabled=false and still Running", d)
	}
	if err := be.SetMapEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !be.MapDetail().Enabled {
		t.Fatal("turning it back on was not saved")
	}
}

// A project file that sets the same key wins. The save still goes to the
// global file, the error says so, and the live map follows the value in use.
func TestMapPrefsReportProjectOverride(t *testing.T) {
	root := mapTestEnv(t)
	if err := config.SaveProject(root, config.Config{RepoMapTokenBudget: 2000}); err != nil {
		t.Fatal(err)
	}
	be, _ := mapBackend(t, root)
	err := be.SetMapPrefs(tui.MapPrefs{Mode: "ast", SummaryLimit: 300, Budget: 8000})
	if err != errMapOverridden {
		t.Fatalf("err = %v, want errMapOverridden", err)
	}
	if mustLoadGlobal(t).RepoMapTokenBudget != 8000 {
		t.Fatal("the global file should still hold the chosen value")
	}
	if got := be.MapPrefs().Budget; got != 2000 {
		t.Fatalf("effective budget = %d, want the project's 2000", got)
	}
	if mapBudget() != 2000 {
		t.Fatalf("live budget = %d, want 2000 to match the config in use", mapBudget())
	}
}

// A mode the map does not know is refused before anything is written.
func TestMapPrefsRefusesUnknownModeWithoutWriting(t *testing.T) {
	root := mapTestEnv(t)
	be, _ := mapBackend(t, root)
	if err := be.SetMapPrefs(tui.MapPrefs{Mode: "bogus", SummaryLimit: 300}); err == nil {
		t.Fatal("an unknown mode must be an error")
	}
	if _, err := os.Stat(config.GlobalPath()); err == nil {
		t.Fatal("a refused change must not create the config file")
	}
	if err := be.SetMapPrefs(tui.MapPrefs{Mode: "ast", SummaryLimit: 300, Budget: -1}); err == nil {
		t.Fatal("a negative budget must be an error")
	}
}

// Auto-regenerate asks the model only in llm and full mode, and only when on.
func TestAutoRegenWantedOnlyInSummaryModes(t *testing.T) {
	root := mapTestEnv(t)
	_, a := mapBackend(t, root)
	m := a.ext.cmap
	m.SetAutoRegen(true)
	m.SetSummaryMode(repomap.SummaryAST)
	if autoRegenWanted(m) {
		t.Fatal("ast mode shows no summaries, so no regeneration")
	}
	m.SetSummaryMode(repomap.SummaryLLM)
	if !autoRegenWanted(m) {
		t.Fatal("llm mode with auto-regenerate on should regenerate after a build")
	}
	m.SetAutoRegen(false)
	if autoRegenWanted(m) {
		t.Fatal("auto-regenerate off must not regenerate")
	}
}

// With auto-regenerate on in llm mode, a finished build fills in the missing
// summaries in the background and the map shows them.
func TestFinishMapBuildRegeneratesSummaries(t *testing.T) {
	root := mapTestEnv(t)
	_, a := mapBackend(t, root)
	m := a.ext.cmap
	m.SetSummarizer(stubSummarizer{})
	m.SetSummaryMode(repomap.SummaryLLM)
	m.SetAutoRegen(true)
	a.finishMapBuild(m)
	deadline := time.Now().Add(5 * time.Second)
	for m.Status().Summaries == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := m.Status().Summaries; got == 0 {
		t.Fatal("auto-regenerate did not write any summaries after the build")
	}
}

// stubSummarizer answers every file with a fixed sentence.
type stubSummarizer struct{}

func (stubSummarizer) Summarize(_ context.Context, files []repomap.SummaryRequest) ([]string, error) {
	out := make([]string, len(files))
	for i := range out {
		out[i] = "Adds numbers."
	}
	return out, nil
}

func mustLoadGlobal(t *testing.T) config.Config {
	t.Helper()
	c, err := config.LoadFile(config.GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	return c
}
