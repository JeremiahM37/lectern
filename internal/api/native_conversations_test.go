package api_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestNativeHistoryUsesExactWorkspaceAndPagesWithoutMutation(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			requireRealTools(t)
			h := newHarness(t, func(c *config.Config) { c.Mock = false })
			root := t.TempDir()
			home := t.TempDir()
			target, _ := h.App.DB.InsertTarget(&store.Target{Name: "native", Kind: "local"})
			session, _ := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: "native", Agent: agent, Workdir: root, TmuxSession: "not-needed", Status: "dead"})
			envName := "CODEX_HOME"
			dir := filepath.Join(home, "sessions")
			if agent == "claude" {
				envName = "CLAUDE_CONFIG_DIR"
				var slug strings.Builder
				for _, c := range root {
					if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
						slug.WriteRune(c)
					} else {
						slug.WriteByte('-')
					}
				}
				dir = filepath.Join(home, "projects", slug.String())
			}
			os.MkdirAll(dir, 0700)
			h.decode("PUT", "/api/agents", []obj{{"name": agent, "command": agent, "env": obj{envName: home}, "fork_args": []string{"fork", "{id}"}}}, 200, nil)
			cid := "11111111-1111-4111-8111-111111111111"
			foreign := "22222222-2222-4222-8222-222222222222"
			makeFile := func(id, cwd string) string {
				var rows []map[string]any
				if agent == "codex" {
					rows = append(rows, map[string]any{"type": "session_meta", "payload": obj{"id": id, "cwd": cwd}})
				}
				for i := 0; i < 450; i++ {
					body := fmt.Sprintf("native message %03d", i)
					if agent == "codex" {
						rows = append(rows, map[string]any{"type": "response_item", "payload": obj{"type": "message", "role": "user", "content": []obj{{"type": "input_text", "text": body}}}})
					} else {
						rows = append(rows, map[string]any{"type": "user", "sessionId": id, "cwd": cwd, "message": obj{"role": "user", "content": body}})
					}
				}
				if agent == "codex" {
					rows = append(rows, map[string]any{"type": "response_item", "payload": obj{"type": "message", "role": "assistant", "channel": "analysis", "content": []obj{{"type": "output_text", "text": "PRIVATE_REASONING"}}}})
				} else {
					rows = append(rows, map[string]any{"type": "assistant", "sessionId": id, "cwd": cwd, "message": obj{"role": "assistant", "content": []obj{{"type": "thinking", "thinking": "PRIVATE_REASONING"}}}})
				}
				var b strings.Builder
				for _, r := range rows {
					data, _ := json.Marshal(r)
					b.Write(data)
					b.WriteByte('\n')
				}
				b.WriteString(`{"partial":`)
				name := filepath.Join(dir, id+".jsonl")
				os.WriteFile(name, []byte(b.String()), 0600)
				return name
			}
			file := makeFile(cid, root)
			makeFile(foreign, t.TempDir())
			original, _ := os.ReadFile(file)
			base := fmt.Sprintf("/api/sessions/%d/conversations", session.ID)
			var list obj
			h.decode("GET", base, nil, 200, &list)
			encoded := fmt.Sprint(list)
			if !strings.Contains(encoded, cid) || strings.Contains(encoded, foreign) {
				t.Fatalf("workspace filter: %v", list)
			}
			var page obj
			h.decode("GET", base+"/"+cid, nil, 200, &page)
			if strings.Contains(fmt.Sprint(page), "PRIVATE_REASONING") {
				t.Fatal("reasoning leaked into reader")
			}
			messages := page["messages"].([]any)
			if len(messages) != 200 {
				t.Fatal(len(messages))
			}
			if !strings.Contains(fmt.Sprint(messages[len(messages)-1]), "native message 449") {
				t.Fatal("not newest page")
			}
			before := int64(page.num("before"))
			if before <= 0 {
				t.Fatal("no earlier cursor")
			}
			h.decode("GET", fmt.Sprintf("%s/%s?before=%d", base, cid, before), nil, 200, &page)
			if strings.Contains(fmt.Sprint(page["messages"]), "native message 449") {
				t.Fatal("overlapping pagination")
			}
			h.decode("GET", base+"/"+foreign, nil, 409, nil)
			h.decode("POST", fmt.Sprintf("/api/sessions/%d/fork", session.ID), obj{"conversation_id": foreign}, 409, nil)
			after, _ := os.ReadFile(file)
			if string(after) != string(original) {
				t.Fatal("history read modified transcript")
			}
		})
	}
}

func TestCodexForkKeepsFirstHeaderIdentity(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	root, home := t.TempDir(), t.TempDir()
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "fork-header", Kind: "local"})
	session, _ := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: "fork", Agent: "codex", Workdir: root, TmuxSession: "not-needed", Status: "dead"})
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "codex", "env": obj{"CODEX_HOME": home}}}, 200, nil)
	child := "11111111-1111-4111-8111-111111111111"
	parent := "22222222-2222-4222-8222-222222222222"
	dir := filepath.Join(home, "sessions")
	os.MkdirAll(dir, 0700)
	rows := []obj{
		{"type": "session_meta", "payload": obj{"id": child, "cwd": root, "source": "cli"}},
		{"type": "session_meta", "payload": obj{"id": parent, "cwd": "/parent/workspace", "source": "cli"}},
		{"type": "response_item", "payload": obj{"type": "message", "role": "assistant", "content": []obj{{"type": "output_text", "text": "Copied native fork history"}}}},
	}
	var body strings.Builder
	for _, row := range rows {
		data, _ := json.Marshal(row)
		body.Write(data)
		body.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, child+".jsonl"), []byte(body.String()), 0600); err != nil {
		t.Fatal(err)
	}
	var list struct{ Conversations []struct{ ID string } }
	path := fmt.Sprintf("/api/sessions/%d/conversations", session.ID)
	h.decode("GET", path, nil, 200, &list)
	if len(list.Conversations) != 1 || list.Conversations[0].ID != child {
		t.Fatalf("fork identity replaced by copied parent: %+v", list)
	}
	var history obj
	h.decode("GET", path+"/"+child, nil, 200, &history)
	data, _ := json.Marshal(history)
	if !strings.Contains(string(data), "Copied native fork history") {
		t.Fatal(string(data))
	}
	h.decode("GET", path+"/"+parent, nil, 409, nil)
}
