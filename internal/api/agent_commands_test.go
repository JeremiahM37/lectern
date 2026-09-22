package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/store"
)

func TestTargetAgentCommandsLookupDoesNotExecute(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	command := filepath.Join(root, "fixture-agent")
	if err := os.WriteFile(command, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(c *config.Config) { c.Mock = false; c.CodexBin = command })
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "command fixture", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	h.decode("PUT", "/api/agents", []obj{
		{"name": "claude", "command": filepath.Join(root, "absent")},
		{"name": "gemini", "command": "touch '" + marker + "'; echo surprise"},
		{"name": "custom-path", "command": command, "env": obj{"PATH": ""}},
	}, 200, nil)
	var rows []obj
	h.decode("GET", fmt.Sprintf("/api/targets/%d/agents", target.ID), nil, 200, &rows)
	found := map[string]obj{}
	for _, row := range rows {
		found[row["name"].(string)] = row
	}
	if found["codex"]["state"] != "available" || found["codex"]["path"] != command {
		t.Fatalf("resolved binary not found: %v", found["codex"])
	}
	if found["claude"]["state"] != "missing" {
		t.Fatalf("missing binary: %v", found["claude"])
	}
	for _, name := range []string{"gemini", "custom-path"} {
		if found[name]["state"] != "unchecked" {
			t.Fatalf("unsafe command evaluated: %v", found[name])
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("agent or shell command executed: %v", err)
	}
	h.decode("GET", "/api/targets/999999/agents", nil, 404, nil)
}

func TestTargetAgentCommandsMockOverridesAreNotClaimedInstalled(t *testing.T) {
	h := newHarness(t)
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "demo", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "custom-codex"}}, 200, nil)
	var rows []obj
	h.decode("GET", fmt.Sprintf("/api/targets/%d/agents", target.ID), nil, 200, &rows)
	for _, row := range rows {
		if row["name"] == "codex" && row["state"] != "unchecked" {
			t.Fatalf("mock claimed override available: %v", row)
		}
	}
}
