package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
)

// Services supervise the same flock-guarded runtime used by `up` and the TUI.
func serviceStateDir() (string, error) { return localruntime.StateDir() }

func unitQuote(s string) string { return strconv.Quote(strings.ReplaceAll(s, "%", "%%")) }
func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func systemdUnit(binary, stateDir string) string {
	return fmt.Sprintf(`[Unit]
Description=Lectern local runtime
After=network-online.target

[Service]
ExecStart=%s local supervise
Restart=on-failure
RestartSec=2
KillMode=process
Environment=%s
Environment=%s

[Install]
WantedBy=default.target
`, unitQuote(binary), unitQuote("XDG_STATE_HOME="+filepath.Dir(filepath.Dir(stateDir))), unitQuote("PATH="+os.Getenv("PATH")))
}

func launchdPlist(binary, stateDir, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>build.lectern.serve</string>
<key>ProgramArguments</key><array><string>%s</string><string>local</string><string>supervise</string></array>
<key>EnvironmentVariables</key><dict>
<key>XDG_STATE_HOME</key><string>%s</string>
<key>PATH</key><string>%s</string>
</dict>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>AbandonProcessGroup</key><true/>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, xmlText(binary), xmlText(filepath.Dir(filepath.Dir(stateDir))), xmlText(os.Getenv("PATH")), xmlText(logPath), xmlText(logPath))
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
	stateDir := dir
	switch runtime.GOOS {
	case "linux":
		return installSystemdUserUnit(binary, stateDir)
	case "darwin":
		return installLaunchdAgent(binary, stateDir, filepath.Join(dir, "service.log"))
	default:
		return "", fmt.Errorf("--service is not supported on %s yet; run `lectern serve` yourself, e.g. from your OS's own startup mechanism", runtime.GOOS)
	}
}

func installSystemdUserUnit(binary, stateDir string) (string, error) {
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
	if err := os.WriteFile(unitPath, []byte(systemdUnit(binary, stateDir)), 0o644); err != nil {
		return "", err
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return "", fmt.Errorf("systemctl --user daemon-reload: %w: %s", err, out)
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", "lectern.service").CombinedOutput(); err != nil {
		return "", fmt.Errorf("systemctl --user enable --now lectern.service: %w: %s", err, out)
	}
	return "Installed and started the systemd user service (~/.config/systemd/user/lectern.service) for the same local board.\n" +
		"It only runs while you're logged in unless you enable lingering — run once:\n" +
		"  loginctl enable-linger $USER\n" +
		"That lets systemd start it at boot even with nobody logged in yet.", nil
}

func installLaunchdAgent(binary, stateDir, logPath string) (string, error) {
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
	if err := os.WriteFile(plistPath, []byte(launchdPlist(binary, stateDir, logPath)), 0o644); err != nil {
		return "", err
	}
	if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
		return "", fmt.Errorf("launchctl load -w %s: %w: %s", plistPath, err, out)
	}
	return "Installed and started the LaunchAgent (~/Library/LaunchAgents/build.lectern.serve.plist) for the same local board.\n" +
		"LaunchAgents run at login automatically; no extra step needed.", nil
}
