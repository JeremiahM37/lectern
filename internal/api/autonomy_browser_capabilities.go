package api

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const autoBrowserProvisioner = "/usr/local/libexec/lectern-browser-runtime.py"

// Discovery describes registered assets. Full file verification and a real
// offline browser probe belong to each assignment's provisioning transaction.
func autoBrowserCapability(dependencies string) map[string]any {
	return autoBrowserCapabilityFromFiles(filepath.Join(dependencies, "browser"),
		autoCapabilityInstallation(autoBrowserProvisioner), autoReadRootImmutable)
}

func autoBrowserCapabilityFromFiles(root string, helper map[string]any, read func(string, int64) ([]byte, error)) map[string]any {
	out := map[string]any{
		"capability": "browser_tests", "status": "unavailable", "helper": helper,
		"engine": "chromium", "supported_playwright": []map[string]string{},
		"request":        "Request exact supported playwright and project dependency pins through python_wheels requirements. They resolve together with supplied pytest. Matching browser assets are bound into that environment before launch.",
		"test_command":   "/opt/browser-runtime/browsers/offline-test COMMAND ARGS",
		"scope":          "Registered local browser assets, not proof of delivery. The assignment must verify hashes, driver compatibility and an offline browser fixture. Run project tests with offline-test to remove model credentials, bridge sockets and external networking while retaining local HTTP fixtures.",
		"provenance":     "Local installed-cache inventory; no independent upstream archive checksum is claimed",
		"worker_receipt": "/prerequisite (python.browser_key)",
	}
	if helper["status"] != "installed" {
		out["reason"] = "Browser runtime verifier is not installed"
		return out
	}
	raw, err := read(filepath.Join(root, "active.json"), 16<<10)
	var selector struct {
		SchemaVersion int               `json:"schema_version"`
		Playwright    map[string]string `json:"playwright"`
	}
	if err != nil || json.Unmarshal(raw, &selector) != nil || selector.SchemaVersion != 1 || len(selector.Playwright) == 0 || len(selector.Playwright) > 16 {
		out["reason"] = "No bounded browser runtime selector is registered"
		return out
	}
	versions := make([]string, 0, len(selector.Playwright))
	for version := range selector.Playwright {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	rows := []map[string]string{}
	for _, version := range versions {
		key := selector.Playwright[version]
		if !autoHash256(key) || version == "" || len(version) > 64 || strings.ContainsAny(version, "/\\\x00\r\n") {
			continue
		}
		raw, err = read(filepath.Join(root, key, "manifest.json"), 4<<20)
		var manifest struct {
			SchemaVersion int                              `json:"schema_version"`
			Kind          string                           `json:"kind"`
			Key           string                           `json:"key"`
			Engine        string                           `json:"engine"`
			Playwright    string                           `json:"playwright_version"`
			Browser       string                           `json:"browser_version"`
			DriverSHA     string                           `json:"driver_declaration_sha256"`
			Provenance    string                           `json:"provenance"`
			Platform      struct{ System, Machine string } `json:"platform"`
			Revisions     map[string]string                `json:"revisions"`
		}
		machine := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
		if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 1 || manifest.Kind != "browser-runtime" || manifest.Key != key || manifest.Engine != "chromium" || manifest.Playwright != version || manifest.Browser == "" || !autoHash256(manifest.DriverSHA) || manifest.Provenance != "local-installed-cache-inventory" || runtime.GOOS != "linux" || machine == "" || manifest.Platform.System != "Linux" || manifest.Platform.Machine != machine || len(manifest.Revisions) != 3 || manifest.Revisions["chromium"] == "" || manifest.Revisions["chromium-headless-shell"] == "" || manifest.Revisions["ffmpeg"] == "" {
			continue
		}
		rows = append(rows, map[string]string{"playwright_version": version, "browser_version": manifest.Browser, "browser_key": key, "driver_declaration_sha256": manifest.DriverSHA})
	}
	out["supported_playwright"] = rows
	if len(rows) > 0 {
		out["status"] = "on_demand"
	} else {
		out["reason"] = "No matching declared browser runtime for this controller platform"
	}
	return out
}
