package memory

import (
	"math"
	"testing"
)

// unitAt returns a unit vector in dim dimensions with the given weights on
// the first few axes, so the test controls every pairwise cosine exactly.
func unitAt(dim int, w map[int]float64) []float32 {
	v := make([]float32, dim)
	var n float64
	for i, x := range w {
		v[i] = float32(x)
		n += x * x
	}
	inv := float32(1 / math.Sqrt(n))
	for i := range v {
		v[i] *= inv
	}
	return v
}

// setEmbedding stores v as the provider vector of record id, as the provider
// path would, without a network call.
func setEmbedding(t *testing.T, d *DB, id, model string, v []float32) {
	t.Helper()
	if _, err := d.db.Exec("UPDATE memories SET embedding = ?, embedding_model = ? WHERE id = ?", encodeVec(v), model, id); err != nil {
		t.Fatal(err)
	}
}

const testProvModel = "ollama/test-embed"

func TestCalibrateMapsBaselineToZeroAndIdenticalToOne(t *testing.T) {
	cases := []struct {
		cos, base, want float64
	}{
		{0.47, 0.5, 0},      // unrelated text under the default baseline is below the floor
		{0.5, 0.5, 0},       // exactly the baseline
		{0.3, 0.5, 0},       // below the baseline clips at 0, never negative
		{1, 0.5, 1},         // identical vectors stay 1
		{0.782, 0.5, 0.564}, // the measured related pair from the nomic calibration
	}
	for _, c := range cases {
		if got := calibrate(c.cos, c.base); math.Abs(got-c.want) > 1e-3 {
			t.Errorf("calibrate(%v, %v) = %v, want %v", c.cos, c.base, got, c.want)
		}
	}
	if got := calibrate(0.9, 1); got != 0 {
		t.Errorf("degenerate baseline 1 must not divide by zero, got %v", got)
	}
}

func TestBaselineStaleOnlyOnNotableChange(t *testing.T) {
	cases := []struct {
		measured, n int
		stale       bool
	}{
		{10, 10, false},
		{10, 12, false}, // two rows is under the minimum step
		{10, 15, true},  // five rows and half again
		{30, 35, false}, // five rows is only a sixth of 30
		{30, 40, true},
		{20, 14, true}, // shrinking counts too
	}
	for _, c := range cases {
		if got := baselineStale(c.measured, c.n); got != c.stale {
			t.Errorf("baselineStale(%d, %d) = %v, want %v", c.measured, c.n, got, c.stale)
		}
	}
}

func TestBaselineDefaultWithFewRows(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	for _, title := range []string{"Pasta water salt", "Vinyl record collection", "Lisbon tram route"} {
		r := mustAdd(t, d, Record{Category: CatFact, Title: title, Body: "Note about " + title})
		setEmbedding(t, d, r.ID, testProvModel, unitAt(16, map[int]float64{0: 1, 1: float64(len(r.ID)%3) + 1}))
	}
	if got := d.baselineFor(testProvModel); got != baselineDefault {
		t.Fatalf("3 rows should use the default baseline %v, got %v", baselineDefault, got)
	}
	if _, ok := d.base.m[testProvModel]; ok {
		t.Fatal("a default baseline must not be cached")
	}
}

// fixtureUnrelatedAndNear builds six provider-tagged rows. Five rows point
// along different axes and share a common component, so every pairwise cosine
// is about 0.48, which is nomic's unrelated-text level. The near row points
// almost exactly at the query.
func fixtureUnrelatedAndNear(t *testing.T) (*DB, []float32, string) {
	t.Helper()
	s, _ := newStores(t)
	d := s.Project
	const dim = 64
	query := unitAt(dim, map[int]float64{0: 0.69, 7: 0.72})
	titles := []string{"Pasta water salt", "Vinyl record collection", "Lisbon tram route", "Garden hose length", "Winter boots size"}
	for i, title := range titles {
		r := mustAdd(t, d, Record{Category: CatFact, Title: title, Body: "Note about " + title})
		setEmbedding(t, d, r.ID, testProvModel, unitAt(dim, map[int]float64{0: 0.69, i + 1: 0.72}))
	}
	near := mustAdd(t, d, Record{Category: CatFact, Title: "Harbor lighting schedule", Body: "Lamps switch on at dusk near the pier."})
	setEmbedding(t, d, near.ID, testProvModel, unitAt(dim, map[int]float64{0: 0.69, 7: 0.72, 8: 0.05}))
	return d, query, near.ID
}

