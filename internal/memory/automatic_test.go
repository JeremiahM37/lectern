package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAutomaticMemoryUsesExactProjectScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path != "/api/memory/context" || query.Get("scope") != "scoped" || query.Get("max_bytes") != "2400" {
			t.Errorf("unexpected lookup: %s", request.URL)
		}
		if !reflect.DeepEqual(query["path"], []string{"memory/kestrel.md", "memory/kestrel/", "Agent Memory/project_kestrel.md"}) {
			t.Errorf("scope: %v", query["path"])
		}
		if query.Get("exclude") != "already-seen" {
			t.Error("lost session deduplication")
		}
		if query.Get("rank") != "hybrid" || query.Get("min_rel") != "0.5" {
			t.Errorf("ranking not requested: rank=%q min_rel=%q", query.Get("rank"), query.Get("min_rel"))
		}
		json.NewEncoder(writer).Encode(ContextResult{Context: "bounded reference", Keys: []string{"new"}})
	}))
	defer server.Close()
	result, err := NewGrimoire(server.URL, "").Automatic(context.Background(), "kestrel", "deploy certificates", []string{"already-seen"})
	if err != nil || result.Context != "bounded reference" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestAutomaticMemoryNoFallbackOnOlderServer(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		http.NotFound(writer, request)
	}))
	defer server.Close()
	provider := NewGrimoire(server.URL, "")
	result := Automatic(context.Background(), provider, "kestrel", "deployment", nil)
	if result.Context != "" || requests != 1 {
		t.Fatalf("widened fallback: %+v requests=%d", result, requests)
	}
}

func TestAutomaticMemoryModesAndProjectOverrides(t *testing.T) {
	provider := NewGrimoire("http://invalid", "")
	if err := provider.ConfigureContext("manual", `{"kestrel":{"paths":["teams/kestrel/"]}}`); err != nil {
		t.Fatal(err)
	}
	if provider.ContextScope("other").Mode != "manual" || provider.ContextScope("kestrel").Paths[0] != "teams/kestrel/" {
		t.Fatal("incorrect override")
	}
	if result, err := provider.Automatic(context.Background(), "other", "anything", nil); err != nil || result.Context != "" {
		t.Fatalf("manual fetched: %v", err)
	}
	if provider.ContextScope("").Mode != "off" {
		t.Fatal("unassigned session must not read memory")
	}
	if err := provider.ConfigureContext("all", ""); err != nil {
		t.Fatal(err)
	}
	if provider.ContextScope("kestrel").Mode != "all" {
		t.Fatal("all not configurable")
	}
	if err := provider.ConfigureContext("project", `{"kestrel":{"mode":"scoped"}}`); err == nil {
		t.Fatal("empty scope accepted")
	}
}

func TestAutomaticMemoryRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		json.NewEncoder(writer).Encode(ContextResult{Context: strings.Repeat("x", 2401)})
	}))
	defer server.Close()
	result := Automatic(context.Background(), NewGrimoire(server.URL, ""), "kestrel", "deployment", nil)
	if result.Context != "" {
		t.Fatal("budget exceeded")
	}
}
