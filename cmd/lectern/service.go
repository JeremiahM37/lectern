package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// serviceStateDir is where `lectern up --service` points the persistent,
// systemd/launchd-managed instance's database and media — deliberately not
// the local-runtime state directory (~/.local/state/lectern/local), which is
// owned by the CLI's own flock-guarded singleton and not meant to be shared
// with a second, independently-supervised process writing the same files.
func serviceStateDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(base, "lectern")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// systemdUnit renders the user unit `lectern up --service` installs on
// Linux. It runs `lectern serve` (the hosted control plane) rather than the
// CLI-managed local runtime, since a systemd-supervised process needs its own
// stable state, independent of any terminal's lock.
func systemdUnit(binary, dbPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Lectern control plane (installed by "lectern up --service")
After=network-online.target

[Service]
ExecStart=%s serve
Restart=on-failure
RestartSec=2
Environment=LECTERN_HOST=127.0.0.1
Environment=LECTERN_PORT=9110
Environment=LECTERN_DB=%s

[Install]
WantedBy=default.target
`, binary, dbPath)
}

// launchdPlist renders the macOS LaunchAgent `lectern up --service` installs.
func launchdPlist(binary, dbPath, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>build.lectern.serve</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>LECTERN_HOST</key>
    <string>127.0.0.1</string>
    <key>LECTERN_PORT</key>
    <string>9110</string>
    <key>LECTERN_DB</key>
    <string>%s</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, binary, dbPath, logPath, logPath)
}

// installService writes and enables a per-user background service so Lectern
// comes back after a reboot with no terminal open. It only touches this
// user's own systemd/launchd configuration — never /etc, and never a system
// unit — matching the same "no sudo unless asked" posture as install.sh.
func installService(binary string) (string, error) {
	dir, err := serviceStateDir()
	if err != nil {
		return "", err
	}
	dbPath := filepath.Join(dir, "lectern.db")
	switch runtime.GOOS {
	case "linux":
		return installSystemdUserUnit(binary, dbPath)
	case "darwin":
		return installLaunchdAgent(binary, dbPath, filepath.Join(dir, "service.log"))
	default:
		return "", fmt.Errorf("--service is not supported on %s yet; run `lectern serve` yourself, e.g. from your OS's own startup mechanism", runtime.GOOS)
	}
}

func installSystemdUserUnit(binary, dbPath string) (string, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", errors.New("systemctl not found; install a systemd user service manually or run `lectern serve` at login")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return "", err
	}
	unitPath := filepath.Join(unitDir, "lectern.service")
	if err := os.WriteFile(unitPath, []byte(systemdUnit(binary, dbPath)), 0o644); err != nil {
		return "", err
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return "", fmt.Errorf("systemctl --user daemon-reload: %w: %s", err, out)
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", "lectern.service").CombinedOutput(); err != nil {
		return "", fmt.Errorf("systemctl --user enable --now lectern.service: %w: %s", err, out)
	}
	return "Installed and started the systemd user service (~/.config/systemd/user/lectern.service) on http://127.0.0.1:9110.\n" +
		"It only runs while you're logged in unless you enable lingering — run once:\n" +
		"  loginctl enable-linger $USER\n" +
		"That lets systemd start it at boot even with nobody logged in yet.", nil
}

func installLaunchdAgent(binary, dbPath, logPath string) (string, error) {
	if _, err := exec.LookPath("launchctl"); err != nil {
		return "", errors.New("launchctl not found; add a LaunchAgent manually or run `lectern serve` at login")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	agentDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return "", err
	}
	plistPath := filepath.Join(agentDir, "build.lectern.serve.plist")
	if err := os.WriteFile(plistPath, []byte(launchdPlist(binary, dbPath, logPath)), 0o644); err != nil {
		return "", err
	}
	if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
		return "", fmt.Errorf("launchctl load -w %s: %w: %s", plistPath, err, out)
	}
	return "Installed and started the LaunchAgent (~/Library/LaunchAgents/build.lectern.serve.plist) on http://127.0.0.1:9110.\n" +
		"LaunchAgents run at login automatically; no extra step needed.", nil
}
