package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestHistoryDedupesRepeats(t *testing.T) {
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
	// Newest first. The second "a" is dropped as a consecutive repeat, and
	// the later "a" moves the earlier copy to the top: each text appears once.
	if want := []string{"a", "b"}; !eq(texts(got), want) {
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
	// Write past the prune threshold directly; the file is rewritten only
	// once it passes HistoryCap+HistoryPruneSlack, so one more append trims it.
	total := HistoryCap + HistoryPruneSlack + 1
	var buf strings.Builder
	for i := 0; i < total; i++ {
		b, _ := json.Marshal(HistoryEntry{Text: fmt.Sprintf("prompt %04d", i), CWD: "/p", At: time.Now().UTC()})
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(historyPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath(), []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory("newest", "/p"); err != nil {
		t.Fatal(err)
	}
	got, _ := Recent("", 0)
	if len(got) != HistoryCap {
		t.Fatalf("kept %d entries, want %d", len(got), HistoryCap)
	}
	if got[0].Text != "newest" {
		t.Fatalf("newest = %q", got[0].Text)
	}
	// total+1 entries, newest HistoryCap kept: the oldest survivor is index total+1-HistoryCap.
	want := fmt.Sprintf("prompt %04d", total+1-HistoryCap)
	if got[len(got)-1].Text != want {
		t.Fatalf("oldest kept = %q, want %q", got[len(got)-1].Text, want)
	}
}

func TestRepeatMovesToTop(t *testing.T) {
	setupHistory(t)
	for _, p := range []string{"alpha", "beta", "alpha", "gamma"} {
		if err := AppendHistory(p, "/p"); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := Recent("", 0)
	want := []string{"gamma", "alpha", "beta"}
	if fmt.Sprint(texts(got)) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v (each prompt once, repeat moved to top)", texts(got), want)
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
