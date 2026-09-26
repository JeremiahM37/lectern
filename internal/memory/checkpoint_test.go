package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCheckpointTimeoutAfterWriteDoesNotDuplicateOrInfer(t *testing.T) {
	var mu sync.Mutex
	notes := map[string]string{}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Method == "GET" {
			body, ok := notes[strings.TrimPrefix(r.URL.Path, "/api/notes/")]
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"body": body + "\n"})
			return
		}
		if r.Method != "POST" || r.URL.Path != "/api/notes" {
			mu.Unlock()
			t.Error("checkpoint used inference or mutation endpoint")
			http.Error(w, "wrong route", 400)
			return
		}
		var in struct{ Path, Body string }
		json.NewDecoder(r.Body).Decode(&in)
		notes[in.Path] = in.Body
		writes++
		mu.Unlock()
		// The file is durable, but remote indexing outlasts the client's timeout.
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()
	g := NewGrimoire(server.URL, "")
	g.Client = &http.Client{Timeout: 20 * time.Millisecond}
	entry := Entry{Key: "autonomy-2026-09-25-62", Project: "autonomous-experiment", Text: "cycle62 complete"}
	if err := g.Remember(context.Background(), entry); err == nil {
		t.Fatal("expected interrupted first response")
	}
	if err := g.Remember(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	entry.Text = "different content"
	if err := g.Remember(context.Background(), entry); err == nil {
		t.Fatal("overwrote immutable receipt")
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 || len(notes) != 1 {
		t.Fatalf("retry wrote %d copies", writes)
	}
}
