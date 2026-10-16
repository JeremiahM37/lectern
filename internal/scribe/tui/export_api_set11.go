package tui

// /session export api toggles the per-step request dump: one JSON line per
// model request, with headers removed and secrets redacted. The dump is on
// only for saved sessions. The switch itself lives in the backend; this file
// is the command side and the optional interface it talks to.

import (
	"fmt"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// APIDumper is the optional backend interface behind /session export api. A
// backend that cannot dump requests (or has no saved session) does not
// implement it, and the command says so.
type APIDumper interface {
	// APIDump reports whether the per-step request dump is on.
	APIDump() bool
	// SetAPIDump turns the dump on or off. When on it returns the file the
	// requests are appended to; when off, an empty path.
	SetAPIDump(on bool) (path string, err error)
}

// apiDumpUsage is the usage line for the api form of /session export.
const apiDumpUsage = "Usage: /session export api [on|off]"

// exportAPI is the `api` form of /session export. With no argument it flips
// the dump; "on" or "off" set it.
func (m *Model) exportAPI(arg string) tea.Cmd {
	d, ok := m.cur().be.(APIDumper)
	if !ok {
		return cmdErrText("this session cannot write an API request dump")
	}
	on := !d.APIDump()
	if arg = strings.TrimSpace(arg); arg != "" {
		v, err := parseOnOff(arg)
		if err != nil {
			return cmdErrText(apiDumpUsage)
		}
		on = v
	}
	path, err := d.SetAPIDump(on)
	if err != nil {
		return cmdErr(err)
	}
	if !on {
		return flashCmd("ok", "API request dump off")
	}
	return flashCmd("ok", fmt.Sprintf("API request dump on: %s (one line per model request)", path))
}

// fakeAPIDump is the demo and test backend's switch. It is package state
// because FakeBackend is defined in fake.go, which this change does not own.
var (
	fakeAPIDumpMu sync.Mutex
	fakeAPIDumpOn bool
)

// APIDump reports the fake's dump switch.
func (f *FakeBackend) APIDump() bool {
	fakeAPIDumpMu.Lock()
	defer fakeAPIDumpMu.Unlock()
	return fakeAPIDumpOn
}

// SetAPIDump flips the fake's dump switch and returns a made-up path.
func (f *FakeBackend) SetAPIDump(on bool) (string, error) {
	fakeAPIDumpMu.Lock()
	defer fakeAPIDumpMu.Unlock()
	fakeAPIDumpOn = on
	if !on {
		return "", nil
	}
	return ".scribe/sessions/fake/requests.jsonl", nil
}
