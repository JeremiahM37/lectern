package skills

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func registryServer(t *testing.T, h http.HandlerFunc) *Registry {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Registry{BaseURL: srv.URL, Client: srv.Client()}
}

const sampleRegistry = `{"skills":[
 {"id":"vercel-labs/agent-skills/review","skillId":"review","name":"Review","installs":1234,"source":"vercel-labs/agent-skills"},
 {"id":"bad/source","skillId":"bad","name":"Bad","installs":5,"source":"https://evil.example/x"},
 {"id":"no/skill","skillId":"","name":"None","installs":7,"source":"owner/repo"},
 {"id":"traversal","skillId":"../x","name":"Trav","installs":9,"source":"owner/repo"}
],"count":4}`

func TestRegistrySearchParsesAndFiltersEntries(t *testing.T) {
	var gotQ, gotPath string
	r := registryServer(t, func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		gotQ = req.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleRegistry))
	})
	got, err := r.Search(context.Background(), "  review ")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotPath != "/api/search" || gotQ != "review" {
		t.Fatalf("request path=%q q=%q", gotPath, gotQ)
	}
	if len(got) != 1 {
		t.Fatalf("want only the installable entry, got %+v", got)
	}
	e := got[0]
	if e.SkillID != "review" || e.Source != "vercel-labs/agent-skills" || e.Installs != 1234 {
		t.Fatalf("entry = %+v", e)
	}
	if want := "https://github.com/vercel-labs/agent-skills.git#review"; e.InstallSource() != want {
		t.Fatalf("InstallSource = %q, want %q", e.InstallSource(), want)
	}
}

func TestRegistryPopularSearchesForAI(t *testing.T) {
	var gotQ string
	r := registryServer(t, func(w http.ResponseWriter, req *http.Request) {
		gotQ = req.URL.Query().Get("q")
		_, _ = w.Write([]byte(`{"skills":[],"count":0}`))
	})
	got, err := r.Popular(context.Background())
	if err != nil {
		t.Fatalf("popular: %v", err)
	}
	if gotQ != "ai" || len(got) != 0 {
		t.Fatalf("popular q=%q got=%v", gotQ, got)
	}
}

func TestRegistryFailuresAreUnavailable(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"json":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"huge": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"skills":[],"pad":"` + strings.Repeat("x", maxRegistryBody) + `"}`))
		},
		"slow": func(w http.ResponseWriter, req *http.Request) {
			select {
			case <-req.Context().Done():
			case <-time.After(3 * time.Second):
			}
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			r := registryServer(t, h)
			r.Client = &http.Client{Timeout: 200 * time.Millisecond}
			_, err := r.Search(context.Background(), "x")
			if err == nil {
				t.Fatal("want an error")
			}
			if name != "json" && !errors.Is(err, ErrRegistryUnavailable) {
				t.Fatalf("error %v should wrap ErrRegistryUnavailable", err)
			}
		})
	}
}

func TestRegistryOfflineDegradesToError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	r := &Registry{BaseURL: url, Client: &http.Client{Timeout: time.Second}}
	if _, err := r.Popular(context.Background()); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("offline popular: %v", err)
	}
}

func TestRegistryRejectsEmptyQuery(t *testing.T) {
	r := &Registry{BaseURL: "http://127.0.0.1:1"}
	if _, err := r.Search(context.Background(), "   "); err == nil {
		t.Fatal("empty query should be rejected before any request")
	}
}

func TestInstallNamedSkillInsideRepositoryDirectory(t *testing.T) {
	root, _ := env(t)
	repo := t.TempDir()
	writeSkill(t, filepath.Join(repo, "skills", "review"), skill("review", "Reviews code.", "Read the diff."))
	writeSkill(t, filepath.Join(repo, "skills", "other"), skill("other", "Something else.", "x"))
	if err := os.MkdirAll(filepath.Join(repo, ".git", "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	sk, err := Install(root, repo+"#review", ScopeProject)
	if err != nil {
		t.Fatalf("install by name: %v", err)
	}
	if sk.Name != "review" {
		t.Fatalf("installed %+v", sk)
	}
	if _, err := Install(root, repo+"#missing", ScopeProject); err == nil {
		t.Fatal("an unknown skill name should still fail")
	}
}
