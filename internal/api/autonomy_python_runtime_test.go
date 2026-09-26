package api

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonRuntimeDiscoveryReportsScopeAndMissing(t *testing.T) {
	root := t.TempDir()
	if autoPythonTestRuntime(root)["status"] != "unavailable" {
		t.Fatal("invented runtime")
	}
	key := strings.Repeat("a", 64)
	os.Mkdir(filepath.Join(root, key), 0755)
	os.WriteFile(filepath.Join(root, "active.json"), []byte(`{"key":"`+key+`"}`), 0444)
	data := map[string]any{"key": key, "kind": "python-test-runtime", "runtime_family": "python3.13", "checksum_verified": true, "packages": map[string]string{"pytest": "9.1.1"}}
	raw, _ := json.Marshal(data)
	p := filepath.Join(root, key, "manifest.json")
	os.WriteFile(p, raw, 0644)
	got := autoPythonTestRuntime(root)
	if got["status"] != "provisioned" || !strings.Contains(got["scope"].(string), "not project dependencies") {
		t.Fatal(got)
	}
	data["key"] = strings.Repeat("b", 64)
	raw, _ = json.Marshal(data)
	os.WriteFile(p, raw, 0644)
	if autoPythonTestRuntime(root)["status"] != "unavailable" {
		t.Fatal("mismatched runtime")
	}
	os.Remove(p)
	os.Symlink("/etc/passwd", p)
	if autoPythonTestRuntime(root)["status"] != "unavailable" {
		t.Fatal("followed manifest link")
	}
	os.WriteFile(filepath.Join(root, "active.json"), []byte(`{"key":"../../outside"}`), 0644)
	if autoPythonTestRuntime(root)["status"] != "unavailable" {
		t.Fatal("path escape")
	}
}

func TestWorkshopTechnicalAuthorityPreservesConsentBoundaries(t *testing.T) {
	s := autoTestServer(t)
	a := planEvidenceFixture()
	a.State.Items = []autonomy.Proposal{{ProjectID: 1, Title: "scope", Acceptance: []string{"evidence"}}}
	for _, role := range []string{"planner", "auditor_a", "auditor_b", "builder", "reviewer"} {
		prompt := s.autoPrompt(context.Background(), a, role, &store.Project{})
		for _, term := range []string{"ordinary reversible technical choices", "explicit user decisions remain binding", "does not expand privileges or expose additional data", "does not authorize production application", "Public actions, destructive operations"} {
			if !strings.Contains(prompt, term) {
				t.Fatalf("%s missing %q", role, term)
			}
		}
	}
}
