package tui

import "testing"

// The spec's prefix forms (spec 02, section 1) must each resolve to a command
// with the argument text after it. No command sets Match today: each form's
// base word is a registered name, and findCommand's name lookup takes the
// longest name that prefixes the line. This test pins that, so a renamed or
// removed base word shows up here rather than as a dead command.
func TestSpecPrefixFormsResolveWithoutAMatcher(t *testing.T) {
	cases := []struct {
		line string
		want string // the command's Name
		args string
	}{
		{"/tab rename work", "tab", "rename work"},
		{"/git branch feature-x", "git branch", "feature-x"},
		{"/git co-author on", "git", "co-author on"},
		{"/font set nerd", "font", "set nerd"},
		{"/privacy add *.env", "privacy", "add *.env"},
		{"/context clear", "context", "clear"},
		{"/session compact logs", "session", "compact logs"},
		{"/model events", "model events", ""},
		{"/lsp install gopls", "lsp install", "gopls"},
		{"/hearth bind work", "hearth", "bind work"},
		{"/terminals rename x", "terminals", "rename x"},
		{"/editor split", "editor", "split"},
	}
	for _, c := range cases {
		cmd, args := findCommand(c.line)
		if cmd == nil {
			t.Errorf("%q resolved to no command", c.line)
			continue
		}
		if cmd.Name != c.want || args != c.args {
			t.Errorf("%q -> %q args %q, want %q args %q", c.line, cmd.Name, args, c.want, c.args)
		}
	}
}
