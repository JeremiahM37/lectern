package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateServersKeepsOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{"model":"openai/gpt-6","theme":"dusk","mcp":[{"name":"a","command":"echo"}]}`)

	err := UpdateServers(path, func(list []config.MCPServer) ([]config.MCPServer, error) {
		return PutServer(list, "", config.MCPServer{Name: "b", URL: "https://example.invalid/mcp"}), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	b, _ := os.ReadFile(path)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("written file is not JSON: %v\n%s", err, b)
	}
	if got["model"] != "openai/gpt-6" || got["theme"] != "dusk" {
		t.Fatalf("other keys were not kept: %v", got)
	}
	servers, err := readConfigMCP(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0].Name != "a" || servers[1].URL == "" {
		t.Fatalf("servers = %+v", servers)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
}

func TestUpdateServersCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	err := UpdateServers(path, func([]config.MCPServer) ([]config.MCPServer, error) {
		return []config.MCPServer{{Name: "new", Command: "srv", Disabled: true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	servers, err := readConfigMCP(path)
	if err != nil || len(servers) != 1 || !servers[0].Disabled {
		t.Fatalf("servers = %+v, err = %v", servers, err)
	}
}

func TestUpdateServersEmptyListRemovesKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{"model":"x","mcp":[{"name":"a","command":"echo"}]}`)
	err := UpdateServers(path, func(list []config.MCPServer) ([]config.MCPServer, error) {
		out, _ := RemoveServer(list, "a")
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `"mcp"`) || !strings.Contains(string(b), `"model"`) {
		t.Fatalf("after removing the last server: %s", b)
	}
}

func TestUpdateServersRefusesInvalidJSONAndLeavesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{"mcp": [`)
	called := false
	err := UpdateServers(path, func(list []config.MCPServer) ([]config.MCPServer, error) {
		called = true
		return list, nil
	})
	if err == nil || called {
		t.Fatalf("invalid JSON should fail before editing, err=%v called=%v", err, called)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"mcp": [` {
		t.Fatalf("file was modified: %q", b)
	}
}

func TestUpdateServersPropagatesEditError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	err := UpdateServers(path, func([]config.MCPServer) ([]config.MCPServer, error) {
		return nil, os.ErrPermission
	})
	if err == nil {
		t.Fatal("edit error was swallowed")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("file written after a failed edit")
	}
}

func TestPutServerReplacesAndRenames(t *testing.T) {
	a := config.MCPServer{Name: "a", Command: "one"}
	b := config.MCPServer{Name: "b", Command: "two"}
	c := config.MCPServer{Name: "c", Command: "three"}

	// Replace in place.
	got := PutServer([]config.MCPServer{a, b}, "b", config.MCPServer{Name: "b", Command: "B2"})
	if len(got) != 2 || got[1].Command != "B2" {
		t.Fatalf("replace: %+v", got)
	}
	// Rename: the entry called prev is the one that changes.
	got = PutServer([]config.MCPServer{a, b}, "a", config.MCPServer{Name: "z", Command: "one"})
	if !reflect.DeepEqual(got, []config.MCPServer{{Name: "z", Command: "one"}, b}) {
		t.Fatalf("rename: %+v", got)
	}
	// Renaming onto a name that already exists later in the list must not
	// leave the old entry behind (the case a naive loop gets wrong).
	got = PutServer([]config.MCPServer{b, a}, "a", config.MCPServer{Name: "b", Command: "fresh"})
	if len(got) != 1 || got[0].Name != "b" || got[0].Command != "fresh" {
		t.Fatalf("rename onto existing: %+v", got)
	}
	// Add a new name.
	got = PutServer([]config.MCPServer{a}, "", c)
	if len(got) != 2 || got[1].Name != "c" {
		t.Fatalf("append: %+v", got)
	}
}

func TestRemoveServer(t *testing.T) {
	list := []config.MCPServer{{Name: "a"}, {Name: "b"}}
	got, found := RemoveServer(list, "a")
	if !found || len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("remove a: %+v %v", got, found)
	}
	if _, found := RemoveServer(list, "nope"); found {
		t.Fatal("removed a server that is not there")
	}
}
