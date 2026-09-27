package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func nodeCatalogFixture(t *testing.T) (string, func(string, int64) ([]byte, error), map[string]map[string]any) {
	t.Helper()
	root := t.TempDir()
	manifest := map[string]any{"schema_version": json.Number("1"), "kind": "node-tooling", "policy": "npm-locked-offline-v1", "platform": "Linux", "machine": map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH], "node_version": "v24.13.1", "npm_version": "11.8.0", "files": []any{map[string]any{"path": "bin/node", "sha256": strings.Repeat("b", 64), "mode": json.Number("365")}}}
	raw, e := autoMaintenanceCanonical(manifest)
	if e != nil {
		t.Fatal(e)
	}
	key := autoSHA(raw)
	manifest["key"] = key
	raw, e = autoMaintenanceCanonical(manifest)
	if e != nil {
		t.Fatal(e)
	}
	os.MkdirAll(filepath.Join(root, key), 0755)
	os.WriteFile(filepath.Join(root, key, "manifest.json"), raw, 0444)
	os.WriteFile(filepath.Join(root, "active.json"), []byte(`{"key":"`+key+`"}`), 0444)
	helpers := map[string]map[string]any{}
	for _, name := range autoNodeCapabilityHelpers {
		helpers[name] = map[string]any{"status": "installed", "sha256": strings.Repeat("c", 64)}
	}
	// File-policy enforcement is tested separately; this reader isolates parsing.
	read := func(path string, max int64) ([]byte, error) { return autoReadRegular(path, max) }
	return root, read, helpers
}
func TestNodeCapabilityRegistrationAndUnavailableCauses(t *testing.T) {
	root, read, helpers := nodeCatalogFixture(t)
	runner := map[string]any{"status": "installed"}
	tooling := autoNodeToolingDiscovery(root, read)
	row := autoNodeCapability(runner, helpers, tooling)
	if row["status"] != "on_demand" || tooling["status"] != "registered" {
		t.Fatal(row)
	}
	for _, name := range autoNodeCapabilityHelpers {
		saved := helpers[name]
		delete(helpers, name)
		if autoNodeCapability(runner, helpers, tooling)["status"] != "unavailable" {
			t.Fatal("missing helper accepted", name)
		}
		helpers[name] = saved
	}
	if autoNodeCapability(map[string]any{"status": "unavailable"}, helpers, tooling)["status"] != "unavailable" {
		t.Fatal("missing runner")
	}
	for _, reason := range []string{"missing", "permission"} {
		got := autoNodeToolingDiscovery(root, func(string, int64) ([]byte, error) { return nil, errors.New(reason) })
		if got["status"] != "unavailable" || got["reason"] == "" {
			t.Fatal(got)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(root, "active.json"))
	var active struct{ Key string }
	json.Unmarshal(raw, &active)
	path := filepath.Join(root, active.Key, "manifest.json")
	manifest, _ := os.ReadFile(path)
	os.Chmod(path, 0644)
	os.WriteFile(path, []byte(strings.Replace(string(manifest), "v24.13.1", "v24.13.2", 1)), 0444)
	if autoNodeToolingDiscovery(root, read)["status"] != "unavailable" {
		t.Fatal("tampered manifest accepted")
	}
	for _, value := range []string{`{`, `{"key":"../../host"}`, `{"key":"` + strings.Repeat("d", 64) + `"}`} {
		os.Chmod(filepath.Join(root, "active.json"), 0644)
		os.WriteFile(filepath.Join(root, "active.json"), []byte(value), 0444)
		if autoNodeToolingDiscovery(root, read)["status"] != "unavailable" {
			t.Fatal(value)
		}
	}
}
func TestNodeCapabilityExamplesAreExecutableRequirementInputs(t *testing.T) {
	root, read, helpers := nodeCatalogFixture(t)
	row := autoNodeCapability(map[string]any{"status": "installed"}, helpers, autoNodeToolingDiscovery(root, read))
	for _, example := range row["examples"].([]map[string]any) {
		raw, _ := json.Marshal(example)
		var req autonomy.Requirement
		if json.Unmarshal(raw, &req) != nil {
			t.Fatal(string(raw))
		}
		if _, e := autoNodeInputs(req); e != nil {
			t.Fatal("catalog advertises rejected input", e)
		}
	}
	raw, _ := json.Marshal(row)
	for _, text := range []string{"/requirements", "/prerequisite", "/environments", "lock_sha256", "runtime_digest", "generation", "credential-free", "not prove", "unsupported", "/scratch/node_modules"} {
		if !strings.Contains(string(raw), text) {
			t.Fatal("missing consumer contract", text)
		}
	}
	if strings.Contains(string(raw), root) {
		t.Fatal("host path disclosed")
	}
}
func TestNodeCapabilityActualWorkerBridgeShape(t *testing.T) {
	s := autoTestServer(t)
	w := httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", "/capabilities", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var body struct {
		Capabilities []map[string]any `json:"capabilities"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	found := 0
	for _, row := range body.Capabilities {
		if row["capability"] == "node_packages" {
			found++
			if row["condition"] != "offline_node_available" || row["schema_version"] != float64(1) || row["status"] == nil || row["tooling"] == nil || row["examples"] == nil {
				t.Fatal(row)
			}
		}
	}
	if found != 1 {
		t.Fatal("Node capability absent or duplicated", found)
	}
}
func TestNodeCapabilityRootPolicyRejectsUntrustedTooling(t *testing.T) {
	root, _, _ := nodeCatalogFixture(t)
	// Temp fixtures are user-owned (or writable while root-run); neither is a
	// privileged installation. A symlink is always invalid, independent of UID.
	active := filepath.Join(root, "active.json")
	os.Remove(active)
	os.Symlink("/etc/passwd", active)
	if autoNodeToolingDiscovery(root, autoReadRootImmutable)["status"] != "unavailable" {
		t.Fatal("followed selector symlink")
	}
}

func TestNodeCapabilityCapturedToolingManifest(t *testing.T) {
	root := os.Getenv("LECTERN_NODE_TOOLING_CATALOG_FIXTURE")
	if root == "" {
		t.Skip("actual root-owned Node tooling metadata fixture")
	}
	got := autoNodeToolingDiscovery(root, autoReadRootImmutable)
	if got["status"] != "registered" || got["node_version"] != "v24.13.1" || got["npm_version"] != "11.8.0" {
		t.Fatal(got)
	}
}
