package session

import (
	"fmt"
	"path/filepath"
	"testing"
)

func setupHistory(t *testing.T) {
	t.Helper()
	t.Setenv("SCRIBE_HOME", filepath.Join(t.TempDir(), "scribe"))
}

func texts(es []HistoryEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Text
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHistoryDedupesConsecutiveOnly(t *testing.T) {
	setupHistory(t)
	for _, p := range []string{"a", "a", "b", "a", "   ", ""} {
		if err := AppendHistory(p, "/proj"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Recent("", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Newest first; the second "a" is not kept, but a later "a" is (not consecutive).
	if want := []string{"a", "b", "a"}; !eq(texts(got), want) {
		t.Fatalf("Recent = %v, want %v", texts(got), want)
	}
}

func TestHistoryRecentFiltersByCWD(t *testing.T) {
	setupHistory(t)
	AppendHistory("in alpha", "/alpha")
	AppendHistory("in beta", "/beta")
	AppendHistory("alpha again", "/alpha")

	got, _ := Recent("/alpha", 10)
	if want := []string{"alpha again", "in alpha"}; !eq(texts(got), want) {
		t.Fatalf("Recent(/alpha) = %v", texts(got))
	}
	got, _ = Recent("", 2)
	if want := []string{"alpha again", "in beta"}; !eq(texts(got), want) {
		t.Fatalf("Recent limit = %v", texts(got))
	}
	if got[0].CWD != "/alpha" || got[0].At.IsZero() {
		t.Fatalf("entry metadata missing: %+v", got[0])
	}
}

func TestHistoryCap(t *testing.T) {
	setupHistory(t)
	for i := 0; i < HistoryCap+5; i++ {
		if err := AppendHistory(fmt.Sprintf("prompt %04d", i), "/p"); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := Recent("", 0)
	if len(got) != HistoryCap {
		t.Fatalf("kept %d entries, want %d", len(got), HistoryCap)
	}
	if got[0].Text != fmt.Sprintf("prompt %04d", HistoryCap+4) {
		t.Fatalf("newest = %q", got[0].Text)
	}
	if got[len(got)-1].Text != "prompt 0005" {
		t.Fatalf("oldest kept = %q", got[len(got)-1].Text)
	}
}

func TestHistorySearchFuzzy(t *testing.T) {
	setupHistory(t)
	AppendHistory("hello there", "/p")
	AppendHistory("fix the build in router", "/p")
	AppendHistory("refactor parser", "/p")
	AppendHistory("fix router bug", "/p")

	// Subsequence match: f-b-r matches "fix the build in router".
	got, _ := Search("fbr", 0)
	if len(got) == 0 || got[0].Text != "fix the build in router" {
		t.Fatalf("fuzzy fbr = %v", texts(got))
	}
	for _, e := range got {
		if e.Text == "hello there" || e.Text == "refactor parser" {
			t.Fatalf("non-matching prompt returned: %q", e.Text)
		}
	}

	// A substring match outranks a scattered one; newer wins ties.
	got, _ = Search("fix router", 0)
	if len(got) != 2 || got[0].Text != "fix router bug" {
		t.Fatalf("substring ranking = %v", texts(got))
	}

	if got, _ := Search("zzz", 0); len(got) != 0 {
		t.Fatalf("no match should be empty, got %v", texts(got))
	}
	if got, _ := Search("", 2); len(got) != 2 || got[0].Text != "fix router bug" {
		t.Fatalf("empty query = recent, got %v", texts(got))
	}
}

func TestHistorySearchDedupesTexts(t *testing.T) {
	setupHistory(t)
	AppendHistory("deploy it", "/a")
	AppendHistory("other", "/a")
	AppendHistory("deploy it", "/b")
	got, _ := Search("deploy", 0)
	if len(got) != 1 {
		t.Fatalf("duplicate texts should show once, got %v", texts(got))
	}
}
