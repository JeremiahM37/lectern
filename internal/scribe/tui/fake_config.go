package tui

import "github.com/JeremiahM37/lectern/v2/internal/scribe/config"

// The fake keeps flat settings in memory, so the demo's settings screen saves
// and re-reads them the way the real backend does.

var (
	_ ConfigFields7 = (*FakeBackend)(nil)
	_ ConfigSetter7 = (*FakeBackend)(nil)
)

// MergedConfig7 returns the in-memory settings.
func (f *FakeBackend) MergedConfig7() config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flat
}

// SetField7 saves one flat key. The fake has one scope, so project is ignored.
func (f *FakeBackend) SetField7(key string, value any, project bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := f.flat.SetField(key, value)
	return err
}

// MoveField7 is a no-op: the fake has a single scope.
func (f *FakeBackend) MoveField7(key string, value any, toProject bool) error { return nil }
