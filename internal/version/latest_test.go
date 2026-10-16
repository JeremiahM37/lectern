package version

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLatestTagReadsTheTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("no user agent sent")
		}
		w.Write([]byte(`{"tag_name":"v2.11.0","name":"ignored"}`))
	}))
	defer srv.Close()
	tag, err := LatestTag(context.Background(), srv.Client(), srv.URL)
	if err != nil || tag != "v2.11.0" {
		t.Fatalf("tag = %q, %v", tag, err)
	}
}

func TestLatestTagErrors(t *testing.T) {
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer missing.Close()
	if _, err := LatestTag(context.Background(), missing.Client(), missing.URL); err == nil {
		t.Fatal("a 404 should be an error")
	}

	noTag := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"no tag here"}`))
	}))
	defer noTag.Close()
	if _, err := LatestTag(context.Background(), noTag.Client(), noTag.URL); err == nil {
		t.Fatal("a reply with no tag should be an error")
	}
}
