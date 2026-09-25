package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Desktop windows must be launched on the client machine. Without a display,
// the existing tmux workspace remains available to ordinary SSH clients.
func desktopTerminalAvailable() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != ""
}

func desktopTerminalCommand(script string) ([]string, error) {
	for _, candidate := range []struct {
		name string
		args []string
	}{
		{"kitty", []string{"--detach", "--title", "Lectern", script}},
		{"konsole", []string{"--separate", "-e", script}},
		{"alacritty", []string{"--title", "Lectern", "-e", script}},
		{"gnome-terminal", []string{"--window", "--", script}},
		{"xterm", []string{"-T", "Lectern", "-e", script}},
	} {
		if bin, err := exec.LookPath(candidate.name); err == nil {
			return append([]string{bin}, candidate.args...), nil
		}
	}
	return nil, fmt.Errorf("no supported desktop terminal found (Kitty, Konsole, Alacritty, GNOME Terminal or xterm)")
}
func openDesktopTerminal(base, token, kind, id string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "lectern-window-")
	if err != nil {
		return err
	}
	script := filepath.Join(dir, "open.sh")
	if err = os.WriteFile(script, []byte(terminalTabScript(executable, base, token, os.Getenv("LECTERN_ATTACH_HOST"), kind, id, script, dir)), 0700); err != nil {
		os.RemoveAll(dir)
		return err
	}
	argv, err := desktopTerminalCommand(script)
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = withoutEnv(withoutEnv(os.Environ(), "TMUX"), "TMUX_PANE")
	// Do not inherit the dashboard PTY. The emulator supplies its own terminal.
	if err = cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(script); os.IsNotExist(err) {
			return nil
		}
		select {
		case err := <-exited:
			if err != nil {
				os.RemoveAll(dir)
				return fmt.Errorf("terminal launch failed: %w", err)
			}
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	os.RemoveAll(dir)
	return fmt.Errorf("terminal did not start; check your desktop display connection")
}
