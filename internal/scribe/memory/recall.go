package memory

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// Recall weights. The score of a candidate is
//
//	0.6*text + 0.25*file + 0.3*similar + 0.15*decay + 0.1*pinned
//
// where text = x/(1+x) for x = BM25 from FTS5, file is 1 when the record
// names a file passed in, similar is the cosine of the hashed embeddings,
// and decay blends recency of use with the use count.
const (
	wText    = 0.6
	wFile    = 0.25
	wSimilar = 0.3
	wDecay   = 0.15
	wPinned  = 0.1

	// MinScore is the score a non-pinned record needs to be recalled.
	MinScore = 0.2
	// similarFloor is the cosine below which a record is not a candidate
	// on similarity alone.
	similarFloor = 0.2

	embedDim = 512

	// injectLimit caps how many records Inject renders.
	injectLimit = 5
	// injectBodyChars trims each body in the injected block.
	injectBodyChars = 400
	// injectTotalChars is the body budget for one injected block. The first
	// record always goes in; after that a record that would take the total
	// past the budget is left out, and so is everything ranked below it.
	injectTotalChars = 1500
	// ftsCandidates caps the FTS hits considered per DB.
	ftsCandidates = 100
	// trigramWeight scales a substring-only (trigram) hit against a word hit.
	trigramWeight = 0.75
	// similarHint is the cosine at which a save reports a near match.
	similarHint = 0.55
)

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "but": true, "not": true,
	"you": true, "all": true, "any": true, "can": true, "had": true, "her": true,
	"was": true, "one": true, "our": true, "out": true, "has": true, "have": true,
	"this": true, "that": true, "with": true, "from": true, "they": true, "will": true,
	"would": true, "there": true, "their": true, "what": true, "about": true, "which": true,
	"when": true, "make": true, "like": true, "into": true, "than": true, "then": true,
	"them": true, "these": true, "some": true, "should": true, "could": true, "also": true,
	"its": true, "it's": true, "is": true, "to": true, "of": true, "in": true, "on": true,
	"be": true, "by": true, "an": true, "as": true, "at": true, "or": true, "if": true,
	"do": true, "does": true, "so": true, "we": true, "my": true, "me": true,
}

