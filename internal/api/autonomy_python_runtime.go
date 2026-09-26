package api

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// Discovery is not admission: the privileged runner verifies the complete
// immutable bundle and interpreter before mounting it for a worker.
func autoPythonTestRuntime(root string) map[string]any {
	absent := map[string]any{"status": "unavailable", "capability": "python_test_runtime", "reason": "No supported offline Python test runtime is provisioned"}
	read := func(path string, limit int64) ([]byte, error) {
		st, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() || st.Size() > limit {
			return nil, os.ErrInvalid
		}
		return os.ReadFile(path)
	}
	raw, err := read(filepath.Join(root, "active.json"), 1024)
	if err != nil {
		return absent
	}
	var active struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(raw, &active) != nil || len(active.Key) != 64 {
		return absent
	}
	if _, err = hex.DecodeString(active.Key); err != nil {
		return absent
	}
	raw, err = read(filepath.Join(root, active.Key, "manifest.json"), 2<<20)
	if err != nil {
		return absent
	}
	var info struct {
		Key              string            `json:"key"`
		Kind             string            `json:"kind"`
		RuntimeFamily    string            `json:"runtime_family"`
		ChecksumVerified bool              `json:"checksum_verified"`
		Packages         map[string]string `json:"packages"`
	}
	if json.Unmarshal(raw, &info) != nil || info.Key != active.Key || info.Kind != "python-test-runtime" || !info.ChecksumVerified || info.Packages["pytest"] == "" {
		return absent
	}
	return map[string]any{"status": "provisioned", "capability": "python_test_runtime", "key": info.Key, "runtime_family": info.RuntimeFamily, "packages": info.Packages, "command": "python3 -m pytest", "manifest": "/opt/python-test-runtime.json", "scope": "Offline test tooling only, not project dependencies or Django. The runner verifies ownership, complete checksums and interpreter before mounting. Existing workers retain their original environment; inspect the mounted manifest before claiming availability."}
}
