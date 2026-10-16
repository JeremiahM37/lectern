package hooks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// trustFile is <config.Home()>/trusted.json.
type trustFile struct {
	Roots []string `json:"roots"`
}

// TrustPath is where the list of trusted project roots is kept.
func TrustPath() string { return filepath.Join(config.Home(), "trusted.json") }

// canonicalRoot is the key a project is trusted under: absolute, with
// symlinks resolved when the path exists.
func canonicalRoot(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs)
}

func readTrust() (trustFile, error) {
	var t trustFile
	b, err := os.ReadFile(TrustPath())
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	if len(b) == 0 {
		return t, nil
	}
	err = json.Unmarshal(b, &t)
	return t, err
}

func writeTrust(t trustFile) error {
	if err := os.MkdirAll(filepath.Dir(TrustPath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := TrustPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, TrustPath())
}

// Trusted reports whether the project at root may run its own hooks and
// MCP servers (the repo's .claude/settings.json, .scribe/config.json,
// .mcp.json). A missing or unreadable trust file means untrusted.
func Trusted(root string) bool {
	t, err := readTrust()
	if err != nil {
		return false
	}
	return slices.Contains(t.Roots, canonicalRoot(root))
}

// Trust records that the project at root may run its own hooks and MCP
// servers. Only call this after the user has said yes.
func Trust(root string) error {
	t, err := readTrust()
	if err != nil {
		return err
	}
	key := canonicalRoot(root)
	if !slices.Contains(t.Roots, key) {
		t.Roots = append(t.Roots, key)
	}
	return writeTrust(t)
}

// Untrust removes the project at root from the trust list.
func Untrust(root string) error {
	t, err := readTrust()
	if err != nil {
		return err
	}
	key := canonicalRoot(root)
	t.Roots = slices.DeleteFunc(t.Roots, func(s string) bool { return s == key })
	return writeTrust(t)
}
