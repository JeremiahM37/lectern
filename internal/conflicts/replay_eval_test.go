package conflicts

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

// TestReplayEval scores Predict against ground truth built by
// tools/conflict_replay.py from a repository's own merge history: each row is
// one two-parent merge, with both sides' diffs and whether the merged tree
// conflicted textually or failed build/tests while both sides passed. It is
// skipped unless LECTERN_CONFLICT_REPLAY names a JSONL file, because the data
// is a private repository's history and does not belong in the test suite.
//
// It compares two predictors on the same pairs: plain file overlap (what
// cross-agent awareness warns on today) and Predict's symbol-level level.
func TestReplayEval(t *testing.T) {
	path := os.Getenv("LECTERN_CONFLICT_REPLAY")
	if path == "" {
		t.Skip("set LECTERN_CONFLICT_REPLAY to a conflict_replay.py JSONL file")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// One confusion matrix per predictor; "truth" is a textual or semantic
	// conflict in the replayed merge.
	type cell struct{ tp, fp, fn, tn int }
	score := map[string]*cell{"file_overlap": {}, "predict_any": {}, "predict_semantic": {}}
	add := func(c *cell, flagged, truth bool) {
		switch {
		case flagged && truth:
			c.tp++
		case flagged:
			c.fp++
		case truth:
			c.fn++
		default:
			c.tn++
		}
	}
	n, pos := 0, 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<20), 64<<20)
	for sc.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		a, _ := raw["side_a"].(string)
		b, _ := raw["side_b"].(string)
		textual, _ := raw["textual_conflict"].(bool)
		semantic, _ := raw["semantic_conflict"].(bool)
		overlap, _ := raw["file_overlap"].(bool)
		truth := textual || semantic
		risk := Predict(ParseFootprint(a), ParseFootprint(b))
		n++
		if truth {
			pos++
		}
		add(score["file_overlap"], overlap, truth)
		add(score["predict_any"], risk.Level != LevelNone, truth)
		add(score["predict_semantic"], risk.Level == LevelSemantic, truth)
		if truth || risk.Level == LevelSemantic {
			t.Logf("merge=%v textual=%v semantic=%v predicted=%s reasons=%v",
				raw["merge"], textual, semantic, risk.Level, risk.Reasons)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("pairs=%d conflicts(textual or semantic)=%d", n, pos)
	for _, k := range []string{"file_overlap", "predict_any", "predict_semantic"} {
		c := score[k]
		t.Logf("%-17s flagged=%3d tp=%3d fp=%3d fn=%3d tn=%3d", k, c.tp+c.fp, c.tp, c.fp, c.fn, c.tn)
	}
}
