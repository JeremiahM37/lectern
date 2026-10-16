package registry

import (
	"fmt"
	"strings"
)

// SetSecret stores a key that belongs to no provider (BraveKeyID or JinaKeyID)
// in the same key store as provider keys. Only the non-provider ids are
// accepted, so this cannot overwrite a provider's key. An empty value is an
// error; use RemoveSecret to delete one.
func (r *Registry) SetSecret(id, key string) error {
	if id != BraveKeyID && id != JinaKeyID {
		return fmt.Errorf("%q is not a search key", id)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("empty key; use RemoveSecret to delete one")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keys, err := readKeys(r.keysPath)
	if err != nil {
		return err
	}
	keys[id] = key
	return writeKeys(r.keysPath, keys)
}

// RemoveSecret deletes a stored non-provider key. Environment variables are
// not touched, and removing a key that is not stored is not an error.
func (r *Registry) RemoveSecret(id string) error {
	if id != BraveKeyID && id != JinaKeyID {
		return fmt.Errorf("%q is not a search key", id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keys, err := readKeys(r.keysPath)
	if err != nil {
		return err
	}
	if _, ok := keys[id]; !ok {
		return nil
	}
	delete(keys, id)
	return writeKeys(r.keysPath, keys)
}
