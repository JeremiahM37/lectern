package api

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

type autoDependencyInfo struct {
	Key              string `json:"key"`
	Module           string `json:"module"`
	ProjectID        int64  `json:"project_id"`
	SourceTaskID     int64  `json:"source_task_id"`
	ChecksumVerified bool   `json:"checksum_verified"`
	GoVersion        string `json:"go_version"`
}

// Discovery only: the privileged runner checks the exact source input digest
// and trusted ownership before mounting anything into a worker.
func autoDependencyCatalog(root string) []autoDependencyInfo {
	rows := []autoDependencyInfo{}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		key := entry.Name()
		if !entry.IsDir() || len(key) != 64 {
			continue
		}
		if _, err := hex.DecodeString(key); err != nil {
			continue
		}
		path := filepath.Join(root, key, "manifest.json")
		st, err := os.Lstat(path)
		if err != nil || !st.Mode().IsRegular() || st.Size() > 65536 {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var info autoDependencyInfo
		if json.Unmarshal(raw, &info) != nil || info.Key != key || !info.ChecksumVerified || info.Module == "" {
			continue
		}
		rows = append(rows, info)
	}
	return rows
}
