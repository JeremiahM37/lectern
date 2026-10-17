package config

import "testing"

// isolate points both SCRIBE_HOME and HOME at temp dirs, so a test never reads
// or writes the real user config.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// The code map settings survive a save and a load from the global file, with
// the on/off switch kept as an explicit false rather than dropped.
func TestRepoMapSettingsRoundTrip(t *testing.T) {
	isolate(t)
	off := false
	auto := true
	want := Config{
		RepoMap:              &off,
		RepoMapTokenBudget:   8000,
		SemanticSummaries:    "llm",
		SemanticSummaryLimit: 200,
		SemanticAutoRegen:    &auto,
	}
	if err := SaveGlobal(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	if Bool(got.RepoMap, true) || got.RepoMapTokenBudget != 8000 || got.SemanticSummaries != "llm" ||
		got.SemanticSummaryLimit != 200 || !Bool(got.SemanticAutoRegen, false) {
		t.Fatalf("round trip lost a repo-map setting: %+v", got)
	}
}

// A project file overrides the global value field by field, and a project that
// says nothing about these keys keeps the global ones.
func TestRepoMapSettingsMergeProjectOverGlobal(t *testing.T) {
	base := Config{RepoMapTokenBudget: 4000, SemanticSummaries: "ast", SemanticSummaryLimit: 300}
	over := Config{RepoMapTokenBudget: 2000}
	got := Merge(base, over)
	if got.RepoMapTokenBudget != 2000 {
		t.Errorf("budget = %d, want the project's 2000", got.RepoMapTokenBudget)
	}
	if got.SemanticSummaries != "ast" || got.SemanticSummaryLimit != 300 {
		t.Errorf("unset project keys must keep the global values: %+v", got)
	}
}

// An unset auto-regenerate is absent from the file and reads as off.
func TestRepoMapAutoRegenDefaultsOff(t *testing.T) {
	isolate(t)
	if err := SaveGlobal(Config{Model: "x"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if Bool(got.SemanticAutoRegen, false) || got.RepoMapTokenBudget != 0 || got.SemanticSummaries != "" {
		t.Fatalf("defaults changed: %+v", got)
	}
}
