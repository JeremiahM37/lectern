package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNoModelListNeverRequestsAndUsesStaticList(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := New(Options{ID: "x", BaseURL: srv.URL, NoModelList: true, Models: []string{"m1"}})
	ms, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "m1" {
		t.Fatalf("models = %+v", ms)
	}
	if hits.Load() != 0 {
		t.Fatalf("server hit %d times with listing off", hits.Load())
	}
}

func TestNoModelListWithoutModelsErrors(t *testing.T) {
	p := New(Options{ID: "x", BaseURL: "http://127.0.0.1:1", NoModelList: true})
	if _, err := p.Models(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestModelsURLOverridesBaseURL(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Write([]byte(`{"data":[{"id":"z"}]}`))
	}))
	defer srv.Close()
	p := New(Options{ID: "x", BaseURL: "http://127.0.0.1:1/v1", ModelsURL: srv.URL + "/list"})
	if _, err := p.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if path != "/list" {
		t.Fatalf("path = %q, want /list", path)
	}
}
