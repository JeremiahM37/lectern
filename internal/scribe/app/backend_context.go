package app

import (
	"fmt"
	"strings"
)

// ClearContext implements the backend half of `/context clear|reset
// [memory|skills]`. "skills" unloads every skill loaded into context for the
// project. "memory" forgets which memories were already injected this session,
// so the next matching prompt may inject them again (the same reset compaction
// makes). The empty target means both. The conversation is never touched.
func (b *scribeBackend) ClearContext(target string) error {
	a := b.current()
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "skills":
		return b.clearSkills()
	case "memory":
		return resetMemoryRecall(a)
	case "", "all":
		if err := b.clearSkills(); err != nil {
			return err
		}
		return resetMemoryRecall(a)
	default:
		return fmt.Errorf("unknown context target %q (use memory or skills)", target)
	}
}

// resetMemoryRecall forgets the memories injected this session. Without a
// memory store there is nothing to reset, which is not an error.
func resetMemoryRecall(a *App) error {
	if a.Memory == nil {
		return nil
	}
	return a.Memory.ResetSurfaced()
}

// clearSkills unloads all active skills and rebuilds the system prompt.
func (b *scribeBackend) clearSkills() error {
	a := b.current()
	if err := writeActiveSkills(a.Workdir, nil); err != nil {
		return err
	}
	return a.refreshSkills()
}
