package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeEmbedder is a controllable provider. vec maps text to a vector; fail
// makes every call error; onBatch runs inside EmbedBatch, so a test can edit
// rows while a provider call is in flight.
type fakeEmbedder struct {
	model   string
	vec     func(string) []float32
	mu      sync.Mutex
	fail    bool
	calls   int
	onBatch func()
}

func (f *fakeEmbedder) Model() string { return f.model }

func (f *fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return embedOne(ctx, f, text)
}

func (f *fakeEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.calls++
	fail, hook := f.fail, f.onBatch
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if fail {
		return nil, errors.New("provider unavailable")
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = normalize(f.vec(t))
	}
	return out, nil
}

func (f *fakeEmbedder) setFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}

func (f *fakeEmbedder) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// unitVec returns a basis vector of dimension 8 at position i.
func unitVec(i int) []float32 {
	v := make([]float32, 8)
	v[i%8] = 1
	return v
}

func lengthVec(text string) []float32 { return unitVec(len(text)) }

func newFake(model string) *fakeEmbedder {
	return &fakeEmbedder{model: model, vec: lengthVec}
}

func waitBackground(s *Stores) {
	for _, d := range s.DBs() {
		d.bg.Wait()
	}
}

func modelOf(t *testing.T, d *DB, id string) string {
	t.Helper()
	var m string
	if err := d.db.QueryRow("SELECT embedding_model FROM memories WHERE id = ?", id).Scan(&m); err != nil {
		t.Fatalf("read model of %s: %v", id, err)
	}
	return m
}

// setVector writes emb and model straight onto a row, to set up mixed-model
// fixtures.
func setVector(t *testing.T, d *DB, id string, emb []float32, model string) {
	t.Helper()
	if _, err := d.db.Exec("UPDATE memories SET embedding = ?, embedding_model = ? WHERE id = ?",
		encodeVec(emb), model, id); err != nil {
		t.Fatalf("set vector: %v", err)
	}
}

func TestInsertEmbedsWithTheProviderInBackground(t *testing.T) {
	s, _ := newStores(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out := make([][]float32, len(body.Input))
		for i := range out {
			out[i] = unitVec(i)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	}))
	defer srv.Close()

	e := NewOllamaEmbedder("nomic-embed-text", srv.URL, nil)
	if err := s.SetEmbedder(e); err != nil {
		t.Fatalf("SetEmbedder: %v", err)
	}
	rec := mustAdd(t, s.Project, Record{Title: "Use the outbox", Body: "Writes go through the outbox table."})
	waitBackground(s)

	if got := modelOf(t, s.Project, rec.ID); got != "ollama/nomic-embed-text" {
		t.Fatalf("row model = %q, want the provider tag", got)
	}
}

func TestSetEmbedderFailedSmokeTestKeepsHash(t *testing.T) {
	s, _ := newStores(t)
	bad := newFake("fake/broken")
	bad.setFail(true)
	if err := s.SetEmbedder(bad); err == nil {
		t.Fatal("want the smoke test error")
	}
	if s.activeEmbedder() != nil {
		t.Fatal("a failed smoke test must revert to the hash embedding")
	}
	rec := mustAdd(t, s.Project, Record{Title: "Plain save", Body: "Hash only."})
	if got := modelOf(t, s.Project, rec.ID); got != HashModel {
		t.Fatalf("row model = %q, want %q", got, HashModel)
	}
}

func TestSetEmbedderSameModelIsNoOp(t *testing.T) {
	s, _ := newStores(t)
	first := newFake("fake/m")
	if err := s.SetEmbedder(first); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEmbedder(newFake("fake/m")); err != nil {
		t.Fatal(err)
	}
	if s.activeEmbedder() != first {
		t.Fatal("re-activating the same model should keep the existing embedder")
	}
	if err := s.SetEmbedder(nil); err != nil || s.activeEmbedder() != nil {
		t.Fatalf("nil should turn the provider off: %v", err)
	}
}

