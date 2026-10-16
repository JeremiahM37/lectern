package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// PolicyFile is the name of the memory settings file. It is read from the
// project's .scribe directory and from the Scribe home; the project file wins
// key by key.
const PolicyFile = "memory-config.json"

// Policy controls where memory is written and read. Values that are not
// recognised are ignored, so a typo falls back to the default rather than
// switching memory off without a trace.
type Policy struct {
	// WriteScope is where a save with no scope goes: "project", "global" or
	// "none" (saves are refused). "all" is accepted and means "project".
	// Empty means project when a project store is open, else global.
	WriteScope string `json:"writeScope,omitempty"`
	// ReadScope is which stores automatic recall reads: "project", "global",
	// "all" or "none". Empty means all. Explicit search and list calls name
	// their own scope and are not affected.
	ReadScope string `json:"readScope,omitempty"`
}

// LoadPolicy reads memory-config.json from the Scribe home and then from
// <root>/.scribe, the project file overriding the home file. A missing or
// unreadable file contributes nothing.
func LoadPolicy(root string) Policy {
	var p Policy
	paths := []string{filepath.Join(config.Home(), PolicyFile)}
	if root != "" {
		paths = append(paths, filepath.Join(config.ProjectDir(root), PolicyFile))
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var f Policy
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		if validWriteScope(f.WriteScope) {
			p.WriteScope = f.WriteScope
		}
		if validReadScope(f.ReadScope) {
			p.ReadScope = f.ReadScope
		}
	}
	return p
}

func validWriteScope(s string) bool {
	switch s {
	case "project", "global", "none", "all":
		return true
	}
	return false
}

func validReadScope(s string) bool {
	switch s {
	case "project", "global", "all", "none":
		return true
	}
	return false
}

// errWritesOff is returned by saves when the policy turns writing off.
var errWritesOff = errors.New("memory writes are turned off (writeScope is \"none\" in " + PolicyFile + ")")

// writeDB returns the store a save with no explicit scope goes to.
func (s *Stores) writeDB() (*DB, error) {
	if s.Policy.WriteScope == "none" {
		return nil, errWritesOff
	}
	if s.Policy.WriteScope == ScopeGlobal && s.Global != nil {
		return s.Global, nil
	}
	if s.Project != nil {
		return s.Project, nil
	}
	if s.Global != nil {
		return s.Global, nil
	}
	return nil, errors.New("no memory store is open")
}

// readDBs returns the stores automatic recall reads, per ReadScope.
func (s *Stores) readDBs() []*DB {
	switch s.Policy.ReadScope {
	case "none":
		return nil
	case ScopeProject:
		if s.Project != nil {
			return []*DB{s.Project}
		}
		return nil
	case ScopeGlobal:
		if s.Global != nil {
			return []*DB{s.Global}
		}
		return nil
	}
	return s.DBs()
}