// wordTokens splits s into lowercase letter/digit runs of length >= 2.
func wordTokens(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if utf8.RuneCountInString(b.String()) >= 2 {
			out = append(out, b.String())
		}
		b.Reset()
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// contentTokens is wordTokens without stopwords.
func contentTokens(s string) []string {
	var out []string
	for _, w := range wordTokens(s) {
		if !stopwords[w] {
			out = append(out, w)
		}
	}
	return out
}

// ftsQuery builds an FTS5 MATCH expression: each content token as a quoted
// prefix term, OR-joined. Empty when the query has no usable tokens.
func ftsQuery(q string) string {
	seen := map[string]bool{}
	var parts []string
	for _, w := range contentTokens(q) {
		if seen[w] {
			continue
		}
		seen[w] = true
		parts = append(parts, `"`+w+`"*`)
	}
	return strings.Join(parts, " OR ")
}

// bm25Norm maps a positive BM25 magnitude into (0,1).
func bm25Norm(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return x / (1 + x)
}

// fnv32a hashes s with FNV-1a.
func fnv32a(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// embed returns an L2-normalised hashed bag of unigrams (weight 1) and
// bigrams (weight 0.7) of content tokens, in embedDim dimensions. Feature
// hashing with a sign bit keeps collisions unbiased. It is offline and
// deterministic; it catches paraphrases that share vocabulary, nothing more.
func embed(text string) []float32 {
	toks := contentTokens(text)
	vec := make([]float32, embedDim)
	add := func(feature string, w float32) {
		bin := fnv32a(feature) % embedDim
		sign := float32(1)
		if fnv32a(feature+"\x00s")&1 == 1 {
			sign = -1
		}
		vec[bin] += sign * w
	}
	for i, t := range toks {
		add("w:"+t, 1.0)
		if i+1 < len(toks) {
			add("b:"+t+" "+toks[i+1], 0.7)
		}
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return vec
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range vec {
		vec[i] *= inv
	}
	return vec
}

// embedText is the text embedded for a record: the title twice (so a long
// body does not drown it), the body and the tags.
func embedText(title, body string, tags []string) string {
	return title + " " + title + " " + body + " " + strings.Join(tags, " ")
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	if len(b) != 4*embedDim {
		return nil
	}
	v := make([]float32, embedDim)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

func dot(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// matchFile reports whether a stored path refers to a passed path. Paths
// match when equal or when one is a trailing path segment of the other, so
// "foo.go" matches "internal/x/foo.go".
func matchFile(stored, passed string) bool {
	if stored == passed {
		return true
	}
	return strings.HasSuffix(passed, "/"+stored) || strings.HasSuffix(stored, "/"+passed)
}

// decay blends recency of last use with the use count, into [0,1].
func decay(uses int, lastUsed, now time.Time) float64 {
	ageDays := now.Sub(lastUsed).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	recency := math.Exp(-ageDays / 30)
	usage := math.Log1p(float64(uses)) / math.Log1p(10)
	if usage > 1 {
		usage = 1
	}
	return 0.5*recency + 0.5*usage
}

// Recall returns the records most relevant to query and the files being
// worked on. The top limit non-pinned records scoring at least MinScore are
// returned, plus every pinned record. Results are sorted by score. Recall
// does not change usage counters; Stores.Inject does.
func (d *DB) Recall(query string, files []string, limit int) ([]Scored, error) {
	if limit <= 0 {
		limit = 5
	}
	qFiles := normalizeFiles(files)
	qVec := embed(query)
	now := d.now()

	// Text hits: word index, plus trigram substring matches.
	textX, err := d.textHits(query, ftsCandidates)
	if err != nil {
		return nil, err
	}
	textCov := map[string]float64{}
	if len(textX) > 0 {
		// BM25's IDF collapses toward zero on a small corpus, so the
		// magnitude alone under-rates a lone good match. Coverage (the share
		// of query terms the record contains) carries the rest.
		want := queryTerms(query)
		for id := range textX {
			cov, err := d.coverage(id, want)
			if err != nil {
				return nil, err
			}
			textCov[id] = cov
		}
	}

	// File hits.
	fileHit := map[string]bool{}
	if len(qFiles) > 0 {
		rows, err := d.db.Query("SELECT memory_id, path FROM memory_files")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, p string
			if err := rows.Scan(&id, &p); err != nil {
				rows.Close()
				return nil, err
			}
			for _, f := range qFiles {
				if matchFile(p, f) {
					fileHit[id] = true
					break
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// Every row, light columns only; bodies are loaded for the winners.
	type cand struct {
		id     string
		score  float64
		pinned bool
		why    []string
	}
	rows, err := d.db.Query("SELECT id, pinned, uses, last_used, embedding FROM memories WHERE " + live)
	if err != nil {
		return nil, err
	}
	var cands []cand
	for rows.Next() {
		var id, lastUsed string
		var pinned, uses int
		var emb []byte
		if err := rows.Scan(&id, &pinned, &uses, &lastUsed, &emb); err != nil {
			rows.Close()
			return nil, err
		}
		c := cand{id: id, pinned: pinned != 0}
		sim := 0.0
		if v := decodeVec(emb); v != nil && qVec != nil {
			sim = math.Max(0, dot(qVec, v))
		}
		x, hasText := textX[id]
		if hasText {
			c.why = append(c.why, "text")
		}
		if fileHit[id] {
			c.why = append(c.why, "file")
		}
		if sim >= similarFloor {
			c.why = append(c.why, "similar")
		}
		if c.pinned {
			c.why = append(c.why, "pinned")
		}
		c.score = wSimilar*sim + wDecay*decay(uses, parseTime(lastUsed), now)
		if hasText {
			c.score += wText * (0.5*textCov[id] + 0.5*bm25Norm(x))
		}
		if fileHit[id] {
			c.score += wFile
		}
		if c.pinned {
			c.score += wPinned
		}
		if !c.pinned && !hasText && !fileHit[id] && sim < similarFloor {
			continue // no lexical, file or similarity signal
		}
		if c.pinned || c.score >= MinScore {
			cands = append(cands, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].id < cands[j].id
	})
	var chosen []cand
	nonPinned := 0
	for _, c := range cands {
		if c.pinned {
			chosen = append(chosen, c)
			continue
		}
		if nonPinned < limit {
			chosen = append(chosen, c)
			nonPinned++
		}
	}
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].score > chosen[j].score })

	out := make([]Scored, 0, len(chosen))
	for _, c := range chosen {
		r, err := loadOne(d.db, c.id)
		if err != nil {
			return nil, err
		}
		r.Scope = d.scope
		out = append(out, Scored{Record: r, Score: c.score, Why: c.why})
	}
	return out, nil
}

// Stores bundles the project and global memory databases. Either may be nil
// (no project root, or the global home is unavailable).
type Stores struct {
	Project *DB
	Global  *DB
	// Policy says where saves with no scope go and what automatic recall
	// reads. The zero value is the default: save to the project store when
	// there is one, and recall from every open store.
	Policy Policy

	// session, surfaced and surfacedDir are set by SetSession. surfaced is
	// the ids Inject has already put in front of the model in this session.
	// CoChange returns the files that change with file in git history, most
	// frequent first. The app sets it from the repo map; nil means none.
	CoChange func(file string) []string

	session     string
	surfMu      sync.Mutex // guards surfaced and surfacedDir: recall and compaction run on different goroutines
	surfaced    map[string]bool
	surfacedDir string
}

// Open opens the project store for root (<root>/.scribe/memory.db) and the
// global store (config.Home()/memory.db). An empty root skips the project
// store.
func Open(root string) (*Stores, error) {
	g, err := OpenDB(filepath.Join(config.Home(), "memory.db"), ScopeGlobal)
	if err != nil {
		return nil, err
	}
	s := &Stores{Global: g, Policy: LoadPolicy(root)}
	if root != "" {
		p, err := OpenDB(filepath.Join(config.ProjectDir(root), "memory.db"), ScopeProject)
		if err != nil {
			g.Close()
			return nil, err
		}
		s.Project = p
	}
	_ = NoteSessionStart() // feeds the cleanup hint; a failed write only delays it
	return s, nil
}

// Close closes every open store.
func (s *Stores) Close() error {
	var first error
	for _, d := range s.DBs() {
		if err := d.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// DBs returns the open stores, project first.
func (s *Stores) DBs() []*DB {
	var out []*DB
	if s.Project != nil {
		out = append(out, s.Project)
	}
	if s.Global != nil {
		out = append(out, s.Global)
	}
	return out
}

// DB returns the store for scope "project" or "global".
func (s *Stores) DB(scope string) (*DB, error) {
	switch scope {
	case ScopeProject:
		if s.Project == nil {
			return nil, errors.New("no project memory store: Scribe was not started inside a project")
		}
		return s.Project, nil
	case ScopeGlobal:
		if s.Global == nil {
			return nil, errors.New("no global memory store")
		}
		return s.Global, nil
	}
	return nil, fmt.Errorf("unknown scope %q; use project or global", scope)
}

// Recall runs DB.Recall on every open store and merges the results.
func (s *Stores) Recall(query string, files []string, limit int) ([]Scored, error) {
	if limit <= 0 {
		limit = 5
	}
	var all []Scored
	for _, d := range s.readDBs() {
		got, err := d.Recall(query, files, limit)
		if err != nil {
			return nil, err
		}
		all = append(all, got...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	return s.addCoChange(all, s.neighbours(files)), nil
}

// Inject renders the memories relevant to query and files as a
// <recalled_memories> block for the agent to prepend to its context. It
// returns "" when nothing clears the threshold. Returned records have their
// use counters bumped.
func (s *Stores) Inject(query string, files []string) string {
	got, err := s.Recall(query, files, injectLimit)
	if err != nil || len(got) == 0 {
		return ""
	}
	// A memory injected earlier in this session is still in the model's
	// context, so it is not injected again (see SetSession).
	fresh := got[:0:0]
	for _, m := range got {
		if !s.Surfaced(m.ID) {
			fresh = append(fresh, m)
		}
	}
	got = fresh
	if len(got) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<recalled_memories>\n")
	byDB := map[string][]string{}
	var surfacedNow []string
	used := 0
	shown := 0
	for _, m := range got {
		body := trimRunes(m.Body, injectBodyChars)
		n := utf8.RuneCountInString(body)
		if shown > 0 && used+n > injectTotalChars {
			break
		}
		used += n
		shown++
		fmt.Fprintf(&b, "[%s] %s (id %s, %s)\n", m.Category, m.Title, shortID(m.ID), m.Scope)
		b.WriteString(body)
		b.WriteString("\n")
		byDB[m.Scope] = append(byDB[m.Scope], m.ID)
		surfacedNow = append(surfacedNow, m.ID)
	}
	b.WriteString("</recalled_memories>")
	for _, d := range s.DBs() {
		_ = d.touch(byDB[d.Scope()])
	}
	s.rememberSurfaced(surfacedNow)
	return b.String()
}

// Prune runs DB.Prune on every open store.
func (s *Stores) Prune(maxAge time.Duration, minUses int) (int, error) {
	n := 0
	for _, d := range s.DBs() {
		k, err := d.Prune(maxAge, minUses)
		if err != nil {
			return n, err
		}
		n += k
	}
	return n, nil
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// trimRunes cuts s to at most n runes, adding an ellipsis when it cuts.
func trimRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// triQuery builds a trigram MATCH expression: each content token of three or
// more characters as a quoted substring, OR-joined. Shorter tokens cannot be
// matched by the trigram index and are left to the word index.
func triQuery(q string) string {
	seen := map[string]bool{}
	var parts []string
	for _, w := range contentTokens(q) {
		if utf8.RuneCountInString(w) < 3 || seen[w] {
			continue
		}
		seen[w] = true
		parts = append(parts, `"`+w+`"`)
	}
	return strings.Join(parts, " OR ")
}

// textHits returns, per record id, the BM25 magnitude (higher is better) of
// the query against the word index and the trigram index. A record found by
// both keeps its word score; one found only by substring is scaled by
// trigramWeight. Superseded records are not filtered here; callers do that.
func (d *DB) textHits(query string, limit int) (map[string]float64, error) {
	out := map[string]float64{}
	if match := ftsQuery(query); match != "" {
		hits, err := d.bm25Hits("memories_fts", match, limit)
		if err != nil {
			return nil, err
		}
		for id, x := range hits {
			out[id] = x
		}
	}
	if match := triQuery(query); match != "" {
		hits, err := d.bm25Hits("memories_tri", match, limit)
		if err != nil {
			return nil, err
		}
		for id, x := range hits {
			if _, ok := out[id]; !ok {
				out[id] = trigramWeight * x
			}
		}
	}
	return out, nil
}

// bm25Hits runs match against one FTS table and returns the top limit ids
// with their BM25 magnitude. table is one of the package's own FTS tables.
func (d *DB) bm25Hits(table, match string, limit int) (map[string]float64, error) {
	rows, err := d.db.Query(
		"SELECT id, bm25("+table+") FROM "+table+" WHERE "+table+" MATCH ? ORDER BY bm25("+table+") LIMIT ?",
		match, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id string
		var bm float64
		if err := rows.Scan(&id, &bm); err != nil {
			return nil, err
		}
		out[id] = -bm // bm25 is negative; the more negative, the better
	}
	return out, rows.Err()
}

// sortedByScore returns the ids in hits, highest magnitude first, ties by id.
func sortedByScore(hits map[string]float64) []string {
	ids := make([]string, 0, len(hits))
	for id := range hits {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if hits[ids[i]] != hits[ids[j]] {
			return hits[ids[i]] > hits[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids
}

// queryTerms returns the distinct content tokens of a query.
func queryTerms(q string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range contentTokens(q) {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// coverage is the fraction of want that some token of record id starts
// with, using the same prefix rule as the FTS query.
func (d *DB) coverage(id string, want []string) (float64, error) {
	if len(want) == 0 {
		return 0, nil
	}
	var title, body, tags string
	err := d.db.QueryRow("SELECT title, body, tags FROM memories_fts WHERE id = ?", id).Scan(&title, &body, &tags)
	if err != nil {
		return 0, err
	}
	toks := wordTokens(title + " " + body + " " + tags)
	hit := 0
	for _, w := range want {
		for _, t := range toks {
			if strings.HasPrefix(t, w) {
				hit++
				break
			}
		}
	}
	return float64(hit) / float64(len(want)), nil
}
