package registry

import (
	"os"
	"strings"
)

// Secret ids for keys that belong to no provider. They live in the same key
// store as provider keys, under these names, so `lectern code --set-key`
// and the settings screen can store them.
const (
	BraveKeyID = "brave-api-key"
	JinaKeyID  = "jina-api-key"
)

// staticSecretEnv is the environment variable each non-provider key defaults
// to. Config search.brave_key_env and search.jina_key_env override the name.
var staticSecretEnv = map[string]string{
	BraveKeyID: "BRAVE_SEARCH_API_KEY",
	JinaKeyID:  "JINA_API_KEY",
}

// Secret resolves a non-provider key. envVar names the environment variable to
// read; empty uses the default for id. Priority follows keyPriority, as for
// provider keys: environment first unless set to "app". source is
// "env:NAME", "store" or "" when nothing resolves. A store that cannot be
// read counts as empty, so a search still runs without a key.
func (r *Registry) Secret(id, envVar string) (value, source string) {
	if envVar == "" {
		envVar = staticSecretEnv[id]
	}
	fromEnv := func() (string, string) {
		if envVar == "" {
			return "", ""
		}
		if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
			return v, "env:" + envVar
		}
		return "", ""
	}
	fromStore := func() (string, string) {
		keys, err := readKeys(r.keysPath)
		if err != nil || keys[id] == "" {
			return "", ""
		}
		return keys[id], "store"
	}
	r.mu.Lock()
	app := r.keyPriority == "app"
	r.mu.Unlock()
	first, second := fromEnv, fromStore
	if app {
		first, second = fromStore, fromEnv
	}
	if v, src := first(); v != "" {
		return v, src
	}
	return second()
}