func TestProviderRecallRejectsUnrelatedAndKeepsNearDuplicate(t *testing.T) {
	d, query, nearID := fixtureUnrelatedAndNear(t)

	// The raw cosine of unrelated rows is about 0.48. Under the default floor
	// they would all be candidates; calibrated against the measured baseline
	// they fall to zero.
	base := d.baselineFor(testProvModel)
	if base < 0.4 || base > 0.56 {
		t.Fatalf("measured baseline %v should sit at the unrelated level, about 0.48", base)
	}

	got, err := d.recallWith("how do I run the Go tests", nil, 5, query, testProvModel)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Record.ID != nearID {
		var titles []string
		for _, g := range got {
			titles = append(titles, g.Record.Title)
		}
		t.Fatalf("only the near duplicate should be recalled, got %v", titles)
	}
	if got[0].Sim < 0.8 {
		t.Fatalf("near duplicate should score high on the calibrated scale, got sim %.3f", got[0].Sim)
	}
	if !containsWhy(got[0].Why, "similar") {
		t.Fatalf("near duplicate should be recalled for similarity, why=%v", got[0].Why)
	}
}

// Unrelated rows score far below the similarity floor, so they are never
// recalled for similarity alone.
func TestProviderScoreIsBelowTheFloorForUnrelatedRow(t *testing.T) {
	d, query, _ := fixtureUnrelatedAndNear(t)
	base := d.baselineFor(testProvModel)
	for _, r := range mustList(t, d) {
		if r.Title == "Harbor lighting schedule" {
			continue
		}
		cos := dot(query, decodeVec(rowEmbedding(t, d, r.ID)))
		if s := calibrate(cos, base); s >= similarFloor/4 {
			t.Errorf("%s: cosine %.3f over baseline %.3f should score near 0, got %.3f", r.Title, cos, base, s)
		}
	}
}

func TestBaselineCachedAndRemeasuredOnGrowth(t *testing.T) {
	d, _, _ := fixtureUnrelatedAndNear(t)
	first := d.baselineFor(testProvModel)
	if c, ok := d.base.m[testProvModel]; !ok || c.rows != 6 {
		t.Fatalf("baseline should be cached at 6 rows, got %+v ok=%v", c, ok)
	}
	// Same count: the cached value is returned without a fresh measurement.
	d.base.mu.Lock()
	d.base.m[testProvModel] = baseline{value: 0.123, rows: 6}
	d.base.mu.Unlock()
	if got := d.baselineFor(testProvModel); got != 0.123 {
		t.Fatalf("an unchanged row count should reuse the cache, got %v", got)
	}
	if first == 0.123 {
		t.Fatal("first measurement should not be the sentinel")
	}
	// Grow the model's rows past the staleness step: the cache is rebuilt.
	for i := 0; i < 6; i++ {
		// Add dedupes near-identical bodies, so each filler body uses its own
		// vocabulary.
		topics := []string{"volcanic basalt cooling", "orchestral brass rehearsal", "fishing tackle storage", "satellite antenna alignment", "bakery sourdough starter", "chess endgame drills"}
		r := mustAdd(t, d, Record{Category: CatFact, Title: "Extra memory " + string(rune('a'+i)), Body: topics[i]})
		setEmbedding(t, d, r.ID, testProvModel, unitAt(64, map[int]float64{i + 10: 1}))
	}
	if got := d.baselineFor(testProvModel); got == 0.123 {
		t.Fatal("a notable row-count change should re-measure the baseline")
	}
}

func TestHashRecallSimIsUnchangedRawCosine(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	r := mustAdd(t, d, Record{Category: CatFact, Title: "Go test runner", Body: "Run go test ./... from the repo root."})
	const q = "how do I run the go tests"
	got, err := d.Recall(q, nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Record.ID != r.ID {
		t.Fatalf("hash recall should find the record, got %+v", got)
	}
	// The hash path never calibrates: Sim is the plain clipped cosine.
	want := math.Max(0, dot(embed(q), decodeVec(rowEmbedding(t, d, r.ID))))
	if got[0].Sim != want {
		t.Fatalf("hash Sim changed: got %v, want raw cosine %v", got[0].Sim, want)
	}
}

func rowEmbedding(t *testing.T, d *DB, id string) []byte {
	t.Helper()
	var emb []byte
	if err := d.db.QueryRow("SELECT embedding FROM memories WHERE id = ?", id).Scan(&emb); err != nil {
		t.Fatal(err)
	}
	return emb
}

func mustList(t *testing.T, d *DB) []Record {
	t.Helper()
	rows, err := d.db.Query("SELECT id, title FROM memories WHERE " + live)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.Title); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func containsWhy(why []string, want string) bool {
	for _, w := range why {
		if w == want {
			return true
		}
	}
	return false
}
