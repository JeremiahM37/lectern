package api_test

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"testing"
)

// A client that accepts gzip gets the lists compressed; the event stream is
// never wrapped, so it keeps flushing event by event.
func TestJSONListsAreGzippedButStreamsAreNot(t *testing.T) {
	h := newHarness(t)
	h.task(h.seededProjectID(), "one", "x", nil)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}

	req, _ := http.NewRequest("GET", h.URL+"/api/tasks?brief=true", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", resp.Header.Get("Content-Encoding"))
	}
	z, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.NewDecoder(z).Decode(&rows); err != nil || len(rows) == 0 {
		t.Fatalf("decoding the gzipped list: %v (%d rows)", err, len(rows))
	}

	req, _ = http.NewRequest("GET", h.URL+"/api/tasks", nil)
	plain, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	plain.Body.Close()
	if plain.Header.Get("Content-Encoding") != "" {
		t.Fatal("a client that did not ask for gzip got it anyway")
	}

	req, _ = http.NewRequest("GET", h.URL+"/api/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept", "text/event-stream")
	stream, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	stream.Body.Close()
	if stream.Header.Get("Content-Encoding") != "" {
		t.Fatal("the event stream was compressed")
	}
}
