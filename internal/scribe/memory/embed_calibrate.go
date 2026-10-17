package memory

import (
	"math"
	"sort"
	"sync"
)

// Provider cosines are not on the scale the hash embedding was tuned for.
// nomic-embed-text scores unrelated text at about 0.47 and a paraphrase at
// about 0.78, while the hash scores unrelated text near 0. Recall's floors
// (similarFloor, MinScore) would admit almost every memory under the raw
// cosine. So each model gets a baseline, the typical cosine between unrelated
// stored memories, and the score is rescaled so the baseline maps to 0 and an
// identical vector maps to 1.
const (
	// baselineSample bounds how many rows the baseline is measured over. The
	// pairs among them are all compared, which is at most 1225 dot products.
	baselineSample = 50
	// baselineMinRows is the fewest rows of one model that give a measured
	// baseline. Below it the default is used, because a median over two or
	// three memories says little about unrelated text.
	baselineMinRows = 5
	// baselineDefault is the baseline used with too few rows. It is a little
	// above the hash's own unrelated-text cosine and well below nomic's.
	baselineDefault = 0.5
	// baselineMax keeps 1-baseline positive, so a degenerate model (every
	// vector nearly the same) cannot divide by zero or invert the scale.
	baselineMax = 0.95
)

// baseline is a measured baseline and the row count it was measured at.
type baseline struct {
	value float64
	rows  int
}

// baselineCache holds per-model baselines for one DB. It lives in memory
// only: a baseline is cheap to re-measure, and persisting it would tie it to
// a model state that changes with every backfill.
type baselineCache struct {
	mu sync.Mutex
	m  map[string]baseline
}

// calibrate maps a provider cosine onto the hash scale. Cosines at or below
// the baseline score 0, and an identical vector scores 1.
func calibrate(cos, base float64) float64 {
	if base >= 1 {
		return 0
	}
	s := (cos - base) / (1 - base)
	if s < 0 {
		return 0
	}
	return s
}

// baselineStale reports whether a baseline measured at rows is out of date
// for n live rows: the count moved by at least baselineMinRows and by at
// least a fifth of what it was.
func baselineStale(measured, n int) bool {
	diff := n - measured
	if diff < 0 {
		diff = -diff
	}
	return diff >= baselineMinRows && diff*5 >= measured
}

// baselineFor returns the baseline for model in this DB. It is measured once
// and kept until the row count moves enough to matter (see baselineStale).
// Any read error falls back to the default; recall must not fail on it.
func (d *DB) baselineFor(model string) float64 {
	n, err := d.countModelRows(model)
	if err != nil || n < baselineMinRows {
		return baselineDefault
	}
	d.base.mu.Lock()
	c, ok := d.base.m[model]
	d.base.mu.Unlock()
	if ok && !baselineStale(c.rows, n) {
		return c.value
	}
	v := d.measureBaseline(model)
	d.base.mu.Lock()
	if d.base.m == nil {
		d.base.m = map[string]baseline{}
	}
	d.base.m[model] = baseline{value: v, rows: n}
	d.base.mu.Unlock()
	return v
}

// measureBaseline is the lower-quartile pairwise cosine over up to
// baselineSample live rows of model. A project's memories are often about
// related things, so the median of all pairs sits above the level of truly
// unrelated text (measured: 0.588 on six Go memories against about 0.46 for
// unrelated text with nomic-embed-text) and under-scores related matches; the
// lower quartile tracks the unrelated pairs.
func (d *DB) measureBaseline(model string) float64 {
	rows, err := d.db.Query(
		"SELECT embedding FROM memories WHERE embedding_model = ? AND "+live+" ORDER BY id LIMIT ?",
		model, baselineSample)
	if err != nil {
		return baselineDefault
	}
	defer rows.Close()
	var vecs [][]float32
	for rows.Next() {
		var emb []byte
		if err := rows.Scan(&emb); err != nil {
			return baselineDefault
		}
		if v := decodeVec(emb); v != nil {
			vecs = append(vecs, v)
		}
	}
	if rows.Err() != nil || len(vecs) < 2 {
		return baselineDefault
	}
	var cos []float64
	for i := range vecs {
		for j := i + 1; j < len(vecs); j++ {
			cos = append(cos, dot(vecs[i], vecs[j]))
		}
	}
	sort.Float64s(cos)
	m := cos[len(cos)/4]
	return math.Min(math.Max(m, 0), baselineMax)
}

// countModelRows counts the live rows whose vector came from model.
func (d *DB) countModelRows(model string) (int, error) {
	var n int
	err := d.db.QueryRow("SELECT COUNT(*) FROM memories WHERE embedding_model = ? AND "+live, model).Scan(&n)
	return n, err
}

// queryBaseline is the lower-quartile cosine between the query and the live
// rows of model. Stored memories resemble one another more than a question
// resembles a memory (measured with nomic-embed-text: memory pairs about
// 0.55, an unrelated question about 0.46), so a baseline from memory pairs
// alone sits too high and under-scores real matches. With fewer than
// baselineMinRows rows it returns fallback.
func (d *DB) queryBaseline(q []float32, model string, fallback float64) float64 {
	rows, err := d.db.Query("SELECT embedding FROM memories WHERE embedding_model = ? AND "+live+" LIMIT ?", model, baselineSample*4)
	if err != nil {
		return fallback
	}
	defer rows.Close()
	var cos []float64
	for rows.Next() {
		var emb []byte
		if rows.Scan(&emb) != nil {
			return fallback
		}
		if v := decodeVec(emb); v != nil {
			cos = append(cos, dot(q, v))
		}
	}
	if rows.Err() != nil || len(cos) < baselineMinRows {
		return fallback
	}
	sort.Float64s(cos)
	return math.Min(math.Max(cos[len(cos)/4], 0), baselineMax)
}
