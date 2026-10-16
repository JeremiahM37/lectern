package app

import (
	"fmt"
	"strings"
)

// ClearContext implements the backend half of `/context clear [memory|skills]`.
// target "skills" unloads every skill loaded into context for the project; the
// empty target means everything. Memory is not held in context: Scribe recalls
// it again for each prompt, so there is no persistent memory to clear and that
// target is refused with an explanation rather than reported as done.
func (b *scribeBackend) ClearContext(target string) error {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "", "all", "skills":
		return b.clearSkills()
	case "memory":
		return fmt.Errorf("memory is not kept in context: it is recalled again for every prompt, so there is nothing to clear")
	default:
		return fmt.Errorf("unknown context target %q (use memory or skills)", target)
	}
}

// clearSkills unloads all active skills and rebuilds the system prompt.
func (b *scribeBackend) clearSkills() error {
	a := b.current()
	if err := writeActiveSkills(a.Workdir, nil); err != nil {
		return err
	}
	return a.refreshSkills()
}
