package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

var managedTopic = regexp.MustCompile(`^lectern-[a-f0-9]{32}$`)

func (g *Grimoire) Provision(ctx context.Context, project, topic string) (string, error) {
	mode := g.ContextScope(project).Mode
	if mode == "manual" || mode == "off" {
		return "disabled", nil
	}
	if !managedTopic.MatchString(topic) {
		return "unavailable", fmt.Errorf("invalid managed memory topic")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{
		"path": "memory/" + topic + ".md", "title": "Memory: " + project,
		"body":        "# Project memory\n",
		"frontmatter": map[string]any{"lectern_memory_topic": topic},
	})
	request, err := http.NewRequestWithContext(ctx, "POST", g.BaseURL+"/api/notes", bytes.NewReader(body))
	if err != nil {
		return "unavailable", err
	}
	request.Header.Set("Content-Type", "application/json")
	g.auth(request)
	response, err := g.Client.Do(request)
	if err != nil {
		return "unavailable", err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusCreated {
		return "ready", nil
	}
	if response.StatusCode == http.StatusConflict {
		request, err = http.NewRequestWithContext(ctx, "GET", g.BaseURL+"/api/notes/memory/"+topic+".md", nil)
		if err != nil {
			return "unavailable", err
		}
		g.auth(request)
		existing, err := g.Client.Do(request)
		if err != nil {
			return "unavailable", err
		}
		defer existing.Body.Close()
		var note struct {
			Frontmatter map[string]any `json:"frontmatter"`
		}
		if existing.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(existing.Body, 64000)).Decode(&note) == nil && note.Frontmatter["lectern_memory_topic"] == topic {
			return "ready", nil
		}
		return "unavailable", fmt.Errorf("existing memory location could not be verified")
	}
	return "unavailable", fmt.Errorf("memory provisioning: %s", response.Status)
}

func (g *Grimoire) ProjectHint(project, topic string) string {
	mode := g.ContextScope(project).Mode
	if mode == "manual" || mode == "off" {
		return ""
	}
	hint := ""
	if managedTopic.MatchString(topic) {
		hint = "Project memory: memory/" + topic + ".md. For durable project facts, use Grimoire remember with topic=\"" + topic + "\" and scope=\"topic\".\n" +
			"Say what each fact is: pass event=true for something that happened; for a claim about how things are now, pass anchors (typed checks naming the exact file, service, port or commit) and host if it is about another machine.\n"
	}
	return hint + ProjectNoteHint(project)
}

// ProjectNoteHint tells an agent where the project is documented and that
// keeping it documented is part of the work. A session's conversation is not
// kept anywhere a later session reads, so what the operator decided has to be
// written down by the agent that heard it.
func ProjectNoteHint(project string) string {
	note := ProjectNotePath(project)
	if note == "" {
		return ""
	}
	return "Project note: " + note + " in Grimoire documents this project. Read it before starting. " +
		"If it does not exist, create it with create_note: what the project is, where it lives, how to run and test it, and its current state. " +
		"When the user decides something or gives a standing instruction about this project, add it to that note before you finish, so the next session has it. " +
		"Other notes about this project go in " + ProjectNoteDir(project) + ". Never write secret values into a note.\n"
}

func ProjectHint(provider Provider, project, topic string) string {
	if configured, ok := provider.(interface{ ProjectHint(string, string) string }); ok {
		return configured.ProjectHint(project, topic)
	}
	return ""
}
