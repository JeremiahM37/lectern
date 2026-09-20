package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LinkedPath is one place a project's memory is read from.
type LinkedPath struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Notes  int    `json:"notes"` // for a folder, how many notes are under it
}

// Link is the connection between a project and the memory store, as it
// actually is rather than as it is assumed to be.
type Link struct {
	Mode  string       `json:"mode"`  // scoped | all | manual | off
	Basis string       `json:"basis"` // managed | configured | guessed | <mode>
	Paths []LinkedPath `json:"paths"`
	// Linked is whether any of those paths holds a note. A guessed link whose
	// paths are all empty is the quiet failure: retrieval runs, finds nothing,
	// and the project looks as though it simply has no memory.
	Linked bool `json:"linked"`
}

// LinkProvider is a memory provider that can say where a project's memory lives.
type LinkProvider interface {
	ProjectLink(ctx context.Context, project, topic string) (Link, error)
}

// ProjectLink resolves the scope retrieval would use and checks each path
// against what the store holds.
func (g *Grimoire) ProjectLink(ctx context.Context, project, topic string) (Link, error) {
	scope, basis := g.projectScope(project, topic)
	link := Link{Mode: scope.Mode, Basis: basis, Paths: []LinkedPath{}}
	if scope.Mode != "scoped" {
		// "all" reads the whole store and the others read nothing: either way
		// there is no particular note to be missing.
		link.Linked = scope.Mode == "all"
		return link, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", g.BaseURL+"/api/notes", nil)
	if err != nil {
		return link, err
	}
	g.auth(req)
	resp, err := g.Client.Do(req)
	if err != nil {
		return link, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return link, fmt.Errorf("grimoire notes: %s", resp.Status)
	}
	var notes []struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&notes); err != nil {
		return link, err
	}
	for _, path := range scope.Paths {
		entry := LinkedPath{Path: path}
		for _, note := range notes {
			if note.Path == path || (strings.HasSuffix(path, "/") && strings.HasPrefix(note.Path, path)) {
				entry.Exists = true
				entry.Notes++
			}
		}
		link.Linked = link.Linked || entry.Exists
		link.Paths = append(link.Paths, entry)
	}
	return link, nil
}
