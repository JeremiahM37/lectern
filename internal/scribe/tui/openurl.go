package tui

import (
	"os/exec"
	"runtime"
)

// lecternRepo is where release notes and downloads live.
const lecternRepo = "https://github.com/JeremiahM37/lectern"

// releasePage is the GitHub page for one release, or the releases index when
// the version is unknown.
func releasePage(latest string) string {
	if latest == "" {
		return lecternRepo + "/releases"
	}
	tag := latest
	if tag[0] != 'v' {
		tag = "v" + tag
	}
	return lecternRepo + "/releases/tag/" + tag
}

// openURL hands a link to the desktop's browser opener. It does not wait for
// the browser to exit. A host with no opener gets an error, and the caller
// shows the link instead.
func openURL(u string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, u)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
