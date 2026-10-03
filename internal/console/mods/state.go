package mods

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// statePath is where a mod's $.state lives: one JSON object per mod, beside
// the console's saved dashboard view for the same server.
func (r *runtime) statePath() string {
	dir := r.host.opts.StateDir
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, safeName(r.mod.Plugin)+"__"+safeName(r.mod.ID)+".json")
}

func safeName(s string) string {
	s = strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' {
			return c
		}
		return '_'
	}, s)
	if strings.Trim(s, ".") == "" {
		return "_"
	}
	return s
}

// loadState reads the saved state once. A missing or unreadable file starts
// empty; an unreadable one is not overwritten until the mod sets a value.
// Callers hold r.mu.
func (r *runtime) loadState() {
	if r.state != nil {
		return
	}
	r.state = map[string]any{}
	path := r.statePath()
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(stateLimit)+1))
	if err != nil || len(data) > stateLimit {
		return
	}
	_ = json.Unmarshal(data, &r.state)
	if r.state == nil {
		r.state = map[string]any{}
	}
}

func (r *runtime) stateGet(key string) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadState()
	return r.state[key]
}

// stateSet stores value (nil deletes the key) and writes the whole object.
// Over the cap, nothing changes and the mod gets an error.
func (r *runtime) stateSet(key string, value any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadState()
	next := make(map[string]any, len(r.state)+1)
	for k, v := range r.state {
		next[k] = v
	}
	if value == nil {
		delete(next, key)
	} else {
		v, err := jsonValue(value)
		if err != nil {
			return err
		}
		next[key] = v
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(data) > stateLimit {
		return fmt.Errorf("$.state is limited to %d KB per mod", stateLimit>>10)
	}
	if path := r.statePath(); path != "" {
		if err := writeFileAtomic(path, data); err != nil {
			return err
		}
	}
	r.state = next
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return errors.New("save mod state: " + err.Error())
	}
	return nil
}
