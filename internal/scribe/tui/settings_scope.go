package tui

// Scope for the direct-argument forms of the settings commands. The rule is
// the one the settings screen uses (spec 02 §1.2): a value with no scope word
// is saved where its key already lives, the project when the project file
// sets it and the global file otherwise. "project" or "global" at the end of
// the argument forces the scope. Forms that take a scope word also accept it
// at the start.

import "strings"

// splitScopeArg separates a trailing or leading scope word from a direct
// argument. It returns the rest of the argument and the scope word ("" when
// there is none). "--project" and "--global" are accepted too.
func splitScopeArg(arg string) (value, scope string) {
	fields := strings.Fields(arg)
	if n := len(fields); n > 0 {
		if w := scopeWord(fields[n-1]); w != "" {
			return strings.Join(fields[:n-1], " "), w
		}
		if w := scopeWord(fields[0]); w != "" {
			return strings.Join(fields[1:], " "), w
		}
	}
	return strings.TrimSpace(arg), ""
}

func scopeWord(s string) string {
	switch strings.ToLower(strings.TrimPrefix(s, "--")) {
	case "project":
		return "project"
	case "global":
		return "global"
	}
	return ""
}

// scopeIsProject resolves a scope word for key. An empty word means the
// scope the key already lives in: the project if the project file sets it.
func (m *Model) scopeIsProject(word, key string) bool {
	switch word {
	case "project":
		return true
	case "global":
		return false
	}
	return savedIn(m.cur().be.Workdir(), true, key)
}

// saveFlatIn writes one flat key to the given scope.
func (m *Model) saveFlatIn(key string, value any, project bool) error {
	w, err := m.flatWriter()
	if err != nil {
		return err
	}
	return w.SetField7(key, value, project)
}

// scopeName names a scope for a confirmation line.
func scopeName(project bool) string {
	if project {
		return "project"
	}
	return "global"
}