func TestBackfillIsBoundedAndOnlyTouchesStaleRows(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	for _, title := range []string{"Alpha note", "Beta note", "Gamma note"} {
		mustAdd(t, d, Record{Title: title, Body: "Body for " + title})
	}

	// Backfill with a provider over HTTP, as a gateway would serve it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		data := make([]map[string]any, len(body.Input))
		for i := range body.Input {
			data[i] = map[string]any{"index": i, "embedding": []float32{1, 0, 0, 0}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	e := NewOpenAIEmbedder("openai", "text-embedding-3-small", srv.URL, "", nil)

	n, err := d.Backfill(context.Background(), e, 2)
	if err != nil || n != 2 {
		t.Fatalf("first run: n=%d err=%v, want 2 rows (limit)", n, err)
	}
	n, err = d.Backfill(context.Background(), e, 10)
	if err != nil || n != 1 {
		t.Fatalf("second run: n=%d err=%v, want the remaining 1", n, err)
	}
	n, err = d.Backfill(context.Background(), e, 10)
	if err != nil || n != 0 {
		t.Fatalf("third run: n=%d err=%v, want nothing left", n, err)
	}
}

func TestBackfillSkipsRowEditedWhileEmbedding(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	rec := mustAdd(t, d, Record{Title: "Original title", Body: "Original body text."})

	fake := newFake("fake/m")
	fake.onBatch = func() {
		// The user edits the row while the provider is still answering.
		fake.onBatch = nil
		if err := d.Update(Record{ID: rec.ID, Title: "Edited title", Body: "Edited body text."}); err != nil {
			t.Errorf("Update: %v", err)
		}
	}
	if _, err := d.Backfill(context.Background(), fake, 10); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	// The stale vector must not be written over the edit. The row stays on
	// the hash so the next run embeds the new text.
	if got := modelOf(t, d, rec.ID); got != HashModel {
		t.Fatalf("stale vector stored: model = %q", got)
	}
}

func TestRecallFallsBackToHashWhenTheProviderFails(t *testing.T) {
	s, _ := newStores(t)
	mustAdd(t, s.Project, Record{Title: "Deploy the relay", Body: "Relay deploys run from the release branch."})

	fake := newFake("fake/m")
	if err := s.SetEmbedder(fake); err != nil {
		t.Fatal(err)
	}
	waitBackground(s)
	fake.setFail(true)

	got, err := s.Recall("how do relay deploys work", nil, 5)
	if err != nil {
		t.Fatalf("recall must not fail when the provider does: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the full-text match should still be recalled through the fallback")
	}
}

func TestRecallNeverMixesModels(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	query := "queue backpressure"

	pinnedA := mustAdd(t, d, Record{Title: "Queue alpha", Body: "alpha body text", Pinned: true})
	pinnedB := mustAdd(t, d, Record{Title: "Queue beta", Body: "beta body text", Pinned: true})
	pinnedHash := mustAdd(t, d, Record{Title: "Queue gamma", Body: "gamma body text", Pinned: true})

	// Every text embeds to the same basis vector, so a same-model match is
	// exactly 1 and a cross-model one would be too if it were compared.
	fake := &fakeEmbedder{model: "fake/m", vec: func(string) []float32 { return unitVec(0) }}
	setVector(t, d, pinnedA.ID, unitVec(0), "fake/m")
	setVector(t, d, pinnedB.ID, unitVec(0), "other/x")

	if err := s.SetEmbedder(fake); err != nil {
		t.Fatal(err)
	}
	// Activation backfills rows from other models, including the hash row.
	// Wait for that, then put back the fixtures this test is about.
	waitBackground(s)
	setVector(t, d, pinnedA.ID, unitVec(0), "fake/m")
	setVector(t, d, pinnedB.ID, unitVec(0), "other/x")
	setVector(t, d, pinnedHash.ID, unitVec(0), HashModel)

	got, err := s.Recall(query, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	sims := map[string]float64{}
	for _, sc := range got {
		sims[sc.ID] = sc.Sim
	}
	if sims[pinnedA.ID] != 1 {
		t.Errorf("same-model row sim = %v, want 1", sims[pinnedA.ID])
	}
	if sims[pinnedB.ID] != 0 {
		t.Errorf("other-model row was compared: sim = %v, want 0", sims[pinnedB.ID])
	}
	if sims[pinnedHash.ID] != 0 {
		t.Errorf("hash row was compared while a provider is active: sim = %v, want 0", sims[pinnedHash.ID])
	}

	// With the provider down, the query is hashed, and only hash rows count.
	fake.setFail(true)
	got, err = s.Recall(query, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range got {
		if sc.ID == pinnedA.ID && sc.Sim != 0 {
			t.Errorf("provider row compared against a hash query: sim = %v", sc.Sim)
		}
		if sc.ID == pinnedB.ID && sc.Sim != 0 {
			t.Errorf("other-model row compared on fallback: sim = %v", sc.Sim)
		}
	}
}

func TestSimilarOnlyComparesSameModel(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	base := mustAdd(t, d, Record{Title: "Base record", Body: "base body"})
	sameModel := mustAdd(t, d, Record{Title: "Same model", Body: "same body"})
	otherModel := mustAdd(t, d, Record{Title: "Other model", Body: "other body"})

	setVector(t, d, base.ID, unitVec(3), "fake/m")
	setVector(t, d, sameModel.ID, unitVec(3), "fake/m")
	setVector(t, d, otherModel.ID, unitVec(3), "other/x")

	got, err := d.Similar(base.ID, 0.5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != sameModel.ID {
		t.Fatalf("Similar = %v, want only the same-model row", got)
	}
}

// A database from before embedding_model existed must come up with its
// existing vectors tagged as the hash, so recall still uses them.
func TestMigrationTagsExistingVectorsAsHash(t *testing.T) {
	path := t.TempDir() + "/memory.db"
	d, err := OpenDB(path, ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	rec := mustAdd(t, d, Record{Title: "Legacy row", Body: "written before the model column"})
	// Rewind to the layout before this change: no column, previous version.
	if _, err := d.db.Exec("ALTER TABLE memories DROP COLUMN embedding_model"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec("PRAGMA user_version = 7"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d2, err := OpenDB(path, ScopeProject)
	if err != nil {
		t.Fatalf("reopen after migration: %v", err)
	}
	defer d2.Close()
	if got := modelOf(t, d2, rec.ID); got != HashModel {
		t.Fatalf("migrated row model = %q, want %q", got, HashModel)
	}
}
