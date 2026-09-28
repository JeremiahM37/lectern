// Package lecternbin lets a test binary stand in for the lectern binary on a
// target. A test whose server runs `lectern pty …`, `lectern ptyhost serve`,
// `lectern helper …` or `lectern term-server …` sets the server's Self to
// os.Executable() and calls Run first thing in TestMain; when the test binary
// is started as one of those commands, Run does it and exits.
package lecternbin

import (
	"os"

	"github.com/JeremiahM37/lectern/v2/internal/helpers"
	"github.com/JeremiahM37/lectern/v2/internal/ptyhost"
	"github.com/JeremiahM37/lectern/v2/internal/terminal/webterm"
)

// Run executes os.Args as a target-side lectern command and exits, or
// returns when they are not one.
func Run() {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[1] {
	case "pty":
		os.Exit(ptyhost.Command(os.Args[2:]))
	case "ptyhost":
		os.Exit(ptyhost.HostCommand(os.Args[2:], "test"))
	case "helper":
		os.Exit(helpers.Main(os.Args[2:]))
	case "term-server":
		os.Exit(webterm.Command(os.Args[2:]))
	}
}
