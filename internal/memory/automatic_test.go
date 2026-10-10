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
		if !reflect.DeepEqual(query["path"], []string{"memory/kestrel.md", "memory/kestrel/", "Agent Memory/project_kestrel.md", "Projects/kestrel/"}) {
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

func TestEveryScopedProjectReadsItsOwnNoteFolder(t *testing.T) {
	g := NewGrimoire("http://unused", "")
	if err := g.ConfigureContext("project", `{"named":{"paths":["Agent Memory/project_named.md"]}}`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ project, topic, basis, want string }{
		{"kestrel", "", "guessed", "Projects/kestrel/"},
		{"named", "", "configured", "Projects/named/"},
		{"Robots", "lectern-91061b54d9fc89ffbdec7b94760d2587", "managed", "Projects/Robots/"},
		{"interface.ai", "", "guessed", "Projects/interface.ai/"},
	} {
		scope, basis := g.projectScope(c.project, c.topic)
		if basis != c.basis || scope.Paths[len(scope.Paths)-1] != c.want {
			t.Errorf("%s: basis %q paths %v", c.project, basis, scope.Paths)
		}
	}
	// The configured map is shared state: reading a scope must not grow it.
	if got := g.ContextProjects["named"].Paths; len(got) != 1 {
		t.Errorf("configured paths were modified: %v", got)
	}
	if scope, _ := g.projectScope("named", ""); len(scope.Paths) != 2 {
		t.Errorf("the folder was added twice: %v", scope.Paths)
	}
}

func TestAProjectNoteNameCannotLeaveTheProjectsFolder(t *testing.T) {
	for name, want := range map[string]string{
		"computer-use mcp": "Projects/computer-use mcp/computer-use mcp.md",
		"../../etc":        "Projects/-..-etc/-..-etc.md",
		"a/b":              "Projects/a-b/a-b.md",
		"  ":               "",
		"..":               "",
	} {
		if got := ProjectNotePath(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

func TestAnAgentIsToldWhereTheProjectIsDocumented(t *testing.T) {
	g := NewGrimoire("http://unused", "")
	if err := g.ConfigureContext("project", `{"quiet":{"mode":"off"}}`); err != nil {
		t.Fatal(err)
	}
	hint := g.ProjectHint("kestrel", "")
	for _, want := range []string{"Projects/kestrel/kestrel.md", "create_note", "standing instruction"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint lacks %q: %s", want, hint)
		}
	}
	if strings.Contains(hint, "Project memory:") {
		t.Errorf("a project without a managed topic was given one: %s", hint)
	}
	managed := g.ProjectHint("kestrel", "lectern-91061b54d9fc89ffbdec7b94760d2587")
	if !strings.HasPrefix(managed, "Project memory: memory/lectern-") || !strings.Contains(managed, "Projects/kestrel/kestrel.md") {
		t.Errorf("managed hint: %s", managed)
	}
	if g.ProjectHint("quiet", "") != "" {
		t.Error("a project with memory off must get no hint")
	}
}
