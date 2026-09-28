package main

import (
	"github.com/JeremiahM37/lectern/v2/internal/ptyhost"
	"github.com/JeremiahM37/lectern/v2/internal/terminal/webterm"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// The PTY host, its tmux-language client and the web terminal server
// (docs/ptyhost.md) live with their packages, so a test binary can stand in
// for lectern on a target too (internal/testutil/lecternbin).

func ptyCommand(args []string) int        { return ptyhost.Command(args) }
func ptyhostCommand(args []string) int    { return ptyhost.HostCommand(args, buildLabel()) }
func termServerCommand(args []string) int { return webterm.Command(args) }

func buildLabel() string {
	v := version.Current()
	label := v.Version
	if v.Revision != "" {
		label += " (" + v.Revision + ")"
	}
	return label
}
