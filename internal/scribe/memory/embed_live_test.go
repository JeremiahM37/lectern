package memory

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveProviderRecall runs against a real Ollama server. It is skipped
// unless SCRIBE_LIVE_EMBED=1 and OLLAMA_HOST name the server, so a normal test
// run never touches the network.
//
//	SCRIBE_LIVE_EMBED=1 OLLAMA_HOST=http://100.127.85.58:11434 \
//	  go test ./internal/scribe/memory/ -run TestLiveProviderRecall -v
func TestLiveProviderRecall(t *testing.T) {
	if os.Getenv("SCRIBE_LIVE_EMBED") != "1" {
		t.Skip("set SCRIBE_LIVE_EMBED=1 to run against a live embedding server")
	}
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		t.Skip("set OLLAMA_HOST to the Ollama server")
	}
	const model = "ollama/nomic-embed-text"
	e, err := NewEmbedder(model, OllamaRoot(host), "")
	if err != nil {
		t.Fatal(err)
	}

	s, _ := newStores(t)
	if err := s.SetEmbedder(e); err != nil {
		t.Fatalf("SetEmbedder: %v", err)
	}
	d := s.Project

	goMemories := []string{
		"Run the whole suite with go test ./... from the repo root; it takes about 40 seconds.",
		"Use go test -race for the concurrency tests in the scheduler package.",
		"Table-driven tests in Go live beside the code in _test.go files and use t.Run subtests.",
	}
	otherMemories := []string{
		"Salt the pasta water generously and reserve a cup of starchy liquid before draining.",
		"The vinyl pressing of the album has a warmer bass than the streaming master.",
		"The night train from Lisbon to Porto leaves before seven and needs a reservation.",
	}
	goIDs := map[string]bool{}
	titles := map[string]string{}
	for i, body := range append(append([]string{}, goMemories...), otherMemories...) {
		title := "memory " + string(rune('A'+i))
		r := mustAdd(t, d, Record{Category: CatFact, Title: title, Body: body})
		titles[r.ID] = title
		if i < len(goMemories) {
			goIDs[r.ID] = true
		}
	}

	// Writes embed in the background; wait for that, then backfill anything
	// left so every row has a provider vector before the query.
	d.bg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := d.Backfill(ctx, e, backfillLimit); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	const query = "how do I run the Go tests"
	qv, err := e.Embed(ctx, query)
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	base := d.baselineFor(model)
	t.Logf("baseline (median unrelated cosine over %d rows) = %.3f", 6, base)
	for _, r := range mustList(t, d) {
		cos := dot(qv, decodeVec(rowEmbedding(t, d, r.ID)))
		t.Logf("raw cos %.3f  calibrated %.3f  %-9s %s", cos, calibrate(cos, base), titles[r.ID], goLabel(goIDs[r.ID]))
	}

	got, err := d.recallWith(query, nil, 10, qv, model)
	if err != nil {
		t.Fatal(err)
	}
	for i, g := range got {
		t.Logf("recalled #%d score %.3f sim %.3f %s", i+1, g.Score, g.Sim, goLabel(goIDs[g.Record.ID]))
	}

	if len(got) < len(goMemories) {
		t.Fatalf("expected the %d Go memories to clear the floor, got %d results", len(goMemories), len(got))
	}
	for i := range goMemories {
		if !goIDs[got[i].Record.ID] {
			t.Fatalf("rank %d should be a Go memory, got %q", i+1, got[i].Record.Title)
		}
	}
	for _, g := range got {
		if !goIDs[g.Record.ID] {
			t.Fatalf("unrelated memory %q should fall below the floor", g.Record.Title)
		}
	}
}

func goLabel(isGo bool) string {
	if isGo {
		return "(go)"
	}
	return "(other)"
}
