package tui

// /context clear and /context reset (spec 02 section 2): clear what is
// injected into context. "skills" unloads the project's active skills,
// "memory" resets the memory recall so relevant memories may be injected
// again, and no target (or "all") does both. There is no confirmation, as
// the spec asks; the injected context is rebuilt from the project each time
// it is needed, so nothing saved is lost. The conversation is not touched.

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func init() {
	// The dashboard (no argument) and any other argument keep their
	// existing behaviour; only clear and reset are handled here.
	var dashboard func(m *Model, args string) tea.Cmd
	for _, c := range commandTable {
		if c.Name == "context" {
			dashboard = c.Run
		}
	}
	overrideCommands(&command{Name: "context", Args: "[clear|reset] [memory|skills]",
		Desc: "Context window and token use. /context clear or reset unloads skills and memory recall", Group: "Intelligence",
		Run: func(m *Model, args string) tea.Cmd {
			if verb, _, ok := contextVerb8(args); ok {
				return contextClear8(m, verb, args)
			}
			if dashboard == nil {
				return flashCmd("err", "Usage: /context, or /context clear [memory|skills]")
			}
			return dashboard(m, args)
		}})
}

// contextVerb8 reads the first word of a /context argument: clear or reset,
// in any case. It reports the rest of the words as well.
func contextVerb8(args string) (string, []string, bool) {
	f := strings.Fields(args)
	if len(f) == 0 {
		return "", nil, false
	}
	switch v := strings.ToLower(f[0]); v {
	case "clear", "reset":
		return v, f[1:], true
	}
	return "", nil, false
}

// contextTarget8 maps the word after clear or reset to a backend target. The
// empty string means both. Plural and short forms are accepted because they
// are the usual way to write it.
func contextTarget8(word string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "", "all", "everything":
		return "", true
	case "skill", "skills":
		return "skills", true
	case "memory", "memories", "mem":
		return "memory", true
	}
	return "", false
}

// contextClear8 runs /context clear or /context reset.
func contextClear8(m *Model, verb string, args string) tea.Cmd {
	rest := strings.Fields(args)[1:]
	if len(rest) > 1 {
		return flashCmd("err", fmt.Sprintf("Usage: /context %s [memory|skills]. Give one target, or none for both.", verb))
	}
	word := ""
	if len(rest) == 1 {
		word = rest[0]
	}
	target, ok := contextTarget8(word)
	if !ok {
		return flashCmd("err", fmt.Sprintf("Unknown target %q. Use memory, skills, or no target for both.", word))
	}
	cc, ok := m.cur().be.(ContextClearer)
	if !ok {
		return errFlash(errors.New("this backend cannot clear context"))
	}
	if err := cc.ClearContext(target); err != nil {
		return errFlash(err)
	}
	switch target {
	case "skills":
		return flashCmd("ok", "Skills unloaded from context")
	case "memory":
		return flashCmd("ok", "Memory recall reset: relevant memories may be injected again on the next prompt")
	}
	return flashCmd("ok", "Skills unloaded and memory recall reset; the conversation is kept")
}
