package workflows

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBundledSourcesHaveWrappersAndPinnedSupport(t *testing.T) {
	for _, definition := range Definitions() {
		files, err := Files(definition.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 || SourceDigest(files) == "" {
			t.Fatalf("workflow %s has no digestable source", definition.ID)
		}
		foundWrapper := false
		for _, file := range files {
			if strings.Join(file.Path, "/") == "SKILL.md" {
				foundWrapper = true
			}
		}
		if !foundWrapper {
			t.Fatalf("workflow %s has no SKILL.md wrapper", definition.ID)
		}
	}
}

// The adapter tests exercise the real pinned scripts in temporary projects.
// Keep this in the Go suite so CI and verify cannot silently skip adapter
// behavior while still avoiding tmux, agents, or a live target.
func TestPythonWorkflowAdapters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "..", "tests", "test_workflow_adapters.py"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workflow adapter tests failed: %v\n%s", err, output)
	}
}

// The bundled workflows moved from a Go map and their own embed into the
// bundled plugins. testdata/golden.json was captured from the old code: the
// definitions, and the digest that names each staged directory on a target
// (~/.lectern/workflows/<id>/<version>-<digest>), must not change, or every
// existing project attachment would stop matching its staged source.
func TestBundledPluginsProduceTheSameWorkflowsAsBefore(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		Definition
		Digest string `json:"digest"`
		Files  int    `json:"files"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	defs := Definitions()
	if len(defs) != len(golden) {
		t.Fatalf("got %d workflows, want %d", len(defs), len(golden))
	}
	for i, g := range golden {
		d := defs[i]
		if d.ID != g.ID || d.Name != g.Name || d.Description != g.Description || d.Version != g.Version ||
			d.UpstreamURL != g.UpstreamURL || strings.Join(d.Commands, "|") != strings.Join(g.Commands, "|") {
			t.Fatalf("workflow %s changed:\n got %+v\nwant %+v", g.ID, d, g.Definition)
		}
		files, err := Files(g.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != g.Files || SourceDigest(files) != g.Digest {
			t.Fatalf("workflow %s staged source changed: %d files digest %s, want %d files digest %s",
				g.ID, len(files), SourceDigest(files), g.Files, g.Digest)
		}
		if !d.Bundled || d.Kind != "workflow" || d.Entry != "lectern-"+g.ID {
			t.Fatalf("workflow %s: bundled=%v kind=%q entry=%q", g.ID, d.Bundled, d.Kind, d.Entry)
		}
	}
}
