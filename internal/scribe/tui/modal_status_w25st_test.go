package tui

import "testing"

func TestStatusNoProviderOrModelIsSaidPlainly(t *testing.T) {
	name, state := providerState(NewFakeBackend(), "")
	if name != "" || state != "no model selected" {
		t.Fatalf("empty model gave %q, %q", name, state)
	}
	_, state = providerState(NewFakeBackend(), "nowhere/some-model")
	if state != "not a known provider" {
		t.Fatalf("unknown provider gave %q", state)
	}
}

func TestSubagentTextCountsByStatus(t *testing.T) {
	got := subagentText([]string{"running", "done", "done", "error"})
	if got != "4 total: 1 running, 2 done, 1 error" {
		t.Fatalf("subagentText = %q", got)
	}
	if subagentText(nil) != "none" {
		t.Fatal("no subagents should read none")
	}
}

func TestGitTextSaysCleanAheadAndBehind(t *testing.T) {
	if got := gitText(GitStatus{Branch: "main"}); got != "main · clean" {
		t.Fatalf("clean branch = %q", got)
	}
	if got := gitText(GitStatus{Branch: "main", Dirty: true, Changed: []string{"a", "b"}, Ahead: 1, Behind: 2}); got != "main · 2 changed · 1 ahead · 2 behind" {
		t.Fatalf("dirty branch = %q", got)
	}
	if got := gitText(GitStatus{}); got != "(no branch)" {
		t.Fatalf("no git = %q", got)
	}
}

// The snapshot carries the background jobs, not only their count.
func TestSnapshotReadsJobsAndSubagents(t *testing.T) {
	snap := takeStatusSnap(NewFakeBackend())
	if len(snap.jobs) != 1 || snap.jobs[0].status != "running" || snap.jobs[0].command != "go test -count=1 ./..." {
		t.Fatalf("jobs = %+v", snap.jobs)
	}
}

func TestMapTextShowsCountsOrProgress(t *testing.T) {
	if got := mapText(MapStats{Files: 214, Symbols: 3920, Edges: 11840}); got != "214 files · 3,920 symbols · 11,840 edges" {
		t.Fatalf("mapText = %q", got)
	}
	if got := mapText(MapStats{Building: true, Progress: 0.42}); got != "building, 42%" {
		t.Fatalf("building mapText = %q", got)
	}
}
