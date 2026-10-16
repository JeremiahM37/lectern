package app

// Backend side of the wave 7 settings: the flat keys that config.SetField
// owns (diff style, router slots, agent features, editor, search, retry, tool
// timeout, privacy) and the search keys. Keys are never written to config.json:
// Brave and Jina keys go to the key store through the registry, and the config
// only names the environment variable that may also hold them.

import (
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// Compile-time checks that the backend provides the optional capabilities
// the settings screen and the wave 7 commands look for.
var (
	_ tui.ConfigFields7 = (*scribeBackend)(nil)
	_ tui.ConfigSetter7 = (*scribeBackend)(nil)
	_ tui.SecretStore7  = (*scribeBackend)(nil)
)

// MergedConfig7 returns the merged settings: project over global, as the
// agent sees them. It is a copy, so a caller cannot change the live config.
func (b *scribeBackend) MergedConfig7() config.Config {
	a := b.current()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Cfg
}

// SetField7 writes one flat key (see config.SetField) to the project or the
// global file and reloads the merged settings. A key this build does not own
// is an error, so nothing is written for it.
func (b *scribeBackend) SetField7(key string, value any, project bool) error {
	a := b.current()
	raw, err := loadConfigFile(configPathFor(a.Workdir, project))
	if err != nil {
		return err
	}
	ok, err := raw.SetField(key, value)
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	if err != nil {
		return err
	}
	return b.saveAndReload(a, raw, project)
}

// MoveField7 saves a flat key to the chosen scope and removes it from the
// other, so it lives in one place afterwards. The value is the current one.
func (b *scribeBackend) MoveField7(key string, value any, toProject bool) error {
	probe := config.Config{}
	if !probe.ClearField(key) {
		return fmt.Errorf("%q cannot be moved between scopes", key)
	}
	if err := b.SetField7(key, value, toProject); err != nil {
		return err
	}
	a := b.current()
	from := configPathFor(a.Workdir, !toProject)
	raw, err := loadConfigFile(from)
	if err != nil {
		return err
	}
	if !raw.ClearField(key) {
		return fmt.Errorf("%q cannot be moved between scopes", key)
	}
	return b.saveAndReload(a, raw, !toProject)
}

// saveAndReload writes raw to the scope and makes the merged settings current.
func (b *scribeBackend) saveAndReload(a *App, raw config.Config, project bool) error {
	if err := saveConfigFile(a.Workdir, raw, project); err != nil {
		return err
	}
	merged, err := reloadConfig(a.Workdir)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.Cfg = merged
	a.mu.Unlock()
	return nil
}

// SetSecret stores a Brave or Jina key in the key store. The value is not
// kept anywhere else and is never returned.
func (b *scribeBackend) SetSecret(id, value string) error {
	return b.current().Registry.SetSecret(id, value)
}

// RemoveSecret deletes a stored Brave or Jina key.
func (b *scribeBackend) RemoveSecret(id string) error {
	return b.current().Registry.RemoveSecret(id)
}

// SecretSource says where a search key comes from: "env:NAME", "store", or ""
// when none is set. It never returns the key itself.
func (b *scribeBackend) SecretSource(id string) string {
	a := b.current()
	a.mu.Lock()
	search := a.Cfg.Search
	a.mu.Unlock()
	env := search.BraveEnv()
	if id == registry.JinaKeyID {
		env = search.JinaEnv()
	}
	_, src := a.Registry.Secret(id, env)
	return src
}
