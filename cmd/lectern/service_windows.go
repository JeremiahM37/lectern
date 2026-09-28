//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// installWindowsLogon starts the local board at every logon through the
// user's own Run key: no administrator rights, no service wrapper, and it
// runs only while the user is signed in, as their agents do. `up` starts the
// runtime detached and exits, so no console window stays open.
func installWindowsLogon(binary string) (string, error) {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer key.Close()
	command := fmt.Sprintf(`"%s" up --no-browser`, binary)
	if err := key.SetStringValue("Lectern", command); err != nil {
		return "", err
	}
	return "Lectern now starts at logon (HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run\\Lectern).\n" +
		"Remove that value to stop it; `lectern local stop` stops the running board.", nil
}
