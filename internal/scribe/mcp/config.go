package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// UpdateServers rewrites the "mcp" list of the Scribe config file at path
// with edit. Every other key in the file is kept as it was. A missing file
// is created. A file that is not valid JSON is left untouched and the error
// is returned. The list is written 2-space indented, mode 0600, through a
// temporary file, so a crash never leaves a half-written config behind.
func UpdateServers(path string, edit func([]config.MCPServer) ([]config.MCPServer, error)) error {
	raw := map[string]json.RawMessage{}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	case len(bytes.TrimSpace(b)) > 0:
		if err := json.Unmarshal(b, &raw); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if raw == nil {
			raw = map[string]json.RawMessage{}
		}
	}

	var list []config.MCPServer
	if v, ok := raw["mcp"]; ok && string(bytes.TrimSpace(v)) != "null" {
		if err := json.Unmarshal(v, &list); err != nil {
			return fmt.Errorf("%s: mcp: %w", path, err)
		}
	}
	next, err := edit(list)
	if err != nil {
		return err
	}
	if len(next) == 0 {
		delete(raw, "mcp")
	} else {
		v, err := json.Marshal(next)
		if err != nil {
			return err
		}
		raw["mcp"] = v
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PutServer returns list with s in place of the server called prev, or with
// s appended when prev is "". Any other server called s.Name is dropped, so
// a name never appears twice in one file. Renaming goes through prev.
func PutServer(list []config.MCPServer, prev string, s config.MCPServer) []config.MCPServer {
	target := -1
	for i, old := range list {
		if prev != "" && old.Name == prev {
			target = i
			break
		}
	}
	if target < 0 {
		for i, old := range list {
			if old.Name == s.Name {
				target = i
				break
			}
		}
	}
	out := make([]config.MCPServer, 0, len(list)+1)
	for i, old := range list {
		switch {
		case i == target:
			out = append(out, s)
		case old.Name == s.Name || (prev != "" && old.Name == prev):
			// The same name elsewhere, or the entry being renamed away.
		default:
			out = append(out, old)
		}
	}
	if target < 0 {
		out = append(out, s)
	}
	return out
}

// RemoveServer returns list without the server called name, and whether one
// was removed.
func RemoveServer(list []config.MCPServer, name string) ([]config.MCPServer, bool) {
	out := make([]config.MCPServer, 0, len(list))
	found := false
	for _, s := range list {
		if s.Name == name {
			found = true
			continue
		}
		out = append(out, s)
	}
	return out, found
}
