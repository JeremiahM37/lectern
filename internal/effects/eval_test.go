package effects

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
)

// evalTurn is one labelled agent turn. Only the fields the eval reads are
// decoded; the message text is never printed.
type evalTurn struct {
	Final string `json:"final"`
	Human string `json:"human"`
}

// TestEvalTurns counts how often extracted claims appear in each human-labelled
// turn class. It is skipped unless LECTERN_EFFECTS_EVAL names a JSON file, and
// it prints counts only, never message text, so the output is safe to share.
func TestEvalTurns(t *testing.T) {
	path := os.Getenv("LECTERN_EFFECTS_EVAL")
	if path == "" {
		t.Skip("set LECTERN_EFFECTS_EVAL to a labelled turns JSON file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var turns []evalTurn
	if err := json.Unmarshal(raw, &turns); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	type tally struct {
		turns, withClaim int
		kinds            map[Kind]int
	}
	byLabel := map[string]*tally{}
	for _, turn := range turns {
		tl := byLabel[turn.Human]
		if tl == nil {
			tl = &tally{kinds: map[Kind]int{}}
			byLabel[turn.Human] = tl
		}
		tl.turns++
		claims := dedupe(Extract(turn.Final))
		if len(claims) > 0 {
			tl.withClaim++
		}
		for _, c := range claims {
			tl.kinds[c.Kind]++
		}
	}

	labels := make([]string, 0, len(byLabel))
	for l := range byLabel {
		labels = append(labels, l)
	}
	sort.Strings(labels)

	kinds := []Kind{KindPushed, KindCommitted, KindTestsPass, KindRestarted, KindURLOK, KindPortListening, KindFileExists}
	fmt.Printf("\neffects eval: %d turns from %s\n", len(turns), path)
	fmt.Printf("%-26s %6s %10s", "human label", "turns", "with_claim")
	for _, k := range kinds {
		fmt.Printf(" %13s", k)
	}
	fmt.Println()
	for _, l := range labels {
		tl := byLabel[l]
		fmt.Printf("%-26s %6d %10d", l, tl.turns, tl.withClaim)
		for _, k := range kinds {
			fmt.Printf(" %13d", tl.kinds[k])
		}
		fmt.Println()
	}
}
