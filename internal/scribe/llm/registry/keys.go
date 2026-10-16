package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// keysFile is the key store: <Home>/keys.json, a JSON object of provider id
// to key. It is written mode 0600 inside a 0700 directory.
func keysFile() string {
	return filepath.Join(config.Home(), "keys.json")
}

// readKeys loads the store. A missing file is an empty store.
func readKeys(path string) (map[string]string, error) {
	keys := map[string]string{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return keys, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read key store: %w", err)
	}
	if len(b) == 0 {
		return keys, nil
	}
	if err := json.Unmarshal(b, &keys); err != nil {
		return nil, fmt.Errorf("key store %s is not valid JSON: %w", path, err)
	}
	if keys == nil {
		keys = map[string]string{}
	}
	return keys, nil
}

// writeKeys replaces the store atomically: write a 0600 temp file, rename it
// over the old one.
func writeKeys(path string, keys map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create key store dir: %w", err)
	}
	b, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write key store: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write key store: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write key store: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace key store: %w", err)
	}
	return nil
}
