package api

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBrowserDiscoveryRequiresMatchingRegisteredRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux runtime")
	}
	machine := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if machine == "" {
		t.Skip("unsupported browser architecture")
	}
	key := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name      string
		mutate    func(map[string]any)
		available bool
	}{
		{"registered", func(m map[string]any) {}, true},
		{"wrong platform", func(m map[string]any) { m["platform"] = map[string]string{"system": "Linux", "machine": "wrong"} }, false},
		{"wrong version", func(m map[string]any) { m["playwright_version"] = "other" }, false},
		{"wrong key", func(m map[string]any) { m["key"] = strings.Repeat("b", 64) }, false},
		{"missing browser version", func(m map[string]any) { delete(m, "browser_version") }, false},
		{"missing revision", func(m map[string]any) { m["revisions"] = map[string]string{"chromium": "1234"} }, false},
		{"unsupported provenance", func(m map[string]any) { m["provenance"] = "unverified" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := map[string]any{"schema_version": 1, "kind": "browser-runtime", "key": key, "engine": "chromium", "playwright_version": "1.62.0", "browser_version": "151", "driver_declaration_sha256": strings.Repeat("c", 64), "provenance": "local-installed-cache-inventory", "platform": map[string]string{"system": "Linux", "machine": machine}, "revisions": map[string]string{"chromium": "1234", "chromium-headless-shell": "1234", "ffmpeg": "1011"}}
			tc.mutate(manifest)
			files := map[string]any{filepath.Join("runtime", "active.json"): map[string]any{"schema_version": 1, "playwright": map[string]string{"1.62.0": key}}, filepath.Join("runtime", key, "manifest.json"): manifest}
			read := func(path string, limit int64) ([]byte, error) {
				v, ok := files[path]
				if !ok {
					return nil, fmt.Errorf("unexpected path %s", path)
				}
				b, e := json.Marshal(v)
				if int64(len(b)) > limit {
					t.Fatal("fixture exceeds bound")
				}
				return b, e
			}
			got := autoBrowserCapabilityFromFiles("runtime", map[string]any{"status": "installed"}, read)
			if (got["status"] == "on_demand") != tc.available {
				t.Fatalf("unexpected discovery: %v", got)
			}
			if tc.available && !strings.Contains(got["scope"].(string), "not proof of delivery") {
				t.Fatal("registration must not claim delivery")
			}
		})
	}
}

func TestBrowserDiscoveryUnavailableDoesNotReadUntrustedPaths(t *testing.T) {
	called := false
	read := func(path string, limit int64) ([]byte, error) { called = true; return nil, fmt.Errorf("missing") }
	got := autoBrowserCapabilityFromFiles("runtime", map[string]any{"status": "unavailable"}, read)
	if called || got["status"] != "unavailable" {
		t.Fatal(got)
	}
	read = func(path string, limit int64) ([]byte, error) {
		if filepath.Base(path) != "active.json" {
			t.Fatal("unsafe manifest read", path)
		}
		return []byte(`{"schema_version":1,"playwright":{"../../escape":"../../escape"}}`), nil
	}
	got = autoBrowserCapabilityFromFiles("runtime", map[string]any{"status": "installed"}, read)
	if got["status"] != "unavailable" {
		t.Fatal(got)
	}
}
