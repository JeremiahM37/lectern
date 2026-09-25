package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type ContextScope struct {
	Mode     string   `json:"mode"`
	Paths    []string `json:"paths"`
	MaxBytes int      `json:"max_bytes"`
}

type ContextResult struct {
	Context string   `json:"context"`
	Keys    []string `json:"keys"`
	// Mode is how the store was searched for this delivery (scoped, all, the
	// name of a project's managed note). It is recorded alongside the delivery
	// because "why did the agent see this" is often answered by "what was it
	// allowed to read" — see docs/memory-visibility.md.
	Mode string `json:"mode,omitempty"`
	// Items describe each delivered record for the operator: which record it
	// was, where it came from, and enough of it to recognise. A store that does
	// not send them still delivers context; the delivery log then names only
	// the keys that went out.
	Items       []Item `json:"items,omitempty"`
	Unavailable bool   `json:"-"`
}

type AutomaticProvider interface {
	Automatic(context.Context, string, string, []string) (ContextResult, error)
}

type ProjectProvider interface {
	AutomaticProject(context.Context, string, string, string, []string) (ContextResult, error)
	Provision(context.Context, string, string) (string, error)
}

var projectSeparators = regexp.MustCompile(`[^a-z0-9]+`)

func (g *Grimoire) ConfigureContext(mode, projects string) error {
	if mode == "" {
		mode = "project"
	}
	if mode != "project" && mode != "all" && mode != "manual" && mode != "off" {
		return fmt.Errorf("invalid Grimoire context mode %q", mode)
	}
	configured := map[string]ContextScope{}
	if projects != "" {
		if err := json.Unmarshal([]byte(projects), &configured); err != nil {
			return err
		}
	}
	for project, scope := range configured {
		if scope.Mode == "" {
			scope.Mode = "scoped"
		}
		if scope.Mode != "scoped" && scope.Mode != "all" && scope.Mode != "manual" && scope.Mode != "off" {
			return fmt.Errorf("invalid Grimoire context mode for project %q", project)
		}
		if scope.Mode == "scoped" && len(scope.Paths) == 0 {
			return fmt.Errorf("Grimoire project %q has an empty memory scope", project)
		}
		configured[project] = scope
	}
	g.ContextMode, g.ContextProjects = mode, configured
	return nil
}

func (g *Grimoire) ContextScope(project string) ContextScope {
	if project == "" {
		return ContextScope{Mode: "off"}
	}
	if scope, found := g.ContextProjects[project]; found {
		return scope
	}
	if g.ContextMode == "off" || g.ContextMode == "manual" || g.ContextMode == "all" {
		return ContextScope{Mode: g.ContextMode}
	}
	slug := strings.Trim(projectSeparators.ReplaceAllString(strings.ToLower(project), "-"), "-")
	if slug == "" {
		return ContextScope{Mode: "off"}
	}
	return ContextScope{Mode: "scoped", Paths: []string{
		"memory/" + slug + ".md", "memory/" + slug + "/",
		"Agent Memory/project_" + strings.ReplaceAll(slug, "-", "_") + ".md",
	}}
}

func (g *Grimoire) Automatic(ctx context.Context, project, query string, excluded []string) (ContextResult, error) {
	return g.AutomaticProject(ctx, project, "", query, excluded)
}

// projectScope is where a project's memory is read from, and how that was
// decided. It is one function so that what is reported to the operator is what
// retrieval actually uses, not a description of it kept beside it.
//
//	managed    — the project has its own provisioned note; the link is exact
//	configured — the operator named the paths
//	guessed    — derived from the project's name; right only if a note happens
//	             to be filed under that name
func (g *Grimoire) projectScope(project, topic string) (ContextScope, string) {
	scope := g.ContextScope(project)
	_, configured := g.ContextProjects[project]
	basis := "guessed"
	if configured {
		basis = "configured"
	}
	if topic != "" && scope.Mode == "scoped" {
		if !configured {
			scope.Paths = []string{"memory/" + topic + ".md"}
			basis = "managed"
		} else {
			scope.Paths = append(append([]string(nil), scope.Paths...), "memory/"+topic+".md")
		}
	}
	if scope.Mode != "scoped" {
		basis = scope.Mode
	}
	return scope, basis
}

func (g *Grimoire) AutomaticProject(ctx context.Context, project, topic, query string, excluded []string) (ContextResult, error) {
	scope, _ := g.projectScope(project, topic)
	if scope.Mode == "manual" || scope.Mode == "off" {
		return ContextResult{}, nil
	}
	budget := scope.MaxBytes
	if budget <= 0 {
		budget = 2400
	}
	if budget > 8000 {
		budget = 8000
	}
	if len(query) > 8000 {
		return ContextResult{}, nil
	}
	parameters := url.Values{"q": {query}, "scope": {scope.Mode}, "path": scope.Paths,
		"max_bytes": {fmt.Sprint(budget)}, "limit": {"5"}, "exclude": {strings.Join(excluded, ",")}}
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", g.BaseURL+"/api/memory/context?"+parameters.Encode(), nil)
	if err != nil {
		return ContextResult{}, err
	}
	g.auth(request)
	response, err := g.Client.Do(request)
	if err != nil {
		return ContextResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ContextResult{}, fmt.Errorf("automatic memory: %s", response.Status)
	}
	// Decoded by hand rather than straight into ContextResult: `items` is only
	// known loosely (the store's own shape, an older shape, or absent
	// entirely), and an unknown field or an unexpected item shape must degrade
	// to "no descriptions" rather than fail the delivery.
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64000))
	if err != nil {
		return ContextResult{}, err
	}
	var payload struct {
		Context string          `json:"context"`
		Keys    []string        `json:"keys"`
		Mode    string          `json:"mode"`
		Items   json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ContextResult{}, err
	}
	result := ContextResult{Context: payload.Context, Keys: payload.Keys,
		Mode: firstNonEmpty(payload.Mode, scope.Mode)}
	result.Items = normalizeItems(payload.Items, result.Keys)
	if len(result.Context) > budget || len(result.Keys) > 10 {
		return ContextResult{}, fmt.Errorf("automatic memory exceeded budget")
	}
	return result, nil
}

func Automatic(ctx context.Context, provider Provider, project, query string, excluded []string, topics ...string) ContextResult {
	if scoped, ok := provider.(ProjectProvider); ok && len(topics) > 0 {
		result, err := scoped.AutomaticProject(ctx, project, topics[0], query, excluded)
		if err == nil {
			return result
		}
		return ContextResult{Unavailable: true}
	}
	if automatic, ok := provider.(AutomaticProvider); ok {
		result, err := automatic.Automatic(ctx, project, query, excluded)
		if err == nil {
			return result
		}
		return ContextResult{Unavailable: true}
	}
	return ContextResult{}
}
