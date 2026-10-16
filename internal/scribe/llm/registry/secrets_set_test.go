package registry

import (
	"path/filepath"
	"testing"
)

func TestSetSecretStoresSearchKeysOnly(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{keysPath: filepath.Join(dir, "keys.json"), byID: map[string]int{}}
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	if err := r.SetSecret(BraveKeyID, "  brave-val  "); err != nil {
		t.Fatal(err)
	}
	if v, src := r.Secret(BraveKeyID, ""); v != "brave-val" || src != "store" {
		t.Fatalf("stored brave key = %q from %q", v, src)
	}
	if err := r.SetSecret("openai", "x"); err == nil {
		t.Fatal("a provider id must not be accepted as a search key")
	}
	if err := r.SetSecret(JinaKeyID, "   "); err == nil {
		t.Fatal("an empty key must be refused")
	}
	if err := r.RemoveSecret(BraveKeyID); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.Secret(BraveKeyID, ""); v != "" {
		t.Fatalf("key still present after RemoveSecret: %q", v)
	}
	if err := r.RemoveSecret(BraveKeyID); err != nil {
		t.Fatalf("removing an absent key must not fail: %v", err)
	}
}
